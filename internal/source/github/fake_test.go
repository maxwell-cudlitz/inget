// A fake GitHub API for the connector tests: recorded shapes rather than recorded bytes.
//
// The fixtures are the API's documented shapes — a Link-paginated array of repositories, a search
// result wrapping one, a recursive tree of blob SHAs, a wrapped tarball — served from an httptest
// server. Building them from a description rather than pasting captured payloads keeps the tests
// readable and lets one fixture exercise pagination, filtering and extraction together.
//
// Every request path is recorded, so a test can assert not only what came back but that the
// connector did not spend a request it did not need.
package github

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxwell-cudlitz/inget/internal/ratelimit"
	"github.com/maxwell-cudlitz/inget/internal/source"
)

// fixture is one repository: its metadata, its files, and whether its tree overran the API's cap.
type fixture struct {
	meta          repo
	files         map[string]string
	treeTruncated bool
}

// fakeGitHub serves the endpoints this connector uses.
type fakeGitHub struct {
	t        *testing.T
	server   *httptest.Server
	pageSize int

	repos  map[string]*fixture // full name to fixture
	orgs   map[string][]string // org to full names, in listing order
	topics map[string][]string // topic to full names

	mu        sync.Mutex
	requests  []string
	failures  map[string]int  // path substring to remaining synthetic 500s
	throttles map[string]int  // path substring to remaining synthetic 403 + retry-after
	forbidden map[string]bool // path substring answering 403 with no retry-after
}

// newFakeGitHub starts a server with no repositories. Tests add them with addRepo.
//
// The retry backoff is shortened for the duration of the test, so the retry path is exercised
// without the suite sleeping through production delays.
func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	original := backoffBase
	backoffBase = time.Millisecond
	t.Cleanup(func() { backoffBase = original })

	f := &fakeGitHub{
		t:         t,
		pageSize:  100,
		repos:     map[string]*fixture{},
		orgs:      map[string][]string{},
		topics:    map[string][]string{},
		failures:  map[string]int{},
		throttles: map[string]int{},
		forbidden: map[string]bool{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

// addRepo registers a repository under an organisation.
func (f *fakeGitHub) addRepo(org string, meta repo, files map[string]string) *fixture {
	f.t.Helper()
	if meta.FullName == "" {
		meta.FullName = org + "/" + meta.Name
	}
	if meta.DefaultBranch == "" {
		meta.DefaultBranch = "main"
	}
	if meta.PushedAt.IsZero() {
		meta.PushedAt = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = meta.PushedAt
	}
	if meta.Visibility == "" {
		meta.Visibility = VisibilityPublic
	}
	fix := &fixture{meta: meta, files: files}
	f.repos[meta.FullName] = fix
	f.orgs[org] = append(f.orgs[org], meta.FullName)
	return fix
}

// addTopic makes an already-registered repository answer a topic search.
func (f *fakeGitHub) addTopic(topic, fullName string) {
	f.topics[topic] = append(f.topics[topic], fullName)
}

// failNext makes the next n requests whose path contains substr answer 500, so a test can prove
// the retry path recovers.
func (f *fakeGitHub) failNext(substr string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[substr] = n
}

// throttleNext makes the next n matching requests answer 403 with retry-after, which is how GitHub
// reports a secondary rate limit.
func (f *fakeGitHub) throttleNext(substr string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.throttles[substr] = n
}

// forbidAll makes every matching request answer 403 with no retry-after, which is a permission
// problem rather than a throttle and must not be retried.
func (f *fakeGitHub) forbidAll(substr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forbidden[substr] = true
}

// paths returns the request paths served so far, with query strings.
func (f *fakeGitHub) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// countPaths returns how many recorded requests contain substr.
func (f *fakeGitHub) countPaths(substr string) int {
	count := 0
	for _, p := range f.paths() {
		if strings.Contains(p, substr) {
			count++
		}
	}
	return count
}

// connector builds a connector pointed at the fake, with the given domain and limits blocks.
func (f *fakeGitHub) connector(d map[string]any, l map[string]any) *Connector {
	f.t.Helper()
	if l == nil {
		l = map[string]any{}
	}
	// The fake needs no pacing, and a real bucket would only make the tests slow.
	conn, err := New(source.Options{
		Name:             "github",
		Driver:           Driver,
		Token:            "test-token",
		APIURL:           f.server.URL,
		Domain:           d,
		Limits:           l,
		FragmentMaxBytes: 1 << 20,
	})
	if err != nil {
		f.t.Fatalf("New() = %v", err)
	}
	conn.client.limiter = ratelimit.Unlimited{}
	return conn
}

// handle routes one request.
func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	if status, header := f.injected(r.URL.Path, r.URL.RequestURI()); status != 0 {
		for key, value := range header {
			w.Header().Set(key, value)
		}
		w.WriteHeader(status)
		return
	}

	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		f.t.Errorf("Authorization = %q, want the bearer token", got)
	}
	path := strings.Trim(r.URL.Path, "/")
	segments := strings.Split(path, "/")

	switch {
	case len(segments) == 3 && segments[0] == "orgs" && segments[2] == "repos":
		f.serveOrgRepos(w, r, segments[1])
	case len(segments) == 2 && segments[0] == "search" && segments[1] == "repositories":
		f.serveSearch(w, r)
	case len(segments) >= 6 && segments[0] == "repos" && segments[3] == "git" && segments[4] == "trees":
		f.serveTree(w, segments[1]+"/"+segments[2])
	case len(segments) >= 5 && segments[0] == "repos" && segments[3] == "tarball":
		f.serveTarball(w, segments[1]+"/"+segments[2])
	case len(segments) == 3 && segments[0] == "repos":
		f.serveRepo(w, segments[1]+"/"+segments[2])
	default:
		http.NotFound(w, r)
	}
}

// injected records the request and returns the synthetic status and headers to answer with, or
// zero when the request should be served normally.
func (f *fakeGitHub) injected(path, uri string) (int, map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, uri)

	for substr, remaining := range f.failures {
		if remaining > 0 && strings.Contains(path, substr) {
			f.failures[substr] = remaining - 1
			return http.StatusInternalServerError, nil
		}
	}
	for substr, remaining := range f.throttles {
		if remaining > 0 && strings.Contains(path, substr) {
			f.throttles[substr] = remaining - 1
			// A secondary rate limit: 403 with retry-after. Zero seconds keeps the test
			// fast; the duration is the limiter's business, not the retry loop's.
			return http.StatusForbidden, map[string]string{"Retry-After": "0"}
		}
	}
	for substr := range f.forbidden {
		if strings.Contains(path, substr) {
			return http.StatusForbidden, nil
		}
	}
	return 0, nil
}

// serveOrgRepos serves a page of an organisation's repositories with a Link header.
func (f *fakeGitHub) serveOrgRepos(w http.ResponseWriter, r *http.Request, org string) {
	names := f.orgs[org]
	if names == nil {
		http.NotFound(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page == 0 {
		page = 1
	}
	start := (page - 1) * f.pageSize
	if start > len(names) {
		start = len(names)
	}
	end := min(start+f.pageSize, len(names))

	out := make([]repo, 0, end-start)
	for _, name := range names[start:end] {
		out = append(out, f.repos[name].meta)
	}
	if end < len(names) {
		next := *r.URL
		query := next.Query()
		query.Set("page", strconv.Itoa(page+1))
		next.RawQuery = query.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next", <%s%s>; rel="last"`,
			f.server.URL, next.RequestURI(), f.server.URL, next.RequestURI()))
	}
	f.writeJSON(w, out)
}

// serveSearch serves a topic search, whose repositories are wrapped in items.
func (f *fakeGitHub) serveSearch(w http.ResponseWriter, r *http.Request) {
	topic := strings.TrimPrefix(r.URL.Query().Get("q"), "topic:")
	out := make([]repo, 0)
	for _, name := range f.topics[topic] {
		out = append(out, f.repos[name].meta)
	}
	f.writeJSON(w, map[string]any{"total_count": len(out), "incomplete_results": false, "items": out})
}

// serveRepo serves one repository resource.
func (f *fakeGitHub) serveRepo(w http.ResponseWriter, fullName string) {
	fix, ok := f.repos[fullName]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		f.writeJSON(w, map[string]string{"message": "Not Found"})
		return
	}
	f.writeJSON(w, fix.meta)
}

// serveTree serves the recursive tree, one blob entry per file.
func (f *fakeGitHub) serveTree(w http.ResponseWriter, fullName string) {
	fix, ok := f.repos[fullName]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		f.writeJSON(w, map[string]string{"message": "Not Found"})
		return
	}
	entries := make([]treeEntry, 0, len(fix.files))
	for path, content := range fix.files {
		entries = append(entries, treeEntry{
			Path: path, Mode: "100644", Type: objectBlob,
			SHA: gitBlobSHA(content), Size: int64(len(content)),
		})
	}
	f.writeJSON(w, treeResponse{SHA: "treesha", Tree: entries, Truncated: fix.treeTruncated})
}

// serveTarball serves the archive, wrapped in the owner-repo-sha directory GitHub uses.
func (f *fakeGitHub) serveTarball(w http.ResponseWriter, fullName string) {
	fix, ok := f.repos[fullName]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		f.writeJSON(w, map[string]string{"message": "Not Found"})
		return
	}
	root := strings.ReplaceAll(fullName, "/", "-") + "-treesha"
	w.Header().Set("Content-Type", "application/x-gzip")
	if _, err := w.Write(tarGz(f.t, root, fix.files)); err != nil {
		f.t.Errorf("writing tarball: %v", err)
	}
}

func (f *fakeGitHub) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("encoding fixture: %v", err)
	}
}

// gitBlobSHA computes the real git blob hash of content, so a fixture's fingerprints behave the way
// the API's do: exact, content-derived, and stable across responses.
func gitBlobSHA(content string) string {
	h := sha1.New()
	// hash.Hash writers never return an error, which is why these are discarded.
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}
