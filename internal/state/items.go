// Level 0 of the cascade: items and their fingerprints.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const (
	// A reappearing item is an upsert that clears the tombstone, not an insert: its
	// cached derivations are still keyed by fragment fingerprint and are still valid.
	itemUpsertSQL = `
INSERT INTO items (datatype, item_id, source_name, fingerprint, composed_hash, metadata, last_run_id)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (datatype, item_id) DO UPDATE SET
    source_name   = excluded.source_name,
    fingerprint   = excluded.fingerprint,
    composed_hash = excluded.composed_hash,
    metadata      = excluded.metadata,
    last_run_id   = excluded.last_run_id,
    last_seen_at  = CURRENT_TIMESTAMP,
    deleted_at    = NULL`

	itemFingerprintsSQL = `
SELECT item_id, fingerprint FROM items WHERE datatype = ? AND deleted_at IS NULL`

	itemSelectSQL = `
SELECT source_name, fingerprint, composed_hash, metadata, last_run_id
FROM items WHERE datatype = ? AND item_id = ? AND deleted_at IS NULL`

	itemTombstoneSQL = `
UPDATE items SET deleted_at = CURRENT_TIMESTAMP
WHERE datatype = ? AND item_id = ? AND deleted_at IS NULL`
)

// ItemFingerprints implements Store.
func (s *store) ItemFingerprints(ctx context.Context, datatype string) (map[string]string, error) {
	fingerprints := map[string]string{}
	err := s.each(ctx, itemFingerprintsSQL, []any{datatype}, func(rows *sql.Rows) error {
		var id, fingerprint string
		if err := rows.Scan(&id, &fingerprint); err != nil {
			return fmt.Errorf("scanning item fingerprint: %w", err)
		}
		fingerprints[id] = fingerprint
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s item fingerprints: %w", datatype, err)
	}
	return fingerprints, nil
}

// Item implements Store.
func (s *store) Item(ctx context.Context, datatype, itemID string) (Item, bool, error) {
	var (
		it           = Item{ID: itemID}
		composedHash sql.NullString
		runID        sql.NullString
		metadata     []byte
	)
	found, err := s.get(ctx, itemSelectSQL, []any{datatype, itemID},
		&it.Source, &it.Fingerprint, &composedHash, &metadata, &runID)
	if err != nil {
		return Item{}, false, fmt.Errorf("reading item %s/%s: %w", datatype, itemID, err)
	}
	if !found {
		return Item{}, false, nil
	}
	if err := json.Unmarshal(metadata, &it.Metadata); err != nil {
		return Item{}, false, fmt.Errorf("decoding metadata of item %s/%s: %w", datatype, itemID, err)
	}
	it.ComposedHash = composedHash.String
	it.RunID = runID.String
	return it, true, nil
}

// PutItem implements Store.
func (s *store) PutItem(ctx context.Context, datatype string, it Item) error {
	if it.ID == "" {
		return fmt.Errorf("item of datatype %s has no ID", datatype)
	}
	metadata, err := marshalJSON(it.Metadata)
	if err != nil {
		return fmt.Errorf("item %s/%s: %w", datatype, it.ID, err)
	}
	_, err = s.exec(ctx, itemUpsertSQL,
		datatype, it.ID, it.Source, it.Fingerprint,
		nullText(it.ComposedHash), metadata, nullText(it.RunID))
	if err != nil {
		return fmt.Errorf("writing item %s/%s: %w", datatype, it.ID, err)
	}
	return nil
}

// Tombstone implements Store.
func (s *store) Tombstone(ctx context.Context, datatype string, ids []string) error {
	rows := make([][]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []any{datatype, id})
	}
	err := s.inTx(ctx, func(t tx) error {
		return t.execMany(ctx, itemTombstoneSQL, rows)
	})
	if err != nil {
		return fmt.Errorf("tombstoning %d %s items: %w", len(ids), datatype, err)
	}
	return nil
}
