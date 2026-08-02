// Schema and binding cases: what Migrate builds, and what AssertModel will and will not
// agree to (D7, D8). The data path is in pgvector_test.go.
package destination

import (
	"strings"
	"testing"
)

func TestMigrateIsIdempotent(t *testing.T) {
	p := newStore(t)
	// newStore already migrated; a second pass must be a no-op rather than a duplicate-
	// object error, because `inget migrate` is expected to be safe to re-run.
	if err := p.Migrate(ctx(t)); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestMigrateCreatesTheConfiguredColumnType(t *testing.T) {
	p := newStore(t)
	if got := columnType(t, p, p.opts.Table); got != "halfvec(4)" {
		t.Errorf("embedding column is %s, want halfvec(4)", got)
	}
}

func TestAssertModelBindsThenAgrees(t *testing.T) {
	p := newStore(t) // binds during setup
	// A second assertion with the same triple must succeed: every run makes it.
	if err := p.AssertModel(ctx(t), testModel, testDims, testSignature); err != nil {
		t.Errorf("re-asserting the same model: %v", err)
	}
}

func TestAssertModelRejectsAnotherModel(t *testing.T) {
	p := newStore(t)
	err := p.AssertModel(ctx(t), "other/embedder", testDims, testSignature)
	if err == nil {
		t.Fatal("AssertModel with another model: want an error")
	}
	// The message has to say what to do about it; a rejection with no remedy just stops a
	// pipeline (D7).
	for _, want := range []string{"other/embedder", testModel, "inget reindex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestAssertModelRejectsAnotherSignature(t *testing.T) {
	p := newStore(t)
	// Same model, different signature: a changed truncate_dims or embedder parameter. The
	// vectors already stored are not comparable with what this run would write.
	if err := p.AssertModel(ctx(t), testModel, testDims, "openai:model=test/embedder,dims=4,rev=2"); err == nil {
		t.Error("AssertModel with another signature: want an error")
	}
}

func TestAssertModelRejectsAnotherWidth(t *testing.T) {
	p := newStore(t)
	err := p.AssertModel(ctx(t), testModel, testDims*2, testSignature)
	if err == nil {
		t.Fatal("AssertModel with another width: want an error")
	}
	// This one is caught before the registry is consulted: the column itself cannot hold it.
	if !strings.Contains(err.Error(), "migrated for") {
		t.Errorf("error = %q, want it to say the table was migrated for another width", err)
	}
}

func TestWritingWithoutAssertModelFails(t *testing.T) {
	p := openStore(t, testOptions(requirePostgres(t)))
	wipe(t, p)
	if err := p.Migrate(ctx(t)); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	err := p.Upsert(ctx(t), []Row{testRow("acme/one", "role", unitVector(1))})
	if err == nil {
		t.Fatal("Upsert before AssertModel: want an error")
	}
	if !strings.Contains(err.Error(), "AssertModel") {
		t.Errorf("error = %q, want it to name AssertModel", err)
	}
}

func TestTwoDestinationsShareOneDatabase(t *testing.T) {
	first := newStore(t)

	// A second destination in the same database, at a different width and storage type.
	// It gets its own goose version table, so it must not conclude that version 1 was
	// already applied to it, and its index names must not collide with the first's.
	secondOpts := testOptions(requirePostgres(t))
	secondOpts.Table = testTable + "_two"
	secondOpts.Storage, secondOpts.Dims = StorageVector, testDims*2
	second := openStore(t, secondOpts)
	wipe(t, second)
	if err := second.Migrate(ctx(t)); err != nil {
		t.Fatalf("migrating the second destination: %v", err)
	}

	// Each binds independently in the shared registry, keyed by table name (D7).
	const secondSignature = "openai:model=other/embedder,dims=8"
	if err := second.AssertModel(ctx(t), "other/embedder", secondOpts.Dims, secondSignature); err != nil {
		t.Fatalf("binding the second destination: %v", err)
	}
	if err := first.AssertModel(ctx(t), testModel, testDims, testSignature); err != nil {
		t.Errorf("the first destination's binding was disturbed: %v", err)
	}

	// A write to one is invisible to the other, and each column keeps its own shape.
	seed(t, first, []Row{testRow("acme/one", "role", unitVector(1))})
	if got := countRows(t, second); got != 0 {
		t.Errorf("second destination holds %d rows after writing to the first, want 0", got)
	}
	if got := columnType(t, second, secondOpts.Table); got != "vector(8)" {
		t.Errorf("second destination's embedding column is %s, want vector(8)", got)
	}
}
