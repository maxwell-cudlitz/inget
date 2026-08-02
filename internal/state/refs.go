// Reference edges, the reverse lookup the invalidation cascade walks, and the staleness mark
// that makes the cascade durable (D12).
//
// PutRefs replaces an item's whole edge set rather than merging into it. Merging would leave
// an edge behind when a reference is removed from config or a resolver stops returning a
// key, and a stale reverse edge invalidates views forever: the referent keeps changing, the
// referencing item keeps regenerating, and nothing in the output says why. Replacing also
// clears the staleness mark, which is why re-resolution and mark-clearing are one write.
//
// MarkRefsStale is the push half of the cascade and StaleReferrers is the pull half. They are
// deliberately asymmetric: marking is one indexed UPDATE regardless of fan-out, so a
// widely-referenced record costs one statement, while the per-run cap lives on the pull side
// where the expense actually is. Anything the cap leaves out keeps its mark and is found by
// the next run's identical query, so deferral needs no queue of its own.
package state

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	refDeleteSQL = `DELETE FROM refs WHERE from_datatype = ? AND from_item_id = ?`

	refInsertSQL = `
INSERT INTO refs (from_datatype, from_item_id, ref_name, to_kind, to_key, to_fingerprint, stale_depth)
VALUES (?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT (from_datatype, from_item_id, ref_name, to_key) DO UPDATE SET
    to_kind        = excluded.to_kind,
    to_fingerprint = excluded.to_fingerprint,
    stale_depth    = NULL,
    resolved_at    = CURRENT_TIMESTAMP`

	refSelectSQL = `
SELECT ref_name, to_kind, to_key, to_fingerprint, stale_depth FROM refs
WHERE from_datatype = ? AND from_item_id = ? ORDER BY ref_name, to_key`

	refReverseSQL = `
SELECT DISTINCT from_datatype, from_item_id FROM refs
WHERE to_kind = ? AND to_key = ? ORDER BY from_datatype, from_item_id`

	// A mark never deepens an existing one: the shallowest path to an invalidation is the one
	// that decides how much further the cascade may travel.
	refMarkStaleSQL = `
UPDATE refs SET stale_depth = ?
WHERE to_kind = ? AND to_key = ? AND (stale_depth IS NULL OR stale_depth > ?)`

	refStaleSQL = `
SELECT from_item_id, MIN(stale_depth) FROM refs
WHERE from_datatype = ? AND stale_depth IS NOT NULL
GROUP BY from_item_id ORDER BY from_item_id`
)

// PutRefs implements Store.
func (s *store) PutRefs(ctx context.Context, from ItemKey, edges []RefEdge) error {
	rows, err := refRows(from, edges)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(t tx) error { return putRefs(ctx, t, from, rows) })
	if err != nil {
		return fmt.Errorf("writing %d reference edges of %s/%s: %w",
			len(edges), from.Datatype, from.ItemID, err)
	}
	return nil
}

// refRows builds the arguments of refInsertSQL. Shared by PutRefs and CheckpointItem so the
// two cannot disagree about what an edge row is.
func refRows(from ItemKey, edges []RefEdge) ([][]any, error) {
	rows := make([][]any, 0, len(edges))
	for _, e := range edges {
		if e.Name == "" || e.Kind == "" || e.Key == "" {
			return nil, fmt.Errorf("reference edge of %s/%s needs a name, kind and key, got %+v",
				from.Datatype, from.ItemID, e)
		}
		rows = append(rows, []any{
			from.Datatype, from.ItemID, e.Name, e.Kind, e.Key, nullText(e.Fingerprint),
		})
	}
	return rows, nil
}

// putRefs replaces one item's edge set inside a transaction.
func putRefs(ctx context.Context, t tx, from ItemKey, rows [][]any) error {
	if err := t.exec(ctx, refDeleteSQL, from.Datatype, from.ItemID); err != nil {
		return err
	}
	return t.execMany(ctx, refInsertSQL, rows)
}

// Refs implements Store.
func (s *store) Refs(ctx context.Context, from ItemKey) ([]RefEdge, error) {
	var edges []RefEdge
	err := s.each(ctx, refSelectSQL, []any{from.Datatype, from.ItemID}, func(rows *sql.Rows) error {
		var (
			e           RefEdge
			fingerprint sql.NullString
			staleDepth  sql.NullInt64
		)
		if err := rows.Scan(&e.Name, &e.Kind, &e.Key, &fingerprint, &staleDepth); err != nil {
			return fmt.Errorf("scanning reference edge: %w", err)
		}
		e.Fingerprint = fingerprint.String
		e.StaleDepth = int(staleDepth.Int64)
		edges = append(edges, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading reference edges of %s/%s: %w", from.Datatype, from.ItemID, err)
	}
	return edges, nil
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

// MarkRefsStale implements Store.
func (s *store) MarkRefsStale(ctx context.Context, kind, key string, depth int) (int, error) {
	if depth < 1 {
		return 0, fmt.Errorf("marking references to %s:%s stale: depth %d, want 1 or more", kind, key, depth)
	}
	affected, err := s.exec(ctx, refMarkStaleSQL, depth, kind, key, depth)
	if err != nil {
		return 0, fmt.Errorf("marking references to %s:%s stale: %w", kind, key, err)
	}
	return int(affected), nil
}

// StaleReferrers implements Store.
func (s *store) StaleReferrers(ctx context.Context, datatype string) ([]StaleReferrer, error) {
	var stale []StaleReferrer
	err := s.each(ctx, refStaleSQL, []any{datatype}, func(rows *sql.Rows) error {
		var r StaleReferrer
		if err := rows.Scan(&r.ItemID, &r.Depth); err != nil {
			return fmt.Errorf("scanning stale referrer: %w", err)
		}
		stale = append(stale, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading stale %s referrers: %w", datatype, err)
	}
	return stale, nil
}
