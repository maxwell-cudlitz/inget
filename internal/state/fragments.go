// Level 1 of the cascade: fragments and their fingerprints.
package state

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	fragmentSelectSQL = `
SELECT frag_key, fingerprint, blob_ref, size_bytes, tier, missing_runs
FROM fragments WHERE datatype = ? AND item_id = ?`

	// Every fragment of the item is counted as missing first; the upsert below then
	// resets the ones this run actually saw. Two fixed statements rather than one
	// statement with a NOT IN list of up to max_fragments_per_item keys.
	fragmentMissingSQL = `
UPDATE fragments SET missing_runs = missing_runs + 1 WHERE datatype = ? AND item_id = ?`

	fragmentUpsertSQL = `
INSERT INTO fragments (datatype, item_id, frag_key, fingerprint, blob_ref, size_bytes, tier)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (datatype, item_id, frag_key) DO UPDATE SET
    fingerprint  = excluded.fingerprint,
    blob_ref     = excluded.blob_ref,
    size_bytes   = excluded.size_bytes,
    tier         = excluded.tier,
    last_seen_at = CURRENT_TIMESTAMP,
    missing_runs = 0`
)

// Fragments implements Store.
func (s *store) Fragments(ctx context.Context, datatype, itemID string) (map[string]FragmentState, error) {
	fragments := map[string]FragmentState{}
	err := s.each(ctx, fragmentSelectSQL, []any{datatype, itemID}, func(rows *sql.Rows) error {
		var (
			f       FragmentState
			blobRef sql.NullString
		)
		if err := rows.Scan(&f.Key, &f.Fingerprint, &blobRef, &f.SizeBytes, &f.Tier, &f.MissingRuns); err != nil {
			return fmt.Errorf("scanning fragment: %w", err)
		}
		f.BlobRef = blobRef.String
		fragments[f.Key] = f
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading fragments of %s/%s: %w", datatype, itemID, err)
	}
	return fragments, nil
}

// fragmentRows builds the argument rows of fragmentUpsertSQL. Shared by PutFragments and
// CheckpointItem.
func fragmentRows(datatype, itemID string, f []FragmentState) ([][]any, error) {
	rows := make([][]any, 0, len(f))
	for _, frag := range f {
		if frag.Key == "" {
			return nil, fmt.Errorf("fragment of %s/%s has no key", datatype, itemID)
		}
		rows = append(rows, []any{
			datatype, itemID, frag.Key, frag.Fingerprint,
			nullText(frag.BlobRef), frag.SizeBytes, frag.Tier,
		})
	}
	return rows, nil
}

// putFragments writes the missing-runs increment and the upserts inside t. The two
// statements are one unit: the increment counts every fragment of the item as missing and
// the upsert resets the ones this run saw, so a commit between them would leave every
// fragment one run staler than it is.
func putFragments(ctx context.Context, t tx, datatype, itemID string, rows [][]any) error {
	if err := t.exec(ctx, fragmentMissingSQL, datatype, itemID); err != nil {
		return err
	}
	return t.execMany(ctx, fragmentUpsertSQL, rows)
}

// PutFragments implements Store.
func (s *store) PutFragments(ctx context.Context, datatype, itemID string, f []FragmentState) error {
	rows, err := fragmentRows(datatype, itemID, f)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(t tx) error {
		return putFragments(ctx, t, datatype, itemID, rows)
	})
	if err != nil {
		return fmt.Errorf("writing %d fragments of %s/%s: %w", len(f), datatype, itemID, err)
	}
	return nil
}
