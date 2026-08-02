// Counting fakes for the pipeline tests: a generator that records what it was asked, an
// embedder count, and a destination that tallies writes.
//
// These counters are what the acceptance assertions are made against. "A second run performs
// zero LLM calls" is only worth asserting at the thing that would have been called; the
// pipeline's own statistics agreeing with themselves proves nothing.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/maxwellcudlitz/inget/internal/destination"
	"github.com/maxwellcudlitz/inget/internal/model"
)

// recordingGenerator is a deterministic generator that counts its calls and keeps the prompts
// it was given, so a test can assert what a view actually saw.
type recordingGenerator struct {
	mu      sync.Mutex
	calls   int
	prompts []string
}

func (g *recordingGenerator) Generate(_ context.Context, prompt string) (string, model.Usage, error) {
	g.mu.Lock()
	g.calls++
	g.prompts = append(g.prompts, prompt)
	g.mu.Unlock()

	sum := sha256.Sum256([]byte(prompt))
	text := "generated:" + hex.EncodeToString(sum[:16])
	return text, model.Usage{PromptTokens: len(prompt) / 4, CompletionTokens: len(text) / 4}, nil
}

func (g *recordingGenerator) Signature() string { return "recording:v1" }

// Calls reports how many generations have been requested.
func (g *recordingGenerator) Calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// promptFor returns the prompt sent for a named view, or "" when that view was not generated.
func (g *recordingGenerator) promptFor(view string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.prompts {
		if strings.HasPrefix(p, "Generate "+view+":") {
			return p
		}
	}
	return ""
}

func (g *recordingGenerator) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = 0
	g.prompts = nil
}

// testDest is a destination that counts what it was asked to write. It is mutex-guarded
// because the worker pool calls it from several goroutines at once.
type testDest struct {
	mu          sync.Mutex
	upsertCalls int
	totalRows   int
	deleted     []string
}

func (d *testDest) Migrate(context.Context) error { return nil }
func (d *testDest) Close() error                  { return nil }

func (d *testDest) AssertModel(context.Context, string, int, string) error { return nil }

func (d *testDest) Upsert(_ context.Context, rows []destination.Row) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.upsertCalls++
	d.totalRows += len(rows)
	return nil
}

func (d *testDest) BulkLoad(_ context.Context, rows []destination.Row) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.totalRows += len(rows)
	return nil
}

func (d *testDest) DeleteItem(_ context.Context, _, itemID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, itemID)
	return nil
}

func (d *testDest) Search(context.Context, destination.SearchQuery) ([]destination.SearchResult, error) {
	return nil, nil
}

func (d *testDest) rows() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.totalRows
}

func (d *testDest) upserts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.upsertCalls
}

func (d *testDest) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.upsertCalls, d.totalRows, d.deleted = 0, 0, nil
}

// Compile-time check that testDest implements destination.Destination.
var _ destination.Destination = (*testDest)(nil)
