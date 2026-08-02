// Shared fixtures for the harness tests.
//
// Two choices shape them. State is a real migrated sqlite store, because loadCorpus is the
// integration point being tested — ViewedItems, Item and ViewState against a real schema — and
// a stub would only prove the stub agrees with itself. The embedder is lexical rather than the
// hash-derived FakeEmbedder from internal/model: hash vectors have no similarity structure, so
// they can prove the mechanics but not that a degraded corpus scores worse than a healthy one,
// which is what the gate has to detect.
package eval

import (
	"context"
	"hash/fnv"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

const (
	testDatatype = "github/repo"
	testSource   = "github"
)

// testViews is the configured view list every fixture item is written against.
var testViews = []string{"role", "stack", "operations"}

// fixture is one seeded item: its metadata and the text of each of its views.
type fixture struct {
	id       string
	metadata map[string]string
	views    map[string]string
}

// healthyCorpus is four items from clearly different domains. Each item's views share a few
// of that item's own words and add their own, which is what a working prompt set produces:
// views of one repository are about the same repository without being restatements of one
// another.
func healthyCorpus() []fixture {
	return []fixture{
		{
			id:       "acme/terraform-vpc",
			metadata: map[string]string{"name": "terraform-vpc", "description": "Reusable terraform modules for vpc networking", "topics": "terraform,networking", "url": "https://github.com/acme/terraform-vpc"},
			views: map[string]string{
				"role":       "terraform modules that define vpc networking for acme accounts",
				"stack":      "terraform hcl aws provider vpc subnet route table modules",
				"operations": "terraform plan and apply pipelines validate vpc networking changes",
			},
		},
		{
			id:       "acme/etl-pipelines",
			metadata: map[string]string{"name": "etl-pipelines", "description": "Python batch pipelines loading warehouse tables", "topics": "python,etl", "updated_at": "2026-08-01T00:00:00Z"},
			views: map[string]string{
				"role":       "python batch pipelines that load warehouse tables nightly",
				"stack":      "python pandas airflow warehouse parquet batch pipelines",
				"operations": "airflow schedules retries and backfills for warehouse pipelines",
			},
		},
		{
			id:       "acme/storefront",
			metadata: map[string]string{"name": "storefront", "description": "React storefront rendering the product catalogue", "topics": "react,frontend"},
			views: map[string]string{
				"role":       "react storefront rendering the product catalogue for shoppers",
				"stack":      "react typescript vite css product catalogue components",
				"operations": "storefront bundles deploy to the cdn behind the catalogue api",
			},
		},
		{
			id:       "acme/inget",
			metadata: map[string]string{"name": "inget", "description": "Go command line tool embedding repository views", "topics": "go,cli,vectors"},
			views: map[string]string{
				"role":       "go command line tool embedding repository views into vectors",
				"stack":      "go cobra pgvector sqlite embedding vectors cli",
				"operations": "cron invocations of the cli embedding views into pgvector",
			},
		},
	}
}

// degradedCorpus is the same items with a prompt that has stopped saying anything specific:
// every view of every item is the same sentence. It is the failure the gate exists to catch,
// because nothing else in the pipeline notices — the text is non-empty, it embeds, and it
// upserts.
func degradedCorpus() []fixture {
	const generic = "This repository contains source code and configuration."
	items := healthyCorpus()
	for i := range items {
		for name := range items[i].views {
			items[i].views[name] = generic
		}
	}
	return items
}

// openStore returns a migrated sqlite state store in a temporary directory.
func openStore(t *testing.T) state.Store {
	t.Helper()
	store, err := state.Open(t.Context(), state.Options{
		Driver: state.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatalf("migrating state: %v", err)
	}
	return store
}

// seed writes the fixtures as a run would have left them: one item row plus one view row per
// non-empty view.
func seed(t *testing.T, store state.Store, items []fixture) {
	t.Helper()
	ctx := t.Context()
	for _, f := range items {
		it := state.Item{ID: f.id, Source: testSource, Fingerprint: "fp-" + f.id, Metadata: f.metadata}
		if err := store.PutItem(ctx, testDatatype, it); err != nil {
			t.Fatalf("seeding item %s: %v", f.id, err)
		}
		for name, text := range f.views {
			v := state.ViewState{
				Name:         name,
				InputHash:    "sha256:" + f.id + name,
				Text:         text,
				EmbeddedHash: "sha256:embedded",
				Signature:    "sha256:sig",
			}
			if err := store.PutViewState(ctx, testDatatype, f.id, v); err != nil {
				t.Fatalf("seeding view %s of %s: %v", name, f.id, err)
			}
		}
	}
}

// options builds an Options with the documented thresholds, which is what the shipped config
// produces after normalization.
func options() Options {
	return Options{
		Datatype:   testDatatype,
		Views:      testViews,
		SampleSize: config.DefaultEvalSampleSize,
		Thresholds: config.Thresholds{
			SelfRetrieval:       config.DefaultSelfRetrieval,
			Distinctiveness:     config.DefaultDistinctiveness,
			ViewCoverage:        config.DefaultViewCoverage,
			ViewDistinctiveness: config.DefaultViewDistinctiveness,
			MetadataTop3:        config.DefaultMetadataTop3,
		},
	}
}

// lexicalEmbedder embeds text as a normalized bag of hashed words. It is deterministic, needs
// no network, and — unlike a hash of the whole string — gives texts that share vocabulary
// vectors that are close, so retrieval metrics computed over it mean something.
type lexicalEmbedder struct {
	dims  int
	calls int
}

// Embed implements model.Embedder.
func (e *lexicalEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vecs[i] = e.vector(text)
	}
	return vecs, nil
}

func (e *lexicalEmbedder) Model() string     { return "lexical-test" }
func (e *lexicalEmbedder) Dims() int         { return e.dims }
func (e *lexicalEmbedder) Signature() string { return "test:lexical" }

// vector accumulates one dimension per hashed word and normalizes to unit length.
func (e *lexicalEmbedder) vector(text string) []float32 {
	vec := make([]float32, e.dims)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, word := range words {
		h := fnv.New32a()
		_, _ = h.Write([]byte(word))
		vec[int(h.Sum32())%e.dims]++
	}

	var sum float64
	for _, x := range vec {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return vec
	}
	norm := float32(math.Sqrt(sum))
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}
