// Levels 2 and 3 of the cascade: generated views, their scoped input hash, and the hash of
// the text that was actually embedded.
package state

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	viewSelectSQL = `
SELECT view_name, input_hash, text, embedded_hash, model, dims, signature
FROM views WHERE datatype = ? AND item_id = ?`

	// Ordered so that a caller sampling from the result gets the same set on every run
	// regardless of how the database happens to return rows.
	viewedItemsSQL = `
SELECT DISTINCT item_id FROM views WHERE datatype = ? ORDER BY item_id`

	viewUpsertSQL = `
INSERT INTO views (datatype, item_id, view_name, input_hash, text, embedded_hash, model, dims, signature)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (datatype, item_id, view_name) DO UPDATE SET
    input_hash    = excluded.input_hash,
    text          = excluded.text,
    embedded_hash = excluded.embedded_hash,
    model         = excluded.model,
    dims          = excluded.dims,
    signature     = excluded.signature,
    updated_at    = CURRENT_TIMESTAMP`
)

// ViewState implements Store.
func (s *store) ViewState(ctx context.Context, datatype, itemID string) (map[string]ViewState, error) {
	views := map[string]ViewState{}
	err := s.each(ctx, viewSelectSQL, []any{datatype, itemID}, func(rows *sql.Rows) error {
		var (
			v            ViewState
			embeddedHash sql.NullString
			model        sql.NullString
			dims         sql.NullInt64
		)
		if err := rows.Scan(&v.Name, &v.InputHash, &v.Text, &embeddedHash, &model, &dims, &v.Signature); err != nil {
			return fmt.Errorf("scanning view: %w", err)
		}
		v.EmbeddedHash = embeddedHash.String
		v.Model = model.String
		v.Dims = int(dims.Int64)
		views[v.Name] = v
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading views of %s/%s: %w", datatype, itemID, err)
	}
	return views, nil
}

// ViewedItems implements Store.
func (s *store) ViewedItems(ctx context.Context, datatype string) ([]string, error) {
	var ids []string
	err := s.each(ctx, viewedItemsSQL, []any{datatype}, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scanning viewed item: %w", err)
		}
		ids = append(ids, id)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s items with views: %w", datatype, err)
	}
	return ids, nil
}

// viewArgs builds the arguments of viewUpsertSQL. Shared by PutViewState and
// CheckpointItem.
func viewArgs(datatype, itemID string, v ViewState) ([]any, error) {
	if v.Name == "" {
		return nil, fmt.Errorf("view of %s/%s has no name", datatype, itemID)
	}
	return []any{
		datatype, itemID, v.Name, v.InputHash, v.Text,
		nullText(v.EmbeddedHash), nullText(v.Model), nullNumber(v.Dims), v.Signature,
	}, nil
}

// PutViewState implements Store.
func (s *store) PutViewState(ctx context.Context, datatype, itemID string, v ViewState) error {
	args, err := viewArgs(datatype, itemID, v)
	if err != nil {
		return err
	}
	if _, err := s.exec(ctx, viewUpsertSQL, args...); err != nil {
		return fmt.Errorf("writing view %s of %s/%s: %w", v.Name, datatype, itemID, err)
	}
	return nil
}
