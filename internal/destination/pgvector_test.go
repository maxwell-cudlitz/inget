// The data path: upsert, search, delete and bulk load against a real pgvector. Schema and
// binding cases are in pgvector_schema_test.go.
package destination

import "testing"

func TestUpsertRoundTripsAndIsIdempotent(t *testing.T) {
	p := newStore(t)
	row := testRow("acme/one", "role", unitVector(1))
	row.Metadata = map[string]string{"language": "Go"}
	row.RelatedKeys = []string{"monday/item:42"}

	seed(t, p, []Row{row})
	if got := countRows(t, p); got != 1 {
		t.Fatalf("row count = %d, want 1", got)
	}
	// Writing the same identity again replaces rather than duplicates: the cascade re-upserts
	// a view whenever its embedding changes, and identity is content-independent.
	row.Text = "rewritten"
	seed(t, p, []Row{row})
	if got := countRows(t, p); got != 1 {
		t.Errorf("row count after re-upsert = %d, want 1", got)
	}

	hits, err := p.Search(ctx(t), SearchQuery{Embedding: row.Embedding, Datatype: testDatatype, Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search returned %d hits, want 1", len(hits))
	}
	if hits[0].Text != "rewritten" {
		t.Errorf("text = %q, want the value from the second upsert", hits[0].Text)
	}
	if hits[0].Metadata["language"] != "Go" {
		t.Errorf("metadata = %v, want language=Go", hits[0].Metadata)
	}
	if hits[0].ItemID != "acme/one" || hits[0].ViewName != "role" {
		t.Errorf("hit identity = %s/%s, want acme/one/role", hits[0].ItemID, hits[0].ViewName)
	}
}

func TestSearchFindsThePlantedNeighbour(t *testing.T) {
	p := newStore(t)
	const planted = "acme/planted"
	rows := make([]Row, 0, 501)
	for i := 1; i <= 500; i++ {
		rows = append(rows, testRow(itemName(i), "role", unitVector(i)))
	}
	target := unitVector(9999)
	rows = append(rows, testRow(planted, "role", target))
	seed(t, p, rows)

	hits, err := p.Search(ctx(t), SearchQuery{Embedding: target, Datatype: testDatatype, Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ItemID != planted {
		t.Fatalf("nearest hit = %+v, want %s", hits, planted)
	}
	// The planted vector is the query, so its cosine similarity is 1 up to halfvec's
	// precision. A score far from 1 means the vector was mangled on the way in.
	if hits[0].Score < 0.99 {
		t.Errorf("score = %v, want >= 0.99 for an exact match", hits[0].Score)
	}
}

func TestSearchFiltersByView(t *testing.T) {
	p := newStore(t)
	// One item in every view, plus enough neighbours that an HNSW scan without iterative
	// rescanning would return candidates from the wrong view and find nothing (D9).
	views := []string{"role", "surface", "stack", "stewardship"}
	rows := []Row{}
	for i := 1; i <= 200; i++ {
		rows = append(rows, testRow(itemName(i), views[i%len(views)], unitVector(i)))
	}
	target := unitVector(7777)
	rows = append(rows, testRow("acme/only-stewardship", "stewardship", target))
	seed(t, p, rows)

	hits, err := p.Search(ctx(t), SearchQuery{
		Embedding: target, Datatype: testDatatype, ViewName: "stewardship", Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search restricted to one view returned nothing")
	}
	for _, h := range hits {
		if h.ViewName != "stewardship" {
			t.Errorf("hit %s is from view %q, want stewardship only", h.ID, h.ViewName)
		}
	}
	if hits[0].ItemID != "acme/only-stewardship" {
		t.Errorf("nearest hit = %s, want acme/only-stewardship", hits[0].ItemID)
	}
}

func TestSearchIsScopedToOneDatatype(t *testing.T) {
	p := newStore(t)
	target := unitVector(5)
	other := testRow("1234", "substance", target)
	other.Datatype = "monday/item"
	seed(t, p, []Row{testRow("acme/one", "role", unitVector(1)), other})

	hits, err := p.Search(ctx(t), SearchQuery{Embedding: target, Datatype: testDatatype, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range hits {
		if h.ItemID == "1234" {
			t.Errorf("search for %s returned the monday/item row", testDatatype)
		}
	}
}

func TestDeleteItemRemovesEveryView(t *testing.T) {
	p := newStore(t)
	const doomed = "acme/doomed"
	seed(t, p, []Row{
		testRow(doomed, "role", unitVector(1)),
		testRow(doomed, "surface", unitVector(2)),
		testRow(doomed, "stack", unitVector(3)),
		testRow("acme/kept", "role", unitVector(4)),
	})

	if err := p.DeleteItem(ctx(t), testDatatype, doomed); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	if got := countRows(t, p); got != 1 {
		t.Errorf("row count after DeleteItem = %d, want 1 (the other item)", got)
	}
	// Deleting an item that is not there is how a tombstone from a partial run arrives.
	if err := p.DeleteItem(ctx(t), testDatatype, "acme/never-existed"); err != nil {
		t.Errorf("DeleteItem for an absent item: %v", err)
	}
}

func TestBulkLoadWritesAndMerges(t *testing.T) {
	p := newStore(t)
	rows := make([]Row, 0, 1000)
	for i := 1; i <= 1000; i++ {
		rows = append(rows, testRow(itemName(i), "role", unitVector(i)))
	}
	// The plan flagged CopyFrom into a halfvec column as unconfirmed. It is settled here by
	// not doing it: the staging column is text and the cast happens in the merge.
	if err := p.BulkLoad(ctx(t), rows); err != nil {
		t.Fatalf("BulkLoad: %v", err)
	}
	if got := countRows(t, p); got != len(rows) {
		t.Fatalf("row count = %d, want %d", got, len(rows))
	}
	// A second load of the same identities must merge, not duplicate and not fail.
	for i := range rows {
		rows[i].Text = "reloaded"
	}
	if err := p.BulkLoad(ctx(t), rows); err != nil {
		t.Fatalf("second BulkLoad: %v", err)
	}
	if got := countRows(t, p); got != len(rows) {
		t.Errorf("row count after the second load = %d, want %d", got, len(rows))
	}

	hits, err := p.Search(ctx(t), SearchQuery{Embedding: unitVector(500), Datatype: testDatatype, Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Text != "reloaded" {
		t.Errorf("nearest hit = %+v, want the reloaded text", hits)
	}
}

func TestBulkLoadLeavesNoStagingTable(t *testing.T) {
	p := newStore(t)
	if err := p.BulkLoad(ctx(t), []Row{testRow("acme/one", "role", unitVector(1))}); err != nil {
		t.Fatalf("BulkLoad: %v", err)
	}
	// ON COMMIT DROP is what makes two loads on one pooled connection safe; without it the
	// second would fail rather than leak silently.
	var leftover int
	err := p.pool.QueryRow(ctx(t),
		`SELECT count(*) FROM pg_tables WHERE tablename = 'inget_bulk_load'`).Scan(&leftover)
	if err != nil {
		t.Fatalf("looking for the staging table: %v", err)
	}
	if leftover != 0 {
		t.Errorf("found %d staging tables after BulkLoad, want none", leftover)
	}
	if err := p.BulkLoad(ctx(t), []Row{testRow("acme/two", "role", unitVector(2))}); err != nil {
		t.Errorf("second BulkLoad: %v", err)
	}
}

func TestEmptyWritesDoNothing(t *testing.T) {
	p := newStore(t)
	if err := p.Upsert(ctx(t), nil); err != nil {
		t.Errorf("Upsert(nil): %v", err)
	}
	if err := p.BulkLoad(ctx(t), nil); err != nil {
		t.Errorf("BulkLoad(nil): %v", err)
	}
	if got := countRows(t, p); got != 0 {
		t.Errorf("row count = %d, want 0", got)
	}
}

func TestUpsertSpansBatches(t *testing.T) {
	p := newStore(t)
	p.opts.BatchSize = 7 // a size that does not divide the row count
	rows := make([]Row, 0, 50)
	for i := 1; i <= 50; i++ {
		rows = append(rows, testRow(itemName(i), "role", unitVector(i)))
	}
	seed(t, p, rows)
	if got := countRows(t, p); got != len(rows) {
		t.Errorf("row count = %d, want %d; a partial final batch was dropped", got, len(rows))
	}
}

func TestSearchRejectsBadQueries(t *testing.T) {
	p := newStore(t)
	cases := []struct {
		name string
		q    SearchQuery
	}{
		{"no datatype", SearchQuery{Embedding: unitVector(1), Limit: 1}},
		{"no limit", SearchQuery{Embedding: unitVector(1), Datatype: testDatatype}},
		{"no vector", SearchQuery{Datatype: testDatatype, Limit: 1}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := p.Search(ctx(t), tt.q); err == nil {
				t.Errorf("Search(%s): want an error", tt.name)
			}
		})
	}
}
