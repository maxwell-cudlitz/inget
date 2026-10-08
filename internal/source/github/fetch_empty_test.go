// Regression coverage for repositories whose metadata names a default branch even
// though GitHub's tree endpoint reports no Git contents. Only the specific empty
// repository conflict is benign; other failures retain their normal error behavior.
package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/source"
)

func TestFetchEmptyRepositoryTreeConflict(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		empty   bool
	}{
		{"empty with default branch", http.StatusConflict, "Git Repository is empty.", true},
		{"other conflict", http.StatusConflict, "Repository access blocked.", false},
		{"validation error", http.StatusUnprocessableEntity, "Git Repository is empty.", false},
		{"permission error", http.StatusForbidden, "Git Repository is empty.", false},
		{"conflict without message", http.StatusConflict, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var treeCalls, archiveCalls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var body any
				switch r.URL.Path {
				case "/repos/acme/fresh":
					body = map[string]string{
						"full_name": "acme/fresh", "name": "fresh", "default_branch": "main",
						"description": "new repository", "pushed_at": "2026-07-30T12:00:00Z",
					}
				case "/repos/acme/fresh/git/trees/main":
					treeCalls.Add(1)
					w.WriteHeader(tt.status)
					body = map[string]string{"message": tt.message}
				default:
					archiveCalls.Add(1)
					w.WriteHeader(http.StatusNotFound)
					body = map[string]string{"message": "Unexpected download"}
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Errorf("encoding API response: %v", err)
				}
			}))
			defer server.Close()
			conn, err := New(source.Options{
				Name: "github", Driver: Driver, APIURL: server.URL,
				Domain: orgDomain("acme"), FragmentMaxBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := conn.Fetch(t.Context(), Datatype, source.Ref{ID: "acme/fresh"})
			if (err == nil) != tt.empty {
				t.Fatalf("empty=%v; result=%+v, err=%v", tt.empty, result, err)
			}
			if treeCalls.Load() != 1 || archiveCalls.Load() != 0 {
				t.Fatalf("tree calls=%d, archive calls=%d", treeCalls.Load(), archiveCalls.Load())
			}
			if !tt.empty {
				return
			}
			if result.Item.ID != "acme/fresh" || result.Item.Fingerprint != "2026-07-30T12:00:00Z" ||
				result.Item.Metadata["default_branch"] != "main" || result.Item.Metadata["description"] != "new repository" {
				t.Fatalf("empty repository metadata/fingerprint lost: %+v", result.Item)
			}
			if len(result.Fragments) != 0 || !hasWarningContaining(result.Warnings, "repository is empty") {
				t.Fatalf("empty repository outcome: %+v", result)
			}
		})
	}
}
