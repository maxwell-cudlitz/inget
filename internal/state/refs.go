// Reference edges and the reverse lookup the invalidation cascade walks (D12).
//
// PutRefs replaces an item's whole edge set rather than merging into it. Merging would leave
// an edge behind when a reference is removed from config or a resolver stops returning a
// key, and a stale reverse edge invalidates views forever: the referent keeps changing, the
// referencing item keeps regenerating, and nothing in the output says why.
package state

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	refDeleteSQL = `DELETE FROM refs WHERE from_datatype = ? AND from_item_id = ?`

	refInsertSQL = `
INSERT INTO refs (from_datatype, from_item_id, ref_name, to_kind, to_key, to_fingerprint)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (from_datatype, from_item_id, ref_name, to_key) DO UPDATE SET
    to_kind        = excluded.to_kind,
    to_fingerprint = excluded.to_fingerprint,
    resolved_at    = CURRENT_TIMESTAMP`

	refReverseSQL = `
SELECT DISTINCT from_datatype, from_item_id FROM refs
WHERE to_kind = ? AND to_key = ? ORDER BY from_datatype, from_item_id`
)

// PutRefs implements Store.
func (s *store) PutRefs(ctx context.Context, from ItemKey, edges []RefEdge) error {
	rows := make([][]any, 0, len(edges))
	for _, e := range edges {
		if e.Name == "" || e.Kind == "" || e.Key == "" {
			return fmt.Errorf("reference edge of %s/%s needs a name, kind and key, got %+v",
				from.Datatype, from.ItemID, e)
		}
		rows = append(rows, []any{
			from.Datatype, from.ItemID, e.Name, e.Kind, e.Key, nullText(e.Fingerprint),
		})
	}
	err := s.inTx(ctx, func(t tx) error {
		if err := t.exec(ctx, refDeleteSQL, from.Datatype, from.ItemID); err != nil {
			return err
		}
		return t.execMany(ctx, refInsertSQL, rows)
	})
	if err != nil {
		return fmt.Errorf("writing %d reference edges of %s/%s: %w",
			len(edges), from.Datatype, from.ItemID, err)
	}
	return nil
}

// ReferencedBy implements Store.
func (s *store) ReferencedBy(ctx context.Context, kind, key string) ([]ItemKey, error) {
	var referrers []ItemKey
	err := s.each(ctx, refReverseSQL, []any{kind, key}, func(rows *sql.Rows) error {
		var k ItemKey
		if err := rows.Scan(&k.Datatype, &k.ItemID); err != nil {
			return fmt.Errorf("scanning reference edge: %w", err)
		}
		referrers = append(referrers, k)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading items referencing %s:%s: %w", kind, key, err)
	}
	return referrers, nil
}
