// Coverage for the configuration bridge and for the rendered migration, both of which are
// checkable without a database: one is a field mapping, the other is text.
package destination

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/config"
)

// fixtureConfig loads testdata/config.yaml with the destination DSN present.
func fixtureConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("INGET_TEST_DESTINATION_DSN", "postgres://localhost/inget")
	cfg, err := config.Load("testdata/config.yaml")
	if err != nil {
		t.Fatalf("loading the fixture configuration: %v", err)
	}
	return cfg
}

func TestFromConfigMapsTheDestinationBlock(t *testing.T) {
	opts, err := FromConfig(fixtureConfig(t), "vectors")
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	want := Options{
		Driver:         DriverPGVector,
		DSN:            "postgres://localhost/inget",
		Table:          "test_vectors",
		Storage:        StorageHalfvec,
		Dims:           4, // truncate_dims, not the native 8
		M:              16,
		EFConstruction: 64,
		EFSearch:       40,
		Granularity:    GranularityItem,
		BatchSize:      10,
	}
	if opts != want {
		t.Errorf("FromConfig = %+v, want %+v", opts, want)
	}
}

func TestFromConfigRejectsAnUnknownName(t *testing.T) {
	_, err := FromConfig(fixtureConfig(t), "elsewhere")
	if err == nil {
		t.Fatal("FromConfig for an unconfigured destination: want an error")
	}
	if !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("error = %q, want it to name the destination", err)
	}
}

func TestFromConfigReportsAMissingDSN(t *testing.T) {
	// Loading without the variable set is allowed; needing it is what fails.
	cfg, err := config.Load("testdata/config.yaml")
	if err != nil {
		t.Fatalf("loading the fixture configuration: %v", err)
	}
	_, err = FromConfig(cfg, "vectors")
	if err == nil {
		t.Fatal("FromConfig with no DSN in the environment: want an error")
	}
	if !strings.Contains(err.Error(), "INGET_TEST_DESTINATION_DSN") {
		t.Errorf("error = %q, want it to name the environment variable", err)
	}
}

// renderedSQL renders the migration set for opts and returns it as one string.
func renderedSQL(t *testing.T, opts Options) string {
	t.Helper()
	normalized, err := opts.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	p := &pgvectorStore{opts: normalized}
	fsys, err := p.renderMigrations()
	if err != nil {
		t.Fatalf("renderMigrations: %v", err)
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("rendered set has no .sql files: names=%v err=%v", names, err)
	}
	var b strings.Builder
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		b.Write(data)
	}
	return b.String()
}

func TestRenderedMigrationCarriesTheConfiguredShape(t *testing.T) {
	sql := renderedSQL(t, fullOptions())
	for _, want := range []string{
		"CREATE TABLE inget_vectors (",
		"embedding    halfvec(1024) NOT NULL",
		"USING hnsw (embedding halfvec_cosine_ops) WITH (m = 24, ef_construction = 200)",
		"CREATE INDEX inget_vectors_facet_idx",
		"CREATE INDEX inget_vectors_hnsw_idx",
		"CREATE TABLE IF NOT EXISTS " + registryTable,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("rendered SQL is missing %q", want)
		}
	}
	// An unrendered template action would reach the server as a syntax error.
	if strings.Contains(sql, "{{") {
		t.Errorf("rendered SQL still contains a template action:\n%s", sql)
	}
}

func TestRenderedMigrationFollowsStorageAndTable(t *testing.T) {
	opts := fullOptions()
	opts.Storage, opts.Dims, opts.Table = StorageVector, 768, "other_vectors"

	sql := renderedSQL(t, opts)
	for _, want := range []string{
		"CREATE TABLE other_vectors (",
		"embedding    vector(768) NOT NULL",
		"vector_cosine_ops",
		"CREATE INDEX other_vectors_hnsw_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("rendered SQL is missing %q", want)
		}
	}
	// Index names are derived from the table, so two destinations in one database do not
	// collide. The check names the statement rather than the substring, because the
	// template's own header comment mentions the default table by name.
	for _, unwanted := range []string{"CREATE TABLE inget_vectors", "CREATE INDEX inget_vectors_"} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("rendered SQL still emits %q for a destination named %s", unwanted, opts.Table)
		}
	}
}

func TestGooseAnnotationsSurviveRendering(t *testing.T) {
	sql := renderedSQL(t, fullOptions())
	for _, want := range []string{"-- +goose Up", "-- +goose Down"} {
		if !strings.Contains(sql, want) {
			t.Errorf("rendered SQL is missing the %q annotation, so goose would reject it", want)
		}
	}
}
