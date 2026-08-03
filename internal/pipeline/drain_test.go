// Tests for the artifact backlog: the self-submission topology, and the conditions under which
// the high-water mark refuses to advance.
//
// The behaviour under test is a loss of work that is silent by construction — a run nobody ever
// read — so the assertions are made against the destination and the generator rather than
// against the pipeline's own counters.
package pipeline

import (
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
)

// submit commits one partial artifact run carrying one item, which is what a CI job announcing
// its own repository produces.
func submit(t *testing.T, h *cascadeHarness, itemID string) {
	t.Helper()
	h.scope = artifact.ScopePartial
	h.writeRunWithItems(baseVersion, noChange, "", itemID)
}

// consumedMark is the datatype's recorded high-water mark.
func consumedMark(t *testing.T, h *cascadeHarness) string {
	t.Helper()
	mark, err := h.store.ConsumedArtifactRun(h.ctx, testDatatype)
	if err != nil {
		t.Fatalf("ConsumedArtifactRun: %v", err)
	}
	return mark
}

// seedMark runs one clean pass so that a mark exists. Until one does, only the latest run is
// pending — the deliberate behaviour of a store that has never drained.
func seedMark(t *testing.T, h *cascadeHarness) {
	t.Helper()
	submit(t, h, "owner/seed")
	if _, _, err := h.run(); err != nil {
		t.Fatalf("seeding the mark: %v", err)
	}
	if consumedMark(t, h) == "" {
		t.Fatal("a clean run recorded no mark")
	}
	h.reset()
}

// Every submission committed since the last clean pass is consumed, not just the newest. This is
// the whole point of the mark: with one artifact run per item, reading only the latest run means
// every other producer's work is never looked at.
func TestDrainConsumesEveryPendingSubmission(t *testing.T) {
	h := newCascadeHarness(t)
	seedMark(t, h)

	submit(t, h, "owner/one")
	submit(t, h, "owner/two")

	plan, err := h.plan()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PendingArtifactRuns != 2 {
		t.Errorf("plan: pending artifact runs = %d, want 2", plan.PendingArtifactRuns)
	}

	_, stats, err := h.run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stats.ItemsProcessed != 2 {
		t.Errorf("processed = %d, want 2 (one item per pending run)", stats.ItemsProcessed)
	}
	for _, itemID := range []string{"owner/one", "owner/two"} {
		if _, found, err := h.store.Item(h.ctx, testDatatype, itemID); err != nil || !found {
			t.Errorf("item %s: found=%v err=%v, want it processed", itemID, found, err)
		}
	}

	// The mark now names the newest run, so a second invocation has nothing left to do.
	h.reset()
	_, stats2, err := h.run()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats2.ItemsProcessed != 0 || h.gen.Calls() != 0 {
		t.Errorf("second run: processed = %d, generator calls = %d, want 0 and 0",
			stats2.ItemsProcessed, h.gen.Calls())
	}
}

// A pass narrowed by --only cannot speak for the whole artifact run, so the mark stays put and
// the rest of the run is picked up next time.
func TestDrainDoesNotAdvanceOnARestrictedPass(t *testing.T) {
	h := newCascadeHarness(t)
	seedMark(t, h)
	before := consumedMark(t, h)

	h.scope = artifact.ScopePartial
	h.writeRunWithItems(baseVersion, noChange, "", "owner/one", "owner/two")

	rc := h.runConfig()
	rc.Only = []string{"owner/one"}
	_, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatalf("restricted run: %v", err)
	}
	if stats.ItemsProcessed != 1 {
		t.Errorf("processed = %d, want 1", stats.ItemsProcessed)
	}
	if got := consumedMark(t, h); got != before {
		t.Errorf("mark = %q, want it unchanged at %q after a restricted pass", got, before)
	}

	// Unrestricted, the same run finishes and the mark moves.
	h.reset()
	_, stats2, err := h.run()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats2.ItemsProcessed != 1 {
		t.Errorf("second run: processed = %d, want 1 (the item the first pass skipped)", stats2.ItemsProcessed)
	}
	if got := consumedMark(t, h); got == before {
		t.Error("mark did not advance after a clean unrestricted pass")
	}
}

// An interrupted drain leaves the mark alone, so the backlog survives the eviction.
func TestDrainSurvivesInterruption(t *testing.T) {
	h := newCascadeHarness(t)
	seedMark(t, h)
	before := consumedMark(t, h)

	submit(t, h, "owner/one")
	submit(t, h, "owner/two")

	if _, _, err := h.runInterrupted(); err != nil {
		t.Fatalf("interrupted run: %v", err)
	}
	if got := consumedMark(t, h); got != before {
		t.Errorf("mark = %q, want it unchanged at %q after an interrupted drain", got, before)
	}

	h.reset()
	_, stats, err := h.run()
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if stats.ItemsProcessed != 2 {
		t.Errorf("resumed run: processed = %d, want 2", stats.ItemsProcessed)
	}
}

// With no mark recorded — every store before this feature — only the latest run is pending, so
// an upgrade does not silently replay a month of retained runs.
func TestUnmarkedDatatypeConsumesOnlyTheLatestRun(t *testing.T) {
	h := newCascadeHarness(t)
	submit(t, h, "owner/one")
	submit(t, h, "owner/two")

	plan, err := h.plan()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PendingArtifactRuns != 1 {
		t.Errorf("pending artifact runs = %d, want 1", plan.PendingArtifactRuns)
	}

	_, stats, err := h.run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stats.ItemsProcessed != 1 {
		t.Errorf("processed = %d, want 1 (the latest run only)", stats.ItemsProcessed)
	}
}
