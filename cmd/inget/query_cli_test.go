// End-to-end CLI cases prove query and query-check work with a true SELECT-only login,
// including the wrapper's fixed argument shape and absence of unused model credentials.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/destination"
)

func TestQueryCommandsWithReadOnlyRole(t *testing.T) {
	f := newQueryCLIFixture(t)
	var calls atomic.Int64
	const text = "--synthetic user text"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 || request.Input[0] != text {
			t.Errorf("embedding input=%v error=%v", request.Input, err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0,0,0]}]}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	env := []string{"INGET_MODELS__EMBEDDER__BASE_URL=" + server.URL + "/v1"}
	output, err := f.command(t, env, "query-check", "--datatype", "github/repo", "--json")
	if err != nil {
		t.Fatalf("query-check with no model secrets: %s: %v", output, err)
	}
	var report []queryCheckResult
	if err := json.Unmarshal(output, &report); err != nil || len(report) != 1 || report[0].Table != f.table || report[0].Dims != 4 {
		t.Fatalf("query-check output=%s error=%v", output, err)
	}
	if calls.Load() != 0 {
		t.Fatal("preflight contacted the model")
	}
	env = append(env, "INGET_QUERY_TEST_EMBEDDER_KEY=synthetic-key")
	args := []string{"query", "--datatype", "github/repo", "--limit", "5", "--json", "--rerank=false", "--", text}
	output, err = f.command(t, env, args...)
	if err != nil || strings.TrimSpace(string(output)) != "[]" {
		t.Fatalf("empty query output=%s error=%v", output, err)
	}
	row := destination.Row{Datatype: "github/repo", ItemID: "synthetic/repo", ViewName: "role", Text: "Synthetic fixture",
		Embedding: []float32{1, 0, 0, 0}, Model: "query-test", Dims: 4, Signature: "openai:dimensions=4,model=query-test"}
	if err := f.writer.Upsert(t.Context(), []destination.Row{row}); err != nil {
		t.Fatal(err)
	}
	output, err = f.command(t, env, args...)
	if err != nil {
		t.Fatalf("seeded query output=%s error=%v", output, err)
	}
	var hits []hit
	if err := json.Unmarshal(output, &hits); err != nil || len(hits) != 1 || hits[0].ItemID != row.ItemID {
		t.Fatalf("seeded query output=%s error=%v", output, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("embedding calls=%d, want 2", calls.Load())
	}
	// A missing binding fails before embedding, and neither command recreates it.
	f.exec(t, "DELETE FROM inget_model_registry WHERE table_name='"+f.table+"'")
	for _, command := range [][]string{{"query-check", "--json"}, args} {
		output, err := f.command(t, env, command...)
		if err == nil || !strings.Contains(string(output), "no model binding") {
			t.Fatalf("missing binding output=%s error=%v", output, err)
		}
	}
	var bindings int
	if err := f.owner.QueryRow(t.Context(), "SELECT count(*) FROM inget_model_registry WHERE table_name=$1", f.table).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 || calls.Load() != 2 {
		t.Fatalf("missing binding check wrote or embedded: bindings=%d calls=%d", bindings, calls.Load())
	}
}

func TestQueryCheckRejectsInvalidConfigScopeAndReadAccess(t *testing.T) {
	f := newQueryCLIFixture(t)
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"datatype", []string{"--datatype", "missing"}, "missing"},
		{"destination", []string{"--destination", "missing"}, "does not write"},
		{"view", []string{"--view", "missing"}, "no view"},
		{"arguments", []string{"unexpected"}, "unknown command"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output, err := f.command(t, nil, append([]string{"query-check"}, tt.args...)...)
			if err == nil || !strings.Contains(string(output), tt.want) {
				t.Fatalf("output=%s error=%v want=%q", output, err, tt.want)
			}
		})
	}
	f.exec(t, "REVOKE SELECT ON "+f.table+" FROM "+f.reader)
	output, err := f.command(t, nil, "query-check", "--json")
	if err == nil || !strings.Contains(string(output), "permission denied") {
		t.Fatalf("unreadable destination output=%s error=%v", output, err)
	}
	f.exec(t, "GRANT SELECT ON "+f.table+" TO "+f.reader)
	raw, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.configPath, append(raw, []byte("\nunknown_query_setting: true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = f.command(t, nil, "query-check")
	if err == nil || !strings.Contains(string(output), "unknown_query_setting") {
		t.Fatalf("unknown config key output=%s error=%v", output, err)
	}
}

func TestQueryCheckSharedDestinationAndLaterInvalidView(t *testing.T) {
	f := newQueryCLIFixture(t)
	raw, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	second := `
  - name: github/other
    source: github
    enricher: llm
    fragment_enricher: {enabled: false}
    compose: {order: tier, max_chars: 3000}
    destinations: [vectors]
    views:
      - name: stack
        prompt: /nonexistent-query-stack.tmpl
        depends_on: ["**"]
`
	if err := os.WriteFile(f.configPath, append(raw, []byte(second)...), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := f.command(t, nil, "query-check", "--json")
	var report []queryCheckResult
	if err != nil || json.Unmarshal(output, &report) != nil || len(report) != 2 || report[0].Table != report[1].Table {
		t.Fatalf("shared-destination output=%s error=%v", output, err)
	}
	for _, args := range [][]string{
		{"query-check", "--view", "role", "--json"},
		{"query", "--view", "role", "--rerank=false", "--", "synthetic"},
	} {
		output, err := f.command(t, []string{"INGET_QUERY_TEST_EMBEDDER_KEY=synthetic-key"}, args...)
		if err == nil || !strings.Contains(string(output), "datatype github/other has no view") {
			t.Fatalf("later invalid view output=%s error=%v", output, err)
		}
	}
}
