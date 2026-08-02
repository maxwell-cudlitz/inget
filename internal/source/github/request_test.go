// Tests for the request layer: what gets retried, what does not, and how a Link header is read.
//
// The distinction that matters is between a throttle and a permission problem. GitHub reports its
// secondary rate limit as 403 with retry-after and its "you cannot see this" as 403 without one, so
// classifying by status alone either retries a failure forever or gives up on a recoverable one.
package github

import (
	"errors"
	"strings"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/source"
)

// A secondary rate limit is recoverable: the limiter records how long to wait and the request is
// re-issued.
func TestSecondaryRateLimitIsRetried(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, serviceFiles())
	f.throttleNext("/repos/acme/svc/git/trees/", 2)

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/svc")
	if len(result.Fragments) == 0 {
		t.Error("no fragments after two throttled tree requests, so the retry did not recover")
	}
	if got := f.countPaths("/repos/acme/svc/git/trees/"); got != 3 {
		t.Errorf("tree requested %d times, want 3: two throttled and one served", got)
	}
}

// A 403 with no retry-after is a permission problem. Retrying it spends quota to fail again, so it
// must surface on the first attempt.
func TestForbiddenIsNotRetried(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, serviceFiles())
	f.forbidAll("/repos/acme/svc/git/trees/")

	_, err := f.connector(orgDomain("acme"), nil).Fetch(t.Context(), Datatype, source.Ref{ID: "acme/svc"})
	if err == nil {
		t.Fatal("Fetch() through a 403 = nil error, want failure")
	}
	if got := f.countPaths("/repos/acme/svc/git/trees/"); got != 1 {
		t.Errorf("tree requested %d times, want 1: a permission failure is not transient", got)
	}
}

// Retries are bounded. A server that is persistently broken must fail the request rather than loop.
func TestPersistentServerErrorsGiveUp(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, serviceFiles())
	f.failNext("/repos/acme/svc/git/trees/", maxAttempts+2)

	_, err := f.connector(orgDomain("acme"), nil).Fetch(t.Context(), Datatype, source.Ref{ID: "acme/svc"})
	if err == nil {
		t.Fatal("Fetch() through persistent 500s = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "attempts") {
		t.Errorf("error %q should say the attempts were exhausted", err)
	}
	if got := f.countPaths("/repos/acme/svc/git/trees/"); got != maxAttempts {
		t.Errorf("tree requested %d times, want the %d-attempt bound", got, maxAttempts)
	}
}

// A missing repository is a distinguishable condition, because callers treat it as a warning rather
// than a failure.
func TestNotFoundIsIdentifiable(t *testing.T) {
	f := newFakeGitHub(t)
	_, err := f.connector(orgDomain("acme"), nil).Fetch(t.Context(), Datatype, source.Ref{ID: "acme/absent"})
	if !errors.Is(err, errNotFound) {
		t.Errorf("Fetch() on a missing repository = %v, want errNotFound", err)
	}
}

// The API's own message is worth surfacing: "Bad credentials" is a far more useful failure than
// "github returned 401".
func TestStatusErrorsCarryTheAPIMessage(t *testing.T) {
	f := newFakeGitHub(t)
	_, err := f.connector(orgDomain("acme"), nil).Fetch(t.Context(), Datatype, source.Ref{ID: "acme/absent"})
	if err == nil || !strings.Contains(err.Error(), "acme/absent") {
		t.Errorf("error %q should name what was being read", err)
	}
}

func TestNextLink(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"absent", "", ""},
		{
			name:   "next and last",
			header: `<https://api.github.com/orgs/a/repos?page=2>; rel="next", <https://api.github.com/orgs/a/repos?page=9>; rel="last"`,
			want:   "https://api.github.com/orgs/a/repos?page=2",
		},
		{
			name:   "prev and first only",
			header: `<https://api.github.com/orgs/a/repos?page=1>; rel="prev", <https://api.github.com/orgs/a/repos?page=1>; rel="first"`,
			want:   "",
		},
		{
			name:   "next is not the first entry",
			header: `<https://x/1>; rel="prev", <https://x/3>; rel="next"`,
			want:   "https://x/3",
		},
		{"malformed", `https://x/2; rel="next"`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextLink(tt.header); got != tt.want {
				t.Errorf("nextLink() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewClientRejectsABadAPIURL(t *testing.T) {
	for _, url := range []string{"not a url at all", "/relative/only", ""} {
		if url == "" {
			// An empty URL is defaulted rather than rejected, so the default has to work.
			c, err := newClient("", "token", "", nil)
			if err != nil || c.base.Host != "api.github.com" {
				t.Errorf("newClient(\"\") = %v, %v; want the public API default", c, err)
			}
			continue
		}
		if _, err := newClient(url, "token", "", nil); err == nil {
			t.Errorf("newClient(%q) = nil error, want failure", url)
		}
	}
}

func TestNewClientPinsTheAPIVersion(t *testing.T) {
	c, err := newClient("https://api.github.com", "token", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.version != DefaultAPIVersion {
		t.Errorf("version = %q, want the pinned default %q", c.version, DefaultAPIVersion)
	}
}
