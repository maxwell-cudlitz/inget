// Integration cases for rebinding and pruning, the two operations `inget reindex` drives.
//
// Like the rest of this suite they need a real PostgreSQL with pgvector: what is under test is
// the registry row, the write path's reaction to it, and a DELETE predicate over the vector
// table. Set INGET_TEST_PG to a scratch database to run them.
package destination

import (
	"strings"
	"testing"
)

// secondModel is the embedder a rebind moves to. Same width as the fixture, because a different
// width is a different destination, not a rebind.
const (
	secondModel     = "test/embedder-v2"
	secondSignature = "openai:model=test/embedder-v2,dims=4"
)

func TestRebindModelReplacesTheBinding(t *testing.T) {
	p := newStore(t)
	ctx := ctx(t)
	seed(t, p, []Row{testRow(itemName(1), "purpose", unitVector(1))})

	// Re-asserting the same model is not a rebind, so nothing changed.
	changed, err := p.RebindModel(ctx, testModel, testDims, testSignature)
	if err != nil {
		t.Fatalf("RebindModel to the current model: %v", err)
	}
	if changed {
		t.Error("RebindModel reported a change when the binding already matched")
	}

	changed, err = p.RebindModel(ctx, secondModel, testDims, secondSignature)
	if err != nil {
		t.Fatalf("RebindModel: %v", err)
	}
	if !changed {
		t.Error("RebindModel reported no change after moving to another model")
	}

	// The point of the rebind: writes from the new embedder are now accepted, and the old
	// embedder's are not.
	fresh := Row{
		Datatype: testDatatype, ItemID: itemName(2), ViewName: "purpose", Text: "fresh",
		Embedding: unitVector(2), Model: secondModel, Dims: testDims, Signature: secondSignature,
	}
	if err := p.Upsert(ctx, []Row{fresh}); err != nil {
		t.Fatalf("Upsert under the new binding: %v", err)
	}
	stale := testRow(itemName(3), "purpose", unitVector(3))
	if err := p.Upsert(ctx, []Row{stale}); err == nil {
		t.Error("Upsert with the previous model's rows: want a rejection")
	}

	// A fresh handle must read the rebound registry row rather than the original one.
	other := openStore(t, testOptions(requirePostgres(t)))
	if err := other.AssertModel(ctx, secondModel, testDims, secondSignature); err != nil {
		t.Errorf("AssertModel against the rebound table: %v", err)
	}
	if err := other.AssertModel(ctx, testModel, testDims, testSignature); err == nil {
		t.Error("AssertModel with the previous model: want a rejection naming reindex")
	}
}

func TestRebindModelRefusesAnotherWidth(t *testing.T) {
	p := newStore(t)
	_, err := p.RebindModel(ctx(t), secondModel, testDims+1, secondSignature)
	if err == nil {
		t.Fatal("RebindModel with a different width: want an error")
	}
	if !strings.Contains(err.Error(), "cannot widen") {
		t.Errorf("error = %q, want it to say the column cannot be widened", err)
	}
}

func TestPruneStaleVectorsRemovesOnlyThePreviousModelsRows(t *testing.T) {
	p := newStore(t)
	ctx := ctx(t)

	// Two datatypes under the original binding, so the datatype filter has something to
	// distinguish.
	seed(t, p, []Row{
		testRow(itemName(1), "purpose", unitVector(1)),
		testRow(itemName(2), "purpose", unitVector(2)),
	})
	other := Row{
		Datatype: "monday/item", ItemID: "8891", ViewName: "summary", Text: "an item",
		Embedding: unitVector(3), Model: testModel, Dims: testDims, Signature: testSignature,
	}
	seed(t, p, []Row{other})

	if _, err := p.RebindModel(ctx, secondModel, testDims, secondSignature); err != nil {
		t.Fatalf("RebindModel: %v", err)
	}
	// One item is re-embedded under the new model, standing in for what a reindex pass writes.
	rewritten := Row{
		Datatype: testDatatype, ItemID: itemName(1), ViewName: "purpose", Text: "rewritten",
		Embedding: unitVector(4), Model: secondModel, Dims: testDims, Signature: secondSignature,
	}
	if err := p.Upsert(ctx, []Row{rewritten}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Scoped to one datatype: the other datatype's stale row survives, because nothing has
	// re-embedded it yet and deleting it would lose vectors a later pass can rewrite.
	deleted, err := p.PruneStaleVectors(ctx, testDatatype)
	if err != nil {
		t.Fatalf("PruneStaleVectors: %v", err)
	}
	if deleted != 1 {
		t.Errorf("PruneStaleVectors(%s) deleted %d rows, want 1", testDatatype, deleted)
	}
	if got := countRows(t, p); got != 2 {
		t.Errorf("table holds %d rows, want 2: the rewritten one and the other datatype's", got)
	}

	// Unscoped: every remaining row from the previous model goes.
	deleted, err = p.PruneStaleVectors(ctx, "")
	if err != nil {
		t.Fatalf("PruneStaleVectors: %v", err)
	}
	if deleted != 1 {
		t.Errorf("PruneStaleVectors(all) deleted %d rows, want 1", deleted)
	}
	if got := countRows(t, p); got != 1 {
		t.Errorf("table holds %d rows, want only the rewritten one", got)
	}
}

func TestPruneStaleVectorsNeedsABinding(t *testing.T) {
	p := newStore(t)
	// A handle that never asserted holds no binding, and pruning against no binding would
	// delete every row in the table.
	unbound := openStore(t, testOptions(requirePostgres(t)))
	if _, err := unbound.PruneStaleVectors(ctx(t), ""); err == nil {
		t.Error("PruneStaleVectors without a binding: want an error")
	}
	if got := countRows(t, p); got != 0 {
		t.Errorf("table holds %d rows after a refused prune, want 0", got)
	}
}
