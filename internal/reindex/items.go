// Per-batch work: read what state holds, embed it, write it, record it.
//
// The order inside a batch is the same discipline the pipeline uses. Destination rows are written
// before the state rows that attest to them, because a view's model and signature in state are a
// claim that a vector of that model exists in the destination. Reversed, an interrupted pass would
// leave state saying every view is current while the index still holds the previous model's
// vectors — and nothing would ever rewrite them, because the guard says there is no work.
//
// Views are embedded per batch rather than per item: one call carries every text in the batch, the
// embedder client splits it into batch_size requests, and a repeated text is sent once.
package reindex

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/destination"
	"github.com/maxwell-cudlitz/inget/internal/pipeline"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// pending is one item's work: the metadata every row of it carries, and the views that need a
// vector.
type pending struct {
	itemID   string
	metadata map[string]string
	related  []string
	views    []state.ViewState
}

// reindexBatch re-embeds one claimed batch of items.
//
// A per-item read failure fails that item and the pass continues, matching the pipeline: one
// item's missing row is one item's problem. An embedder failure aborts, because it is not.
func reindexBatch(ctx context.Context, deps Deps, o Options, runID string,
	ids []string, report *Report) error {
	batch := make([]pending, 0, len(ids))
	var texts []string

	for _, id := range ids {
		item, err := load(ctx, deps, o, id)
		if err != nil {
			slog.ErrorContext(ctx, "reindexing item failed",
				"datatype", o.Datatype, "item_id", id, "error", err.Error())
			if err := deps.State.CompleteWork(ctx, runID, o.Datatype, id, err); err != nil {
				return err
			}
			report.Failed++
			continue
		}
		if len(item.views) == 0 {
			if err := deps.State.CompleteWork(ctx, runID, o.Datatype, id, nil); err != nil {
				return err
			}
			report.Unchanged++
			continue
		}
		batch = append(batch, item)
		for _, v := range item.views {
			texts = append(texts, v.Text)
		}
	}
	if len(batch) == 0 {
		return nil
	}

	vectors, err := deps.Embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embedding %d views of %s: %w", len(texts), o.Datatype, err)
	}
	if len(vectors) != len(texts) {
		return fmt.Errorf("embedder returned %d vectors for %d views of %s",
			len(vectors), len(texts), o.Datatype)
	}
	return writeBatch(ctx, deps, o, runID, batch, vectors, report)
}

// load reads one item's metadata, reference keys and the views that need re-embedding.
func load(ctx context.Context, deps Deps, o Options, itemID string) (pending, error) {
	stored, found, err := deps.State.Item(ctx, o.Datatype, itemID)
	if err != nil {
		return pending{}, err
	}
	if !found {
		// Tombstoned between the sample and the claim. Its rows are the destination's to
		// delete, which the pipeline already did, so there is nothing to rewrite.
		return pending{}, nil
	}
	views, err := deps.State.ViewState(ctx, o.Datatype, itemID)
	if err != nil {
		return pending{}, err
	}
	related, err := relatedKeys(ctx, deps, o, itemID)
	if err != nil {
		return pending{}, err
	}

	item := pending{
		itemID:   itemID,
		metadata: pipeline.AnnotateMetadata(stored.Metadata, o.MetadataFields, related),
		related:  related,
	}
	// Views are rewritten in name order, so a batch's texts and its vectors line up
	// deterministically and a failure is reproducible.
	for _, name := range slices.Sorted(maps.Keys(views)) {
		v := views[name]
		if v.Text == "" || !inScope(o, name) || (!o.Force && current(deps, v)) {
			continue
		}
		item.views = append(item.views, v)
	}
	return item, nil
}

// writeBatch writes every row of the batch to every destination, then records the new guards and
// completes the work rows.
func writeBatch(ctx context.Context, deps Deps, o Options, runID string,
	batch []pending, vectors [][]float32, report *Report) error {
	emb := deps.Embedder
	rows := make([]destination.Row, 0, len(vectors))
	at := 0
	for _, item := range batch {
		for _, v := range item.views {
			rows = append(rows, destination.Row{
				Datatype:    o.Datatype,
				ItemID:      item.itemID,
				ViewName:    v.Name,
				Text:        v.Text,
				Embedding:   vectors[at],
				Model:       emb.Model(),
				Dims:        emb.Dims(),
				Signature:   emb.Signature(),
				Metadata:    item.metadata,
				RelatedKeys: item.related,
			})
			at++
		}
	}
	// BulkLoad, not Upsert: this is the path the interface documents it for, and a batch's rows
	// are one set-based merge rather than one statement per row.
	for _, name := range o.Destinations {
		if err := deps.Destinations[name].BulkLoad(ctx, rows); err != nil {
			return err
		}
	}

	for _, item := range batch {
		for _, v := range item.views {
			v.EmbeddedHash = delta.EmbeddingHash(v.Text)
			v.Model, v.Dims, v.Signature = emb.Model(), emb.Dims(), emb.Signature()
			if err := deps.State.PutViewState(ctx, o.Datatype, item.itemID, v); err != nil {
				return err
			}
			report.Vectors++
		}
		if err := deps.State.CompleteWork(ctx, runID, o.Datatype, item.itemID, nil); err != nil {
			return err
		}
		report.Items++
	}
	return nil
}

// relatedKeys returns the item's resolved reference keys, sorted and deduplicated the way the
// resolver produced them, so a rewritten row's related_keys column matches what the pipeline
// wrote.
func relatedKeys(ctx context.Context, deps Deps, o Options, itemID string) ([]string, error) {
	edges, err := deps.State.Refs(ctx, state.ItemKey{Datatype: o.Datatype, ItemID: itemID})
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(edges))
	for _, e := range edges {
		if e.Key != "" && !slices.Contains(keys, e.Key) {
			keys = append(keys, e.Key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// inScope reports whether a view name is one this pass was asked to rewrite.
func inScope(o Options, name string) bool {
	return o.fullScope() || slices.Contains(o.Views, name)
}

// current reports whether a stored view already carries the configured embedder's vector, which
// is what makes a second pass free.
func current(deps Deps, v state.ViewState) bool {
	emb := deps.Embedder
	return v.EmbeddedHash == delta.EmbeddingHash(v.Text) &&
		v.Model == emb.Model() && v.Dims == emb.Dims() && v.Signature == emb.Signature()
}
