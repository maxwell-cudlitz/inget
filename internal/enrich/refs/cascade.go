// The invalidation cascade: telling the items that reference a record that it moved.
//
// One indexed UPDATE marks every edge pointing at the record, so a widely-referenced record
// costs the same as an unreferenced one no matter how large its fan-out. The expense is on the
// other side — each marked item is reprocessed by a later run — which is why the per-run cap
// belongs to the run that consumes marks (Defer) rather than to the one that sets them.
//
// Two properties matter and both come from the depth carried on the mark rather than from a
// visited set:
//
//   - Termination. A record processed at depth d marks at d+1 and a record already at the
//     bound marks nothing, so X→Y→X stops after the bound instead of ping-ponging forever.
//   - Cost. Cascade is called only for an item that published something new, so a marked item
//     whose reference turned out to resolve identically ends the cascade rather than passing
//     the mark along. No generator call happens on that path: the fragment fingerprints are
//     unchanged, so derivations hit the level-1 cache, and the level-2 input hash is unchanged,
//     so every view is skipped.
package refs

import (
	"context"
	"fmt"
	"log/slog"
)

// Cascade marks the referrers of one record for re-resolution, returning how many edges it
// marked. depth is the cascade depth the record was itself processed at — zero when it was
// processed because its own content changed — and maxDepth is enrich.max_reference_depth.
//
// It is a package function rather than a method on Set because the record being cascaded from
// need not declare any references itself: a github/repo item referenced by a monday/item has
// no reference block of its own, and it is still what changed.
func Cascade(ctx context.Context, store Store, datatype, itemID string, depth, maxDepth int) (int, error) {
	next := depth + 1
	if next > maxDepth {
		slog.DebugContext(ctx, "reference cascade stopped at the depth bound",
			"datatype", datatype, "item_id", itemID, "depth", depth, "max_reference_depth", maxDepth)
		return 0, nil
	}
	key := IngetKey(datatype, itemID)
	marked, err := store.MarkRefsStale(ctx, KindInget, key, next)
	if err != nil {
		return 0, fmt.Errorf("cascading from %s: %w", key, err)
	}
	if marked > 0 {
		slog.InfoContext(ctx, "invalidated referencing items",
			"referent", key, "edges_marked", marked, "depth", next)
	}
	return marked, nil
}

// WithinCap applies the per-run cascade cap to the items a run found marked, returning the
// ones to process now and logging the remainder.
//
// Nothing is written to defer the rest: a mark is cleared only by the marked item's own
// re-resolution, so an item left out here is found by the next run's identical query. That is
// the whole reason the mark lives on the edge instead of in a queue.
func WithinCap[T any](ctx context.Context, datatype string, stale []T, limit int) []T {
	if limit <= 0 || len(stale) <= limit {
		return stale
	}
	slog.WarnContext(ctx, "reference cascade exceeded the per-run cap, deferring the remainder",
		"datatype", datatype, "stale", len(stale), "max_cascade_per_run", limit,
		"deferred", len(stale)-limit)
	return stale[:limit]
}
