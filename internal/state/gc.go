// The state half of `inget state gc`: what is still referenced, and what is collectable.
//
// Phase 2 of the collection in docs/artifact-envelope.md deletes blobs no retained run and
// no live fragment references, so this package's contribution is the second half of that
// predicate. Phase 3 deletes the derivations of fragments that have been absent for longer
// than retention.missing_runs, which is the only place a paid-for derivation is discarded.
//
// Neither read is scoped to a datatype. A blob is content-addressed and therefore shared
// across datatypes and sources, so a per-datatype answer would let one datatype's collection
// delete a blob another datatype still points at.
package state

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	blobRefsSQL = `SELECT DISTINCT blob_ref FROM fragments WHERE blob_ref IS NOT NULL`

	// A correlated EXISTS rather than a join: the fragment a derivation belongs to is
	// addressed by the whole primary key of fragments, so the subquery is one index probe,
	// and a plain DELETE ... WHERE EXISTS is accepted by both dialects unchanged.
	//
	// Fragments are never deleted, only counted missing, so a derivation whose fragment row
	// has vanished does not occur and is not handled here; if some future path starts
	// deleting fragment rows, this predicate stops seeing those derivations and they leak.
	derivationCollectSQL = `
DELETE FROM derivations WHERE EXISTS (
    SELECT 1 FROM fragments f
    WHERE f.datatype = derivations.datatype
      AND f.item_id  = derivations.item_id
      AND f.frag_key = derivations.frag_key
      AND f.missing_runs > ?
)`
)

// LiveBlobRefs implements Store.
func (s *store) LiveBlobRefs(ctx context.Context) (map[string]struct{}, error) {
	refs := map[string]struct{}{}
	err := s.each(ctx, blobRefsSQL, nil, func(rows *sql.Rows) error {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return fmt.Errorf("scanning blob reference: %w", err)
		}
		refs[ref] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading live blob references: %w", err)
	}
	return refs, nil
}

// CollectDerivations implements Store.
func (s *store) CollectDerivations(ctx context.Context, missingRuns int) (int, error) {
	if missingRuns < 1 {
		return 0, fmt.Errorf("collecting derivations: missing_runs %d, want 1 or more", missingRuns)
	}
	affected, err := s.exec(ctx, derivationCollectSQL, missingRuns)
	if err != nil {
		return 0, fmt.Errorf("collecting derivations of fragments missing for more than %d runs: %w",
			missingRuns, err)
	}
	return int(affected), nil
}
