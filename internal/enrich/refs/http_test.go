// http resolver cases: templating, the allowlist, auth, and what counts as a failure.
//
// The allowlist case is the security-relevant one. An endpoint's response is untrusted input
// that ends up in front of a model, so a field the configuration did not ask for must not reach
// a prompt however convenient it looks.
package refs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// jsonServer serves one JSON body and records the last request it saw.
func jsonServer(t *testing.T, status int, body string) (*httptest.Server, *http.Request) {
	t.Helper()
	var last http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

// httpRef opens an http resolver against a test server.
func httpRef(t *testing.T, srv *httptest.Server, token string) Resolver {
	t.Helper()
	resolver, err := Open(Options{
		Name:     "ticket",
		Kind:     KindHTTP,
		Endpoint: srv.URL + "/api/tickets/{key}",
		Token:    token,
		Client:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return resolver
}

func TestHTTPResolverAppliesTheFieldAllowlist(t *testing.T) {
	srv, last := jsonServer(t, http.StatusOK,
		`{"number":7,"title":"Rotate the signing key","state":"open","internal_notes":"do not index","owner":{"id":3}}`)
	resolver := httpRef(t, srv, "tok-123")

	rec, err := resolver.Resolve(t.Context(), "TICKET-7", []string{"number", "title", "state", "owner"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.Fields["title"] != "Rotate the signing key" {
		t.Errorf("title = %q", rec.Fields["title"])
	}
	if rec.Fields["number"] != "7" {
		t.Errorf("number = %q, want the JSON number rendered without a decimal point", rec.Fields["number"])
	}
	if _, present := rec.Fields["internal_notes"]; present {
		t.Error("a field outside the allowlist reached the record")
	}
	if _, present := rec.Fields["owner"]; present {
		t.Error("a nested object was kept; only scalars belong in front of a prompt")
	}
	if got := last.Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Errorf("Authorization = %q", got)
	}
	if last.URL.Path != "/api/tickets/TICKET-7" {
		t.Errorf("path = %q, want the key substituted", last.URL.Path)
	}
}

func TestHTTPResolverEscapesTheKeyIntoThePath(t *testing.T) {
	srv, last := jsonServer(t, http.StatusOK, `{"title":"x"}`)
	resolver := httpRef(t, srv, "")

	// The key comes from third-party content, so it must not be able to add path segments or
	// query parameters to a URL the operator wrote. The wire form is what matters, so the
	// assertion is on the escaped path: URL.Path is the decoded value the server saw.
	if _, err := resolver.Resolve(t.Context(), "../admin?all=1", []string{"title"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := last.URL.EscapedPath(); got != "/api/tickets/..%2Fadmin%3Fall=1" {
		t.Errorf("escaped path = %q, want the key escaped", got)
	}
	if last.URL.RawQuery != "" {
		t.Errorf("query = %q, want none", last.URL.RawQuery)
	}
}

func TestHTTPResolverTreatsNotFoundAsUnresolved(t *testing.T) {
	srv, _ := jsonServer(t, http.StatusNotFound, `{"error":"nope"}`)
	resolver := httpRef(t, srv, "")

	rec, err := resolver.Resolve(t.Context(), "TICKET-404", []string{"title"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.resolved() {
		t.Errorf("record = %+v, want unresolved", rec)
	}
}

func TestHTTPResolverFailsOnServerErrorAndBadJSON(t *testing.T) {
	broken, _ := jsonServer(t, http.StatusInternalServerError, `{}`)
	if _, err := httpRef(t, broken, "").Resolve(t.Context(), "TICKET-1", []string{"title"}); err == nil {
		t.Error("expected an error for HTTP 500")
	}
	garbage, _ := jsonServer(t, http.StatusOK, `not json`)
	if _, err := httpRef(t, garbage, "").Resolve(t.Context(), "TICKET-1", []string{"title"}); err == nil {
		t.Error("expected an error for a malformed response")
	}
}

func TestHTTPResolverRejectsBadEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
	}{
		{name: "no key placeholder", endpoint: "https://example.test/api/tickets"},
		{name: "not absolute", endpoint: "/api/tickets/{key}"},
		{name: "unset variable", endpoint: "${INGET_TEST_UNSET_BASE}/api/{key}"},
		{name: "empty", endpoint: ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(Options{Name: "ticket", Kind: KindHTTP, Endpoint: tt.endpoint})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestHTTPResolverExpandsTheEndpointFromTheEnvironment(t *testing.T) {
	srv, _ := jsonServer(t, http.StatusOK, `{"title":"x"}`)
	t.Setenv("INGET_TEST_TICKETS_BASE", srv.URL)

	resolver, err := Open(Options{
		Name:     "ticket",
		Kind:     KindHTTP,
		Endpoint: "${INGET_TEST_TICKETS_BASE}/api/tickets/{key}",
		Client:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec, err := resolver.Resolve(t.Context(), "TICKET-1", []string{"title"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.Fields["title"] != "x" {
		t.Errorf("fields = %v", rec.Fields)
	}
}
