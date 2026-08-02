// Fixtures for the pgvector integration suite.
//
// Every case here needs a real PostgreSQL with pgvector >= 0.8.0, because the behaviour
// under test is the extension's: halfvec casting, HNSW recall under a filter, and COPY into
// a staging table. There is no fake that would tell us anything about those. The suite is
// therefore skipped unless INGET_TEST_PG names a DSN, which is what CI does.
//
// The suite DROPs its own table before each case, so point the variable at a scratch
// database. It uses a table name distinct from the shipped inget_vectors, so a stray run
// against a real database cannot touch real vectors.
package destination

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
)

// envTestPostgres names the DSN of a throwaway PostgreSQL database with pgvector installed.
const envTestPostgres = "INGET_TEST_PG"

// Fixture shape. Four dimensions keeps the planted-neighbour geometry easy to read; the
// production width is 1024 and nothing here depends on it.
const (
	testTable     = "inget_test_vectors"
	testDims      = 4
	testModel     = "test/embedder"
	testSignature = "openai:model=test/embedder,dims=4"
	testDatatype  = "github/repo"
)

// testOptions returns options for the scratch table.
func testOptions(dsn string) Options {
	return Options{
		Driver:         DriverPGVector,
		DSN:            dsn,
		Table:          testTable,
		Storage:        StorageHalfvec,
		Dims:           testDims,
		M:              16,
		EFConstruction: 64,
		EFSearch:       100,
		Granularity:    GranularityItem,
		BatchSize:      100,
	}
}

// requirePostgres skips the calling test unless a scratch DSN is configured.
func requirePostgres(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(envTestPostgres)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the pgvector integration suite", envTestPostgres)
	}
	return dsn
}

// newStore drops the scratch table, migrates it, and returns a store bound to the fixture
// model. It returns the concrete type so cases can run statements the interface does not
// expose, which is how row counts are asserted.
func newStore(t *testing.T) *pgvectorStore {
	t.Helper()
	p := openStore(t, testOptions(requirePostgres(t)))
	wipe(t, p)
	if err := p.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := p.AssertModel(t.Context(), testModel, testDims, testSignature); err != nil {
		t.Fatalf("AssertModel: %v", err)
	}
	return p
}

// openStore opens a destination and closes it when the test ends.
func openStore(t *testing.T, opts Options) *pgvectorStore {
	t.Helper()
	opened, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	p, ok := opened.(*pgvectorStore)
	if !ok {
		t.Fatalf("Open returned %T, want *pgvectorStore", opened)
	}
	return p
}

// wipe removes the scratch table, its goose version table and its registry row, so a case
// starts from nothing regardless of what the previous one did.
func wipe(t *testing.T, p *pgvectorStore) {
	t.Helper()
	ctx := t.Context()
	statements := []string{
		"DROP TABLE IF EXISTS " + p.opts.Table,
		"DROP TABLE IF EXISTS " + p.opts.Table + "_goose_version",
		fmt.Sprintf("DELETE FROM %s WHERE table_name = '%s'", registryTable, p.opts.Table),
	}
	for _, statement := range statements {
		// The registry may not exist before the first migration, which is not a failure.
		if _, err := p.pool.Exec(ctx, statement); err != nil && !isMissingTable(err) {
			t.Fatalf("wiping with %q: %v", statement, err)
		}
	}
}

// isMissingTable reports whether err is PostgreSQL's undefined_table.
func isMissingTable(err error) bool {
	return err != nil && containsCode(err.Error(), "42P01")
}

// containsCode reports whether a driver error message carries a SQLSTATE.
func containsCode(message, code string) bool {
	for i := 0; i+len(code) <= len(message); i++ {
		if message[i:i+len(code)] == code {
			return true
		}
	}
	return false
}

// countRows returns the number of rows in the scratch table.
func countRows(t *testing.T, p *pgvectorStore) int {
	t.Helper()
	var n int
	if err := p.pool.QueryRow(t.Context(), "SELECT count(*) FROM "+p.opts.Table).Scan(&n); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	return n
}

// columnType returns the rendered type of a table's embedding column, which is how the
// schema cases check what the template produced.
func columnType(t *testing.T, p *pgvectorStore, table string) string {
	t.Helper()
	var rendered string
	err := p.pool.QueryRow(t.Context(), `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = $1::regclass AND a.attname = 'embedding'`, table).Scan(&rendered)
	if err != nil {
		t.Fatalf("reading the embedding column type of %s: %v", table, err)
	}
	return rendered
}

// itemName renders a fixture item identifier, zero-padded so identifiers sort as they read.
func itemName(n int) string {
	digits := []byte("0000")
	for i := len(digits) - 1; i >= 0 && n > 0; i-- {
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return "acme/repo-" + string(digits)
}

// testRow builds a row for one item and view with the given vector.
func testRow(itemID, view string, vector []float32) Row {
	return Row{
		Datatype:  testDatatype,
		ItemID:    itemID,
		ViewName:  view,
		Text:      itemID + " " + view,
		Embedding: vector,
		Model:     testModel,
		Dims:      testDims,
		Signature: testSignature,
	}
}

// unitVector returns a deterministic unit vector derived from n, so that two different n
// are measurably apart and every vector is the same length.
func unitVector(n int) []float32 {
	v := make([]float32, testDims)
	for i := range v {
		v[i] = float32(math.Sin(float64(n*testDims+i) * 0.7))
	}
	return normalize(v)
}

// normalize scales v to unit length. Cosine distance ignores magnitude, but keeping the
// fixtures normalized makes a score readable as a similarity.
func normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		v[0] = 1
		return v
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

// seed writes rows through Upsert and fails the test if it errors.
func seed(t *testing.T, p *pgvectorStore, rows []Row) {
	t.Helper()
	if err := p.Upsert(t.Context(), rows); err != nil {
		t.Fatalf("Upsert %d rows: %v", len(rows), err)
	}
}

// ctx is a short alias for the test context, used where a line would otherwise wrap.
func ctx(t *testing.T) context.Context { return t.Context() }
