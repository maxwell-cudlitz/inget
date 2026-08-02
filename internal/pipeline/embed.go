// Stages 8 and 9: embedding the views that need it and writing them to every destination.
//
// One Embed call carries every view of an item that needs a vector. The embedder client
// already splits a call into models.embedder.batch_size requests, so batching here turns
// eight round trips per item into one without duplicating that logic.
package pipeline

import (
	"context"
	"fmt"

	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/destination"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// embedViews embeds every pending view in one call and pairs each vector with the guard
// state and destination row it belongs to.
func embedViews(ctx context.Context, ex *execution, req viewRequest, pending []pendingView) ([]state.ViewState, []destination.Row, error) {
	cfg := ex.deps.Config
	emb := ex.deps.Embedder

	texts := make([]string, len(pending))
	for i, p := range pending {
		texts[i] = p.text
	}
	vectors, err := emb.Embed(ctx, texts)
	if err != nil {
		return nil, nil, fmt.Errorf("embedding %d views of %s/%s: %w",
			len(texts), cfg.Name, req.itemID, err)
	}
	if len(vectors) != len(pending) {
		return nil, nil, fmt.Errorf("embedding %d views of %s/%s: got %d vectors",
			len(texts), cfg.Name, req.itemID, len(vectors))
	}

	views := make([]state.ViewState, 0, len(pending))
	rows := make([]destination.Row, 0, len(pending))
	for i, p := range pending {
		views = append(views, state.ViewState{
			Name:         p.name,
			InputHash:    p.inputHash,
			Text:         p.text,
			EmbeddedHash: delta.EmbeddingHash(p.text),
			Model:        emb.Model(),
			Dims:         emb.Dims(),
			Signature:    emb.Signature(),
		})
		// Row.ID is left for the destination to derive from the row's identity, so the two
		// packages cannot disagree about what a row's primary key is.
		rows = append(rows, destination.Row{
			Datatype:  cfg.Name,
			ItemID:    req.itemID,
			ViewName:  p.name,
			Text:      p.text,
			Embedding: vectors[i],
			Model:     emb.Model(),
			Dims:      emb.Dims(),
			Signature: emb.Signature(),
			Metadata:  req.metadata,
		})
	}
	ex.stats.addEmbeddings(len(pending))
	return views, rows, nil
}

// upsertRows writes rows to every configured destination.
func upsertRows(ctx context.Context, ex *execution, rows []destination.Row) error {
	if len(rows) == 0 {
		return nil
	}
	for _, name := range ex.deps.Config.Destinations {
		dest, ok := ex.deps.Destinations[name]
		if !ok {
			return fmt.Errorf("destination %q not found in deps", name)
		}
		if err := dest.Upsert(ctx, rows); err != nil {
			return fmt.Errorf("upserting to %s: %w", name, err)
		}
	}
	return nil
}
