// The real grouped CLI must keep the SELECT-only boundary and embed once for
// multiple declared views across datatypes, including identical source item IDs.
package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/destination"
)

func TestGroupedQueryCLIUsesOneEmbeddingAcrossViewsAndDatatypes(t *testing.T) {
	f := newQueryCLIFixture(t)
	addGroupedQueryScope(t, f)
	var embeddings atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embeddings.Add(1)
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 || request.Input[0] != "find implementation" {
			t.Errorf("incorrect embedding request: %+v %v", request, err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0,0,0]}]}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	rows := []destination.Row{
		groupedFixtureRow("github/repo", "same", "role", 1, "repository role"),
		groupedFixtureRow("github/repo", "same", "workflow", .8, "repository workflow"),
		groupedFixtureRow("calendar/event", "same", "role", .9, "event role"),
		groupedFixtureRow("calendar/event", "same", "workflow", .7, "event workflow"),
		groupedFixtureRow("github/repo", "second", "role", .6, "another item"),
	}
	if err := f.writer.Upsert(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	env := []string{"INGET_MODELS__EMBEDDER__BASE_URL=" + server.URL + "/v1", "INGET_QUERY_TEST_EMBEDDER_KEY=synthetic-key"}
	args := []string{"query", "--views", "role,workflow", "--group-by", "item", "--candidate-limit", "16", "--limit", "3", "--max-output-bytes", "102400", "--json", "--rerank=false", "--", "find implementation"}
	output, err := f.command(t, env, args...)
	if err != nil {
		t.Fatalf("grouped read-only CLI failed: %s: %v", output, err)
	}
	var report groupedQueryOutput
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("grouped output=%s err=%v", output, err)
	}
	if embeddings.Load() != 1 || report.CandidateCount != 5 || report.CandidateLimit != 16 || len(report.Results) != 3 || report.ReturnedItems != 3 {
		t.Fatalf("one-embedding/global-budget contract failed: embeddings=%d report=%+v", embeddings.Load(), report)
	}
	seen := map[string]bool{}
	for _, item := range report.Results {
		key := item.Datatype + ":" + item.ItemID
		if seen[key] {
			t.Fatal("duplicate item escaped grouping")
		}
		seen[key] = true
		if item.ItemID == "same" && (len(item.Evidence) != 2 || len(item.MatchedViews) != 2) {
			t.Fatal("multi-view evidence was dropped")
		}
	}
	if !seen["github/repo:same"] || !seen["calendar/event:same"] {
		t.Fatal("shared item IDs collided across datatypes")
	}
	// Invalid views and impossible budgets fail before embedding or source access.
	for _, invalid := range [][]string{
		{"query", "--views", "role,missing", "--rerank=false", "--", "find implementation"},
		{"query", "--views", "role,workflow", "--group-by", "item", "--candidate-limit", "1", "--limit", "1", "--rerank=false", "--", "find implementation"},
	} {
		if output, err := f.command(t, env, invalid...); err == nil {
			t.Fatalf("invalid new mode succeeded: %s", output)
		}
	}
	if embeddings.Load() != 1 {
		t.Fatal("an invalid view/budget spent an embedding call")
	}
}

func groupedFixtureRow(datatype, id, view string, score float64, text string) destination.Row {
	return destination.Row{Datatype: datatype, ItemID: id, ViewName: view, Text: text,
		Embedding: []float32{float32(score), float32(math.Sqrt(1 - score*score)), 0, 0},
		Model:     "query-test", Dims: 4, Signature: "openai:dimensions=4,model=query-test"}
}

func addGroupedQueryScope(t *testing.T, f *queryCLIFixture) {
	t.Helper()
	raw, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw) + `
      - name: workflow
        prompt: /nonexistent-query-workflow.tmpl
        depends_on: ["**"]
  - name: calendar/event
    source: github
    enricher: llm
    fragment_enricher: {enabled: false}
    compose: {order: tier, max_chars: 3000}
    destinations: [vectors]
    views:
      - name: role
        prompt: /nonexistent-event-role.tmpl
        depends_on: ["**"]
      - name: workflow
        prompt: /nonexistent-event-workflow.tmpl
        depends_on: ["**"]
`
	if err := os.WriteFile(f.configPath, []byte(strings.TrimSpace(text)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
