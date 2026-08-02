// Per-item processing: the stage sequence one worker executes for one item.
//
// Stages, matching the design's Enrichment Pipeline section:
//  2. Read the item record and its persisted fragment state.
//  3. Reconcile fragments (level-1 delta).
//  4. Resolve references, record their reverse edges, collect related keys (D12).
//  5. Derive per-fragment artifacts, from cache where the level-1 key hits.
//  6. Compose, per view, over the fragments and references that view depends on.
//  7. Generate the views whose scoped input hash or signature moved.
//  8. Annotate metadata.
//  9. Embed the views whose text changed.
//  10. Upsert to every configured destination.
//  11. Checkpoint the item: every guard, its reference edges, and the work row, in one
//     transaction, then tell the items referencing this one that it moved.
//
// Nothing durable is written until the checkpoint, and the checkpoint runs after the upsert
// returns. A guard is a claim that the work below it happened, so an item that fails anywhere
// above leaves state untouched and reconciles as changed on the next run. Fragment derivations
// are the exception and are cached as they are produced: they are a cache rather than a guard,
// and discarding paid-for tokens because a later view failed is the one outcome worse than
// repeating the work.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/enrich/refs"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// processItem executes the enrichment cascade for one item.
func processItem(ctx context.Context, ex *execution, rec *artifact.Record) error {
	cfg := ex.deps.Config
	itemID := rec.ItemID

	cached, err := ex.deps.State.Fragments(ctx, cfg.Name, itemID)
	if err != nil {
		return fmt.Errorf("loading fragments for %s/%s: %w", cfg.Name, itemID, err)
	}
	fragDelta := delta.Reconcile(fingerprints(cached), incoming(rec))

	resolved, err := resolveReferences(ctx, ex, rec)
	if err != nil {
		return err
	}

	enriched, err := enrichFragments(ctx, ex, rec)
	if err != nil {
		return err
	}

	viewStates, err := ex.deps.State.ViewState(ctx, cfg.Name, itemID)
	if err != nil {
		return fmt.Errorf("loading views for %s/%s: %w", cfg.Name, itemID, err)
	}

	entries := append(buildComposeEntries(rec, enriched), resolved.Entries...)
	out, err := processViews(ctx, ex, viewRequest{
		itemID:      itemID,
		entries:     entries,
		changedKeys: append(changedKeys(fragDelta), resolved.ChangedKeys...),
		anyChanged:  resolved.MetadataChanged,
		refDigest:   resolved.Digest,
		metadata:    annotateMetadata(cfg, rec, resolved),
		relatedKeys: resolved.RelatedKeys,
		existing:    viewStates,
	})
	if err != nil {
		return err
	}

	if err := upsertRows(ctx, ex, out.rows); err != nil {
		return err
	}

	// The unscoped composition is the item's level-2 record: no view is generated from it,
	// but `inget state show` and a future reindex need one hash that says what the item
	// looked like when it was last processed.
	composed := delta.Compose(entries, cfg.ComposeOrder, cfg.ComposeMaxChars)
	cp := state.Checkpoint{
		RunID: ex.runID,
		Item: state.Item{
			ID:           itemID,
			Source:       cfg.Source,
			Fingerprint:  rec.Fingerprint,
			ComposedHash: composed.Hash,
			Metadata:     rec.Metadata,
			RunID:        ex.runID,
		},
		Fragments: buildFragmentStates(rec),
		Views:     out.views,
		Refs:      resolved.Edges,
	}
	if err := ex.deps.State.CheckpointItem(ctx, cfg.Name, cp); err != nil {
		return err
	}

	cascade(ctx, ex, itemID, resolved.Depth, len(out.rows) > 0)
	ex.stats.addProcessed(1)
	return nil
}

// resolveReferences is stage 4: look up this item's declared referents.
//
// A datatype with no references still returns a usable zero value, so the stages below it need
// no branch. Fragment content is read lazily and only for the keys a key_from glob matched.
func resolveReferences(ctx context.Context, ex *execution, rec *artifact.Record) (refs.Resolution, error) {
	cfg := ex.deps.Config
	from := state.ItemKey{Datatype: cfg.Name, ItemID: rec.ItemID}

	if ex.deps.Refs == nil {
		return refs.Resolution{}, nil
	}
	stored, err := ex.deps.State.Refs(ctx, from)
	if err != nil {
		return refs.Resolution{}, fmt.Errorf("loading reference edges of %s/%s: %w", cfg.Name, rec.ItemID, err)
	}

	blobs := make(map[string]artifact.Fragment, len(rec.Fragments))
	keys := make([]string, 0, len(rec.Fragments))
	for _, frag := range rec.Fragments {
		blobs[frag.Key] = frag
		keys = append(keys, frag.Key)
	}

	resolved, err := ex.deps.Refs.Resolve(ctx, refs.Input{
		Datatype:  cfg.Name,
		ItemID:    rec.ItemID,
		Metadata:  rec.Metadata,
		Fragments: keys,
		Stored:    stored,
		Content: func(ctx context.Context, key string) (string, error) {
			return loadFragmentContent(ctx, ex.arts, blobs[key])
		},
	})
	if err != nil {
		return refs.Resolution{}, err
	}
	ex.stats.addReferences(len(resolved.Edges))
	return resolved, nil
}

// cascade is the second half of stage 11: mark the items referencing this one.
//
// It runs only when the item published something — its own content changed, or a view was
// re-embedded — because an item that resolved to exactly what it held before has nothing to
// tell its referrers, and stopping there is what keeps a reference cycle from costing a
// reprocess on every future run.
//
// A failure to mark is logged rather than returned: the item's own work is committed and
// correct, and the next change to it will mark again.
func cascade(ctx context.Context, ex *execution, itemID string, depth int, republished bool) {
	if !ex.changed[itemID] && !republished {
		return
	}
	// state.Store's method set covers refs.Store, so this is a compile-time check rather
	// than an assertion that could fail at runtime.
	var store refs.Store = ex.deps.State
	if _, err := refs.Cascade(ctx, store, ex.deps.Config.Name, itemID, depth, ex.refDepth); err != nil {
		slog.WarnContext(ctx, "cascading reference invalidation",
			"datatype", ex.deps.Config.Name, "item_id", itemID, "error", err)
	}
}

// fingerprints reduces persisted fragment state to the key → fingerprint map Reconcile
// compares against.
func fingerprints(cached map[string]state.FragmentState) map[string]string {
	fps := make(map[string]string, len(cached))
	for k, f := range cached {
		fps[k] = f.Fingerprint
	}
	return fps
}

// incoming converts an artifact record's fragments into reconciliation inputs.
func incoming(rec *artifact.Record) []delta.IncomingFragment {
	in := make([]delta.IncomingFragment, 0, len(rec.Fragments))
	for _, frag := range rec.Fragments {
		in = append(in, delta.IncomingFragment{Key: frag.Key, Fingerprint: frag.Fingerprint})
	}
	return in
}

// changedKeys is every fragment key whose content this run is responsible for: added,
// modified, and deleted. Deleted keys are included because a view that depended only on a
// removed fragment must regenerate without it, and nothing else would tell it to.
func changedKeys(d delta.Delta) []string {
	keys := make([]string, 0, len(d.Added)+len(d.Modified)+len(d.Deleted))
	keys = append(keys, d.Added...)
	keys = append(keys, d.Modified...)
	keys = append(keys, d.Deleted...)
	return keys
}
