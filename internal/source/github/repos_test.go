// Tests for enumeration against the fake API: pagination, every domain filter, deduplication
// across the three additive sources, --limit, --since and --only.
package github

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/maxwell-cudlitz/inget/internal/source"
)

// orgDomain is the smallest domain that enumerates one organisation.
func orgDomain(org string) map[string]any {
	return map[string]any{"orgs": []string{org}}
}

func TestListFollowsLinkPagination(t *testing.T) {
	f := newFakeGitHub(t)
	f.pageSize = 2
	for i := range 5 {
		f.addRepo("acme", repo{Name: fmt.Sprintf("svc-%d", i)}, nil)
	}

	refs := collect(t, f.connector(orgDomain("acme"), nil), source.ListQuery{})
	if len(refs) != 5 {
		t.Fatalf("List() returned %d refs, want 5: %v", len(refs), refIDs(refs))
	}
	if got := f.countPaths("/orgs/acme/repos"); got != 3 {
		t.Errorf("served %d listing pages, want 3 for 5 repositories at 2 per page", got)
	}
	// Refs carry the level-0 fingerprint, without which every item would be fetched.
	for _, ref := range refs {
		if ref.Fingerprint == "" {
			t.Errorf("ref %s has no fingerprint, so the level-0 guard cannot skip it", ref.ID)
		}
	}
}

func TestListAppliesDomainFilters(t *testing.T) {
	stale := time.Now().AddDate(0, 0, -400)
	tests := []struct {
		name   string
		meta   repo
		domain map[string]any
		want   bool
	}{
		{"plain repository", repo{Name: "keep"}, orgDomain("acme"), true},
		{"archived excluded by default", repo{Name: "old", Archived: true}, orgDomain("acme"), false},
		{"archived included when asked", repo{Name: "old", Archived: true},
			map[string]any{"orgs": []string{"acme"}, "include_archived": true}, true},
		{"fork excluded by default", repo{Name: "copy", Fork: true}, orgDomain("acme"), false},
		{"fork included when asked", repo{Name: "copy", Fork: true},
			map[string]any{"orgs": []string{"acme"}, "include_forks": true}, true},
		{"private excluded by a public domain", repo{Name: "secret", Visibility: VisibilityPrivate},
			map[string]any{"orgs": []string{"acme"}, "visibility": VisibilityPublic}, false},
		{"private included by an all domain", repo{Name: "secret", Visibility: VisibilityPrivate},
			orgDomain("acme"), true},
		{"inactive excluded", repo{Name: "dormant", PushedAt: stale},
			map[string]any{"orgs": []string{"acme"}, "max_inactive_days": 365}, false},
		{"inactive kept when the cutoff is disabled", repo{Name: "dormant", PushedAt: stale},
			map[string]any{"orgs": []string{"acme"}, "max_inactive_days": 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeGitHub(t)
			f.addRepo("acme", tt.meta, nil)

			refs := collect(t, f.connector(tt.domain, nil), source.ListQuery{})
			if got := len(refs) == 1; got != tt.want {
				t.Errorf("enumerated %v, want kept = %v", refIDs(refs), tt.want)
			}
		})
	}
}

// An empty repository has nothing to fragment, which is a different exclusion from being inactive
// and must not depend on any filter being enabled.
func TestListSkipsEmptyRepositories(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "fresh", DefaultBranch: "-"}, nil)
	f.repos["acme/fresh"].meta.DefaultBranch = ""

	if refs := collect(t, f.connector(orgDomain("acme"), nil), source.ListQuery{}); len(refs) != 0 {
		t.Errorf("enumerated %v, want nothing for a repository with no default branch", refIDs(refs))
	}
}

// The three sources are additive, and a repository reachable by more than one must be enumerated
// once: a duplicate would be fetched twice and would break the record uniqueness the envelope needs.
func TestListDeduplicatesAcrossSources(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "shared"}, nil)
	f.addRepo("acme", repo{Name: "other"}, nil)
	f.addTopic("golang", "acme/shared")

	domain := map[string]any{
		"orgs":   []string{"acme"},
		"repos":  []string{"acme/shared"},
		"topics": []string{"golang"},
	}
	refs := collect(t, f.connector(domain, nil), source.ListQuery{})
	got := refIDs(refs)
	slices.Sort(got)
	want := []string{"acme/other", "acme/shared"}
	if !slices.Equal(got, want) {
		t.Errorf("enumerated %v, want %v exactly once each", got, want)
	}
}

func TestListStopsAtTheLimit(t *testing.T) {
	f := newFakeGitHub(t)
	f.pageSize = 2
	for i := range 10 {
		f.addRepo("acme", repo{Name: fmt.Sprintf("svc-%d", i)}, nil)
	}

	var refs []source.Ref
	err := f.connector(orgDomain("acme"), nil).List(t.Context(), Datatype, source.ListQuery{Limit: 3},
		func(ref source.Ref) error {
			refs = append(refs, ref)
			return nil
		})
	// The sentinel reaches the caller unchanged, which is what tells a run it did not see the
	// whole domain and so must not issue tombstones.
	if !errors.Is(err, source.ErrStopList) {
		t.Fatalf("List() = %v, want source.ErrStopList", err)
	}
	if len(refs) != 3 {
		t.Errorf("List() yielded %d refs, want 3", len(refs))
	}
	if got := f.countPaths("/orgs/acme/repos"); got > 2 {
		t.Errorf("served %d pages after a limit of 3, want to stop early", got)
	}
}

// --since stops a sorted organisation listing rather than filtering it, which is the only way it
// saves any requests at all.
func TestListSinceStopsASortedListing(t *testing.T) {
	f := newFakeGitHub(t)
	f.pageSize = 2
	now := time.Now().UTC()
	for i := range 6 {
		f.addRepo("acme", repo{
			Name:     fmt.Sprintf("svc-%d", i),
			PushedAt: now.AddDate(0, 0, -i), // descending, as sort=pushed&direction=desc returns
		}, nil)
	}

	refs := collect(t, f.connector(orgDomain("acme"), nil), source.ListQuery{Since: now.AddDate(0, 0, -2)})
	if len(refs) != 3 {
		t.Errorf("enumerated %v, want the three repositories pushed within the window", refIDs(refs))
	}
	if got := f.countPaths("/orgs/acme/repos"); got > 2 {
		t.Errorf("served %d pages, want to stop once the cutoff was passed", got)
	}
}

// --only names the items, so the domain filters do not apply: an operator asking for one archived
// repository should get it rather than an empty run with no explanation.
func TestListOnlyBypassesFilters(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "archived-but-asked-for", Archived: true}, nil)

	refs := collect(t, f.connector(orgDomain("acme"), nil),
		source.ListQuery{Only: []string{"acme/archived-but-asked-for"}})
	if len(refs) != 1 {
		t.Errorf("enumerated %v, want the explicitly named repository", refIDs(refs))
	}
	if got := f.countPaths("/orgs/acme/repos"); got != 0 {
		t.Errorf("served %d organisation listings, want none when items are named explicitly", got)
	}
}

// A webhook for a deleted repository is a normal event, so a missing item is a warning rather than
// a failed run.
func TestListOnlyToleratesAMissingRepository(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "present"}, nil)

	refs := collect(t, f.connector(orgDomain("acme"), nil),
		source.ListQuery{Only: []string{"acme/present", "acme/deleted"}})
	if got := refIDs(refs); !slices.Equal(got, []string{"acme/present"}) {
		t.Errorf("enumerated %v, want only the repository that still exists", got)
	}
}

// A 500 is transient and must be retried; the retry is what keeps a long enumeration from failing
// on one bad response.
func TestListRetriesServerErrors(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, nil)
	f.failNext("/orgs/acme/repos", 2)

	refs := collect(t, f.connector(orgDomain("acme"), nil), source.ListQuery{})
	if len(refs) != 1 {
		t.Errorf("enumerated %v after two transient failures, want the repository", refIDs(refs))
	}
}

func TestListRejectsAnUnknownDatatype(t *testing.T) {
	f := newFakeGitHub(t)
	err := f.connector(orgDomain("acme"), nil).List(t.Context(), "monday/item", source.ListQuery{},
		func(source.Ref) error { return nil })
	if err == nil {
		t.Fatal("List() with a foreign datatype = nil error, want failure")
	}
}

func TestDecodeDomainRejectsBadBlocks(t *testing.T) {
	tests := []struct {
		name  string
		block map[string]any
	}{
		{"unknown key", map[string]any{"orgs": []string{"acme"}, "include_forkz": true}},
		{"bad visibility", map[string]any{"orgs": []string{"acme"}, "visibility": "secret"}},
		{"repo without an owner", map[string]any{"repos": []string{"inget"}}},
		{"negative inactivity", map[string]any{"orgs": []string{"acme"}, "max_inactive_days": -1}},
		{"nothing to enumerate", map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeDomain(tt.block); err == nil {
				t.Errorf("decodeDomain(%v) = nil error, want failure", tt.block)
			}
		})
	}
}

func TestDecodeLimitsFillsDefaults(t *testing.T) {
	got, err := decodeLimits(map[string]any{})
	if err != nil {
		t.Fatalf("decodeLimits() = %v", err)
	}
	if got.MaxConcurrent != defaultMaxConcurrent || got.TarballMaxBytes != defaultTarballMaxBytes {
		t.Errorf("decodeLimits() = %+v, want the documented defaults", got)
	}
	if !got.scanSecrets() {
		t.Error("secret scanning defaults to off; publishing credentials must never be the default")
	}

	off, err := decodeLimits(map[string]any{"secret_scan": false})
	if err != nil {
		t.Fatalf("decodeLimits() = %v", err)
	}
	if off.scanSecrets() {
		t.Error("secret_scan: false did not disable scanning")
	}
}
