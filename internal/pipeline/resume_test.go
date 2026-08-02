// The two acceptance criteria that live outside the cascade itself: an interrupted run resumes
// without repeating work, and `inget plan` describes what `inget run` then does.
package pipeline

import (
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/state"
)

// TestInterruptedRunResumesWithoutDuplicatingWork covers the pod-eviction path. Shutdown is
// signalled before any item is claimed, so the run must end as interrupted with its queue
// intact; the next run must adopt that queue and finish it, paying for each item once.
func TestInterruptedRunResumesWithoutDuplicatingWork(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRunWithItems(baseVersion, noChange, "", "owner/one", "owner/two")

	plan1, stats1, err := h.runInterrupted()
	if err != nil {
		t.Fatalf("interrupted run: %v", err)
	}
	if len(plan1.WorkItems) != 2 {
		t.Fatalf("interrupted run: work items = %d, want 2", len(plan1.WorkItems))
	}
	if stats1.ItemsProcessed != 0 {
		t.Errorf("interrupted run: processed = %d, want 0", stats1.ItemsProcessed)
	}
	if h.gen.Calls() != 0 {
		t.Errorf("interrupted run: generator calls = %d, want 0", h.gen.Calls())
	}

	status, found, err := h.store.RunStatus(h.ctx, plan1.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("run %s was not recorded", plan1.RunID)
	}
	if status != state.RunInterrupted {
		t.Errorf("interrupted run: status = %q, want %q", status, state.RunInterrupted)
	}

	// Resume: same run row, both items processed, and each fragment derived exactly once.
	h.reset()
	plan2, stats2, err := h.run()
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if !plan2.Resuming {
		t.Error("resumed run: expected the plan to report resumption")
	}
	if plan2.RunID != plan1.RunID {
		t.Errorf("resumed run: run id = %s, want %s", plan2.RunID, plan1.RunID)
	}
	if stats2.ItemsProcessed != 2 {
		t.Errorf("resumed run: processed = %d, want 2", stats2.ItemsProcessed)
	}
	if stats2.ItemsFailed != 0 {
		t.Errorf("resumed run: failed = %d, want 0", stats2.ItemsFailed)
	}
	// Two items with identical fragment paths and content: the second item's fragments hit the
	// derivation cache the first item filled, so derivations are counted once, not twice.
	if stats2.FragmentsEnrich != numFrags {
		t.Errorf("resumed run: fragments enriched = %d, want %d", stats2.FragmentsEnrich, numFrags)
	}
	if want, got := numFrags+2*numViews, h.gen.Calls(); got != want {
		t.Errorf("resumed run: generator calls = %d, want %d", got, want)
	}

	// A third run has nothing left to do, which is what "without duplicating work" means.
	h.reset()
	_, stats3, err := h.run()
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if stats3.ItemsProcessed != 0 || h.gen.Calls() != 0 {
		t.Errorf("third run: processed = %d, generator calls = %d, want 0 and 0",
			stats3.ItemsProcessed, h.gen.Calls())
	}
}

// TestPlanMatchesRun asserts the plan describes the run that follows it, and that producing it
// costs nothing.
func TestPlanMatchesRun(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRunWithItems(baseVersion, noChange, "", "owner/one", "owner/two")

	plan, err := h.plan()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.WorkItems) != 2 {
		t.Fatalf("plan: work items = %d, want 2", len(plan.WorkItems))
	}
	if plan.RunID != "" {
		t.Errorf("plan: run id = %q, want empty (no run was started)", plan.RunID)
	}
	if h.gen.Calls() != 0 || h.emb.Embedded() != 0 || h.dest.upserts() != 0 {
		t.Errorf("plan spent something: generator=%d embedded=%d upserts=%d",
			h.gen.Calls(), h.emb.Embedded(), h.dest.upserts())
	}

	est := plan.Estimate
	if est.FragmentDerivations != 2*numFrags {
		t.Errorf("plan: fragment derivations = %d, want %d", est.FragmentDerivations, 2*numFrags)
	}
	if est.ViewGenerations != 2*numViews {
		t.Errorf("plan: view generations = %d, want %d", est.ViewGenerations, 2*numViews)
	}
	if est.InputTokens == 0 || est.OutputTokens == 0 || est.CostUSD == 0 {
		t.Errorf("plan: expected non-zero token and cost estimates, got %+v", est)
	}
	if !est.UpperBound {
		t.Error("plan: estimate should be labelled an upper bound")
	}

	// Now run, and hold the plan to what it promised.
	_, stats, err := h.run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stats.ItemsProcessed != len(plan.WorkItems) {
		t.Errorf("run processed %d items, plan named %d", stats.ItemsProcessed, len(plan.WorkItems))
	}
	if stats.ViewsGenerated > est.ViewGenerations {
		t.Errorf("run generated %d views, over the estimated bound of %d",
			stats.ViewsGenerated, est.ViewGenerations)
	}
	// The estimate counts a derivation per fragment per item; the run shares identical
	// fragments between the two items through the cache, so it does strictly less.
	if stats.FragmentsEnrich > est.FragmentDerivations {
		t.Errorf("run derived %d fragments, over the estimated bound of %d",
			stats.FragmentsEnrich, est.FragmentDerivations)
	}
}

// TestPlanReportsSignatureChange asserts a prompt change is surfaced before it is paid for.
func TestPlanReportsSignatureChange(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Re-point one view at a different prompt, which is what editing a template does.
	replaceViewPrompt(t, h, "role", "Generate role differently: {{.Document}}")

	h.reset()
	plan, err := h.plan()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := testDatatype + ":view:role"
	if len(plan.Estimate.ChangedSignatures) != 1 || plan.Estimate.ChangedSignatures[0] != want {
		t.Errorf("changed signatures = %v, want [%s]", plan.Estimate.ChangedSignatures, want)
	}
}
