// related_keys: the reverse-reference column, and the one thing about it that a fake cannot
// establish.
//
// A resolved reference key reaching a row is asserted by the pipeline's own tests. What needs a
// real PostgreSQL is that the column is queryable the way an operator would query it — array
// overlap against the GIN index — because "the value is in a TEXT[] column" and "the value can
// be found by the index built over that column" are different claims (D12).
package destination

import "testing"

func TestRelatedKeysAreQueryableByOverlap(t *testing.T) {
	p := newStore(t)
	linked := testRow("acme/linked", "role", unitVector(1))
	linked.RelatedKeys = []string{"github/repo|acme/thing", "ticket|TICKET-7"}
	unlinked := testRow("acme/unlinked", "role", unitVector(2))
	seed(t, p, []Row{linked, unlinked})

	var itemID string
	err := p.pool.QueryRow(t.Context(),
		"SELECT item_id FROM "+p.opts.Table+" WHERE related_keys && $1",
		[]string{"github/repo|acme/thing"}).Scan(&itemID)
	if err != nil {
		t.Fatalf("querying related_keys: %v", err)
	}
	if itemID != "acme/linked" {
		t.Errorf("item_id = %q, want the row carrying the related key", itemID)
	}

	// The index is what makes the query worth having at corpus scale, so its presence is part of
	// the contract rather than an implementation detail.
	var indexes int
	err = p.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_indexes
		WHERE tablename = $1 AND indexdef ILIKE '%gin%related_keys%'`, p.opts.Table).Scan(&indexes)
	if err != nil {
		t.Fatalf("reading indexes: %v", err)
	}
	if indexes == 0 {
		t.Error("no GIN index over related_keys; an overlap query would sequentially scan")
	}
}
