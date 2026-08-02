// Tests for the orchestrator: the level-0 guard, blob reuse, tombstone safety, scope handling and
// dry-run inertness.
//
// Two of these are the step's acceptance gate. "Re-running with no upstream changes writes zero new
// blobs" is asserted against the manifest's own counters, and every tombstone case is asserted
// because a wrong answer there deletes a live index.
package fetch

import (
	"slices"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
)

func TestRunCommitsEveryEnumeratedItem(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"), item("acme/b", "v1"))

	report := h.run()
	if report.Items != 2 || report.Enumerated != 2 {
		t.Errorf("report = %+v, want 2 enumerated and 2 written", report)
	}
	if report.SkippedUnchanged != 0 {
		t.Errorf("skipped %d items on a first run, want 0", report.SkippedUnchanged)
	}
	if report.Fragments != 4 {
		t.Errorf("fragments = %d, want 4 across two items", report.Fragments)
	}

	manifest := h.latestManifest()
	if manifest.Scope != artifact.ScopeFull {
		t.Errorf("manifest scope = %q, want full", manifest.Scope)
	}
	if manifest.Truncated {
		t.Error("manifest reports truncation on a complete enumeration")
	}
	if manifest.Counts.Items != 2 {
		t.Errorf("manifest counts.items = %d, want 2", manifest.Counts.Items)
	}
	if manifest.DomainHash != testDomain || manifest.ConfigHash != testConfig {
		t.Errorf("manifest hashes = %q/%q, want the configured ones", manifest.DomainHash, manifest.ConfigHash)
	}

	records := h.records()
	rec, ok := records["acme/a"]
	if !ok {
		t.Fatalf("records hold %v, want one per item", mapKeys(records))
	}
	if len(rec.Fragments) != 2 || rec.FragmentCount != 2 {
		t.Errorf("record carries %d fragments (count %d), want 2", len(rec.Fragments), rec.FragmentCount)
	}
	for _, frag := range rec.Fragments {
		if frag.Blob == "" {
			t.Errorf("fragment %s has no blob reference, so its content is unreachable", frag.Key)
		}
	}
}

// The level-0 guard is the cheapest one in the system: an item whose fingerprint matches state costs
// one line of a listing response and no further request at all.
func TestRunSkipsUnchangedItemsWithoutFetching(t *testing.T) {
	unchanged := item("acme/a", "v1")
	h := newHarness(t, unchanged, item("acme/b", "v1"))
	h.checkpoint(unchanged, blobDigests(unchanged))

	report := h.run()
	if report.SkippedUnchanged != 1 {
		t.Errorf("skipped %d items, want 1", report.SkippedUnchanged)
	}
	if report.Items != 1 {
		t.Errorf("wrote %d records, want 1", report.Items)
	}
	if got := h.conn.fetched; !slices.Equal(got, []string{"acme/b"}) {
		t.Errorf("fetched %v, want only the changed item", got)
	}
	if h.latestManifest().Counts.ItemsSkippedUnchanged != 1 {
		t.Error("the manifest does not record the skipped item")
	}
}

// The acceptance criterion: nothing upstream changed, so nothing is uploaded. The fragments are
// reused from what state remembers rather than re-hashed and re-checked.
func TestRefetchWithNoChangesWritesNoBlobs(t *testing.T) {
	one := item("acme/a", "v1")
	h := newHarness(t, one)

	first := h.run()
	if first.BlobsWritten != 2 {
		t.Fatalf("first run wrote %d blobs, want 2", first.BlobsWritten)
	}

	// A successful enrichment run is what records the guards, so simulate one, then change only
	// the item's level-0 token: the fragments themselves are identical.
	h.checkpoint(one, blobDigests(one))
	h.conn.items[0].fingerprint = "acme/a:v2"
	h.conn.reset()

	second := h.run()
	if second.Items != 1 {
		t.Fatalf("second run wrote %d records, want 1", second.Items)
	}
	if second.BlobsWritten != 0 {
		t.Errorf("second run wrote %d blobs, want 0 when no fragment changed", second.BlobsWritten)
	}
	if second.BlobsReused != 2 {
		t.Errorf("second run reused %d blobs, want 2", second.BlobsReused)
	}
	// The record must still resolve: a reused reference is only correct if it names a real blob.
	for _, frag := range h.records()["acme/a"].Fragments {
		if _, err := h.arts.GetBlob(h.ctx, frag.Blob); err != nil {
			t.Errorf("reused blob for %s does not resolve: %v", frag.Key, err)
		}
	}
}

// One changed file uploads exactly one blob. This is content addressing doing its job across a run
// boundary rather than within one.
func TestChangedFragmentWritesOneBlob(t *testing.T) {
	one := item("acme/a", "v1")
	h := newHarness(t, one)
	h.run()
	h.checkpoint(one, blobDigests(one))

	h.conn.items[0].fingerprint = "acme/a:v2"
	h.conn.items[0].fragments["main.go"] = "// main.go at v2\n"
	h.conn.reset()

	report := h.run()
	if report.BlobsWritten != 1 {
		t.Errorf("wrote %d blobs after one file changed, want 1", report.BlobsWritten)
	}
	if report.BlobsReused != 1 {
		t.Errorf("reused %d blobs, want 1", report.BlobsReused)
	}
}

// A failed item is that item's problem. It must not become a tombstone, because the item exists at
// the source and only the download failed.
func TestFailedItemDoesNotDeleteAnything(t *testing.T) {
	failing := item("acme/broken", "v1")
	failing.fail = true
	h := newHarness(t, item("acme/a", "v1"), failing)
	h.checkpoint(item("acme/broken", "v0"), nil)

	report := h.run()
	if report.Failed != 1 {
		t.Errorf("failed = %d, want 1", report.Failed)
	}
	if report.Items != 1 {
		t.Errorf("wrote %d records, want 1", report.Items)
	}
	if report.Tombstones != 0 {
		t.Errorf("tombstones = %d, want 0: the item exists, the fetch failed", report.Tombstones)
	}
	if !hasWarning(report.Warnings, "fetch failed") {
		t.Errorf("warnings = %v, want one naming the failure", report.Warnings)
	}
}

// A dry run answers "what would this cost" and must leave nothing behind, so an operator can ask it
// against production without consequence.
func TestDryRunWritesNothing(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"), item("acme/b", "v1"))

	report := h.run(func(rc *RunConfig) { rc.DryRun = true })
	if !report.DryRun || report.Enumerated != 2 || report.Items != 2 {
		t.Errorf("report = %+v, want a dry run reporting 2 items", report)
	}
	if report.BlobsWritten != 0 {
		t.Errorf("a dry run wrote %d blobs, want 0", report.BlobsWritten)
	}
	if h.conn.fetchCount() != 0 {
		t.Errorf("a dry run fetched %d items, want 0", h.conn.fetchCount())
	}
	if runs, err := h.arts.ListRuns(h.ctx, testSource, testDatatype); err != nil || len(runs) != 0 {
		t.Errorf("ListRuns() = %v, %v; want no committed run after a dry run", runs, err)
	}
}

// A dry run reports what the level-0 guard would skip, which is the only reason it needs the state
// store at all.
func TestDryRunAppliesTheLevelZeroGuard(t *testing.T) {
	unchanged := item("acme/a", "v1")
	h := newHarness(t, unchanged, item("acme/b", "v1"))
	h.checkpoint(unchanged, blobDigests(unchanged))

	report := h.run(func(rc *RunConfig) { rc.DryRun = true })
	if report.SkippedUnchanged != 1 || report.Items != 1 {
		t.Errorf("report = %+v, want 1 skipped and 1 to do", report)
	}
}

func TestFragmentCapTruncatesAndWarns(t *testing.T) {
	big := item("acme/a", "v1", "README.md", "main.go", "go.mod", "Makefile")
	h := newHarness(t, big)

	report := h.run(func(rc *RunConfig) { rc.MaxFragmentsPerItem = 2 })
	if report.Fragments != 2 {
		t.Errorf("wrote %d fragments under a cap of 2, want 2", report.Fragments)
	}
	if !hasWarning(report.Warnings, "max_fragments_per_item") {
		t.Errorf("warnings = %v, want one naming the cap", report.Warnings)
	}

	rec := h.records()["acme/a"]
	if !rec.FragmentTruncated {
		t.Error("the record does not report that fragments were dropped")
	}
	if rec.FragmentCount != 4 {
		t.Errorf("fragment_count = %d, want the pre-cap total 4", rec.FragmentCount)
	}
}

// Warnings the connector produced belong in the manifest: a run that quietly dropped half a
// repository and one that fetched all of it must not look the same afterwards.
func TestConnectorWarningsReachTheManifest(t *testing.T) {
	noisy := item("acme/a", "v1")
	noisy.warnings = []string{"acme/a: the tree API truncated its response"}
	h := newHarness(t, noisy)

	h.run()
	if got := h.latestManifest().Warnings; !hasWarning(got, "truncated its response") {
		t.Errorf("manifest warnings = %v, want the connector's line", got)
	}
}

// A second fetch must not adopt the first one's identity: artifact runs are immutable, so a new
// attempt is a new run directory.
func TestEachRunGetsItsOwnIdentifier(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"))

	first := h.run()
	second := h.run()
	if first.RunID == second.RunID {
		t.Error("two runs share an identifier, so the second would overwrite the first")
	}
	runs, err := h.arts.ListRuns(h.ctx, testSource, testDatatype)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Errorf("ListRuns() = %v, want two committed runs", runs)
	}
}

func TestRunRecordsItsHistory(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"))

	report := h.run()
	status, found, err := h.state.RunStatus(h.ctx, report.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || status != "ok" {
		t.Errorf("RunStatus(%s) = %q, %v; want ok", report.RunID, status, found)
	}
}

// A concurrent fetch of the same datatype must not run: two producers enumerating at once would
// spend double the quota to write two runs, one of which is redundant.
func TestSecondFetchIsLockedOut(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"))

	release, err := h.state.Lock(h.ctx, h.config().lockKey())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	if _, err := Run(h.ctx, h.deps(), h.config()); err == nil {
		t.Fatal("Run() while the fetch lock is held = nil error, want failure")
	}
}

// Content over artifacts.blob_max_bytes is recorded as truncated rather than failing the item. The
// fragment still describes the source: its presence and fingerprint are useful even when the bytes
// are not stored.
func TestOversizedContentIsRecordedAsTruncated(t *testing.T) {
	big := item("acme/a", "v1")
	big.fragments["huge.txt"] = strings.Repeat("x", 4096)
	h := newHarnessWithBlobCap(t, 512, big)

	report := h.run()
	if report.Items != 1 {
		t.Fatalf("wrote %d records, want 1: an oversized file is not a failed item", report.Items)
	}
	if report.Failed != 0 {
		t.Errorf("failed = %d, want 0", report.Failed)
	}

	var found bool
	for _, frag := range h.records()["acme/a"].Fragments {
		if frag.Key != "huge.txt" {
			continue
		}
		found = true
		if frag.Blob != "" {
			t.Error("an oversized fragment carries a blob reference")
		}
		if !frag.Truncated {
			t.Error("an oversized fragment is not marked truncated")
		}
		if frag.Bytes != 4096 {
			t.Errorf("bytes = %d, want the size at the source 4096", frag.Bytes)
		}
	}
	if !found {
		t.Error("the oversized fragment was dropped rather than recorded")
	}
}

func TestRunRejectsIncompleteConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		adjust func(*RunConfig)
	}{
		{"no source", func(rc *RunConfig) { rc.Source = "" }},
		{"no datatype", func(rc *RunConfig) { rc.Datatype = "" }},
		{"no producer", func(rc *RunConfig) { rc.Producer = "" }},
		{"no hashes", func(rc *RunConfig) { rc.ConfigHash = "" }},
		{"bad scope", func(rc *RunConfig) { rc.Scope = "sometimes" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, item("acme/a", "v1"))
			rc := h.config()
			tt.adjust(&rc)
			if _, err := Run(h.ctx, h.deps(), rc); err == nil {
				t.Error("Run() = nil error, want failure")
			}
		})
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}
	return false
}

func mapKeys(m map[string]*artifact.Record) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
