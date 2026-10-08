// Consumer deletion safety: records describe changed items, while only producer tombstones
// prove source absence. These cases use real artifact and state stores before inspecting
// deletion calls, so omissions cannot quietly turn into destination deletes.
package pipeline

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

func TestFullArtifactWithoutTombstonesPreservesOmittedItems(t *testing.T) {
	h := newCascadeHarness(t)
	for _, id := range []string{"owner/unchanged", "owner/failed"} {
		seedDeletionItem(t, h, id)
	}
	writeDeletionRun(t, h, artifact.ScopeFull, artifact.CommitInfo{
		ItemsSkippedUnchanged: 1,
		Warnings:              []string{"owner/failed: fetch failed: temporary error"},
	})
	plan, stats, err := h.run()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Deleted != 0 || stats.TombstonesApplied != 0 || len(h.dest.deleted) != 0 {
		t.Fatalf("omitted records caused deletions: plan=%d applied=%d calls=%v",
			plan.Deleted, stats.TombstonesApplied, h.dest.deleted)
	}
	assertDeletionItem(t, h, "owner/unchanged", true)
	assertDeletionItem(t, h, "owner/failed", true)
	if h.gen.Calls() != 0 || h.emb.Embedded() != 0 {
		t.Fatal("an artifact with no records spent model calls")
	}
}

func TestExplicitTombstonesDeleteOnlyNamedItemsOnce(t *testing.T) {
	h := newCascadeHarness(t)
	for _, id := range []string{"owner/gone", "owner/z", "owner/keep"} {
		seedDeletionItem(t, h, id)
	}
	writeDeletionRun(t, h, artifact.ScopeFull, artifact.CommitInfo{
		Tombstones: []string{"owner/z", "owner/gone", "owner/z"},
	})
	want := []string{"owner/gone", "owner/z"}
	plan, err := h.plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Deleted != 2 || !slices.Equal(plan.Tombstones, want) {
		t.Fatalf("plan deletions = %d %v, want 2 %v", plan.Deleted, plan.Tombstones, want)
	}
	assertDeletionItem(t, h, "owner/gone", true) // Planning must remain read-only.
	_, stats, err := h.run()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TombstonesApplied != 2 || !slices.Equal(h.dest.deleted, want) {
		t.Fatalf("applied=%d calls=%v, want 2 %v", stats.TombstonesApplied, h.dest.deleted, want)
	}
	assertDeletionItem(t, h, "owner/gone", false)
	assertDeletionItem(t, h, "owner/z", false)
	assertDeletionItem(t, h, "owner/keep", true)
}

func TestIneligibleTombstonesAreNotPlannedOrApplied(t *testing.T) {
	tests := []struct {
		name      string
		scope     artifact.Scope
		truncated bool
		adjust    func(*RunConfig)
	}{
		{name: "partial artifact", scope: artifact.ScopePartial},
		{name: "truncated artifact", scope: artifact.ScopeFull, truncated: true},
		{name: "only", scope: artifact.ScopeFull, adjust: func(rc *RunConfig) {
			rc.Only = []string{"owner/new-a"}
		}},
		{name: "limit reached", scope: artifact.ScopeFull, adjust: func(rc *RunConfig) {
			rc.Limit = 1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newCascadeHarness(t)
			seedDeletionItem(t, h, "owner/gone")
			info := artifact.CommitInfo{Truncated: tt.truncated}
			if tt.scope == artifact.ScopeFull {
				info.Tombstones = []string{"owner/gone"}
			}
			writeDeletionRun(t, h, tt.scope, info, "owner/new-a", "owner/new-b")
			rc := h.runConfig()
			if tt.adjust != nil {
				tt.adjust(&rc)
			}
			rc.DryRun = true
			plan, _, err := h.runWith(rc)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Deleted != 0 || len(plan.Tombstones) != 0 {
				t.Fatalf("ineligible deletion appeared in plan: %+v", plan)
			}
			rc.DryRun = false
			_, stats, err := h.runWith(rc)
			if err != nil {
				t.Fatal(err)
			}
			if stats.TombstonesApplied != 0 || len(h.dest.deleted) != 0 {
				t.Fatal("ineligible tombstone reached the destination")
			}
			assertDeletionItem(t, h, "owner/gone", true)
		})
	}
}

func TestContradictoryTombstoneFailsBeforeDeletionOrGeneration(t *testing.T) {
	for _, tt := range []struct{ name, id string }{
		{"record and tombstone", "owner/keep"}, {"empty ID", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newCascadeHarness(t)
			seedDeletionItem(t, h, "owner/keep")
			writeDeletionRun(t, h, artifact.ScopeFull, artifact.CommitInfo{
				Tombstones: []string{tt.id},
			}, "owner/keep")
			_, _, err := h.run()
			if err == nil || !strings.Contains(err.Error(), "tombstone") {
				t.Fatalf("contradictory manifest error = %v, want tombstone error", err)
			}
			assertDeletionItem(t, h, "owner/keep", true)
			if h.gen.Calls() != 0 || h.emb.Embedded() != 0 || len(h.dest.deleted) != 0 {
				t.Fatal("invalid manifest performed paid work or deletion")
			}
		})
	}
}

func seedDeletionItem(t *testing.T, h *cascadeHarness, id string) {
	t.Helper()
	if err := h.store.PutItem(h.ctx, testDatatype, state.Item{
		ID: id, Source: testSource, Fingerprint: "v1", Metadata: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDeletionItem(t *testing.T, h *cascadeHarness, id string, want bool) {
	t.Helper()
	_, found, err := h.store.Item(h.ctx, testDatatype, id)
	if err != nil || found != want {
		t.Fatalf("item %s active = %v, %v; want %v", id, found, err, want)
	}
}

func writeDeletionRun(t *testing.T, h *cascadeHarness, scope artifact.Scope, info artifact.CommitInfo, ids ...string) {
	t.Helper()
	w, err := h.arts.NewWriter(h.ctx, artifact.RunInfo{
		Source: testSource, Datatype: testDatatype, Scope: scope,
		Producer: "test", DomainHash: "sha256:domain", ConfigHash: testConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	for _, id := range ids {
		if err := w.Write(h.ctx, &artifact.Record{
			ItemID: id, Fingerprint: "v2", FetchedAt: time.Now(),
			Metadata: map[string]string{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Commit(h.ctx, info); err != nil {
		t.Fatal(err)
	}
}
