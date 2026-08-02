// Tests for tombstone safety: the one place where a wrong answer deletes a live index.
//
// Deletions are only inferable from a run that enumerated the entire configured domain and finished
// doing so. Every way of not doing that — partial scope, explicit items, a reached limit — has a case
// here, and so does the positive case, because a guard that suppresses everything is no guard.
package fetch

import (
	"slices"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
)

func TestFullRunTombstonesItemsTheSourceNoLongerHas(t *testing.T) {
	gone := item("acme/gone", "v1")
	h := newHarness(t, item("acme/a", "v1"))
	h.checkpoint(gone, blobDigests(gone))

	report := h.run()
	if report.Tombstones != 1 {
		t.Fatalf("tombstones = %d, want 1", report.Tombstones)
	}
	if got := h.latestManifest().Tombstones; !slices.Equal(got, []string{"acme/gone"}) {
		t.Errorf("manifest tombstones = %v, want [acme/gone]", got)
	}
}

// Every case where a run did not see the whole domain must produce no tombstones. Getting any of
// these wrong deletes a live index.
func TestTombstonesAreSuppressedWhenTheDomainWasNotFullySeen(t *testing.T) {
	gone := item("acme/gone", "v1")
	tests := []struct {
		name   string
		adjust func(*RunConfig)
	}{
		{"partial scope", func(rc *RunConfig) { rc.Scope = artifact.ScopePartial }},
		{"explicit items", func(rc *RunConfig) { rc.Only = []string{"acme/a"} }},
		{"limit reached", func(rc *RunConfig) { rc.Limit = 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, item("acme/a", "v1"), item("acme/b", "v1"))
			h.checkpoint(gone, blobDigests(gone))

			report := h.run(tt.adjust)
			if report.Tombstones != 0 {
				t.Errorf("tombstones = %d, want 0", report.Tombstones)
			}
			if got := h.latestManifest().Tombstones; len(got) != 0 {
				t.Errorf("manifest tombstones = %v, want none", got)
			}
		})
	}
}

// A limit means enumeration stopped early, which the manifest has to say: a consumer reading a full
// run cannot otherwise tell "absent" from "not reached".
func TestLimitMarksTheRunTruncated(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"), item("acme/b", "v1"), item("acme/c", "v1"))

	report := h.run(func(rc *RunConfig) { rc.Limit = 2 })
	if !report.Truncated {
		t.Error("report does not mark a limited run truncated")
	}
	manifest := h.latestManifest()
	if !manifest.Truncated {
		t.Error("manifest does not mark a limited run truncated")
	}
	if manifest.ProcessTombstones() {
		t.Error("a truncated run says its tombstones are actionable")
	}
	if report.Items != 2 {
		t.Errorf("wrote %d records under a limit of 2, want 2", report.Items)
	}
}

// Explicit items force partial scope regardless of what --scope said, because absence from such a
// run means nothing at all.
func TestOnlyForcesPartialScope(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"), item("acme/b", "v1"))

	report := h.run(func(rc *RunConfig) {
		rc.Scope = artifact.ScopeFull
		rc.Only = []string{"acme/a"}
	})
	if report.Scope != string(artifact.ScopePartial) {
		t.Errorf("scope = %q, want partial", report.Scope)
	}
	if h.latestManifest().Scope != artifact.ScopePartial {
		t.Error("the manifest records a full scope for a run given explicit items")
	}
	if report.Items != 1 {
		t.Errorf("wrote %d records, want only the named item", report.Items)
	}
}
