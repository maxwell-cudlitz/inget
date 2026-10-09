// The two acceptance criteria that live outside the cascade itself: an interrupted run resumes
// without repeating work, and `inget plan` describes what `inget run` then does.
package pipeline

import (
	"slices"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
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
	if est.FragmentDerivations != numFrags {
		t.Errorf("plan: fragment derivations = %d, want %d", est.FragmentDerivations, numFrags)
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
	// Both estimate and runtime share identical fragment cache keys between items.
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
	if len(plan.WorkItems) != 0 {
		t.Errorf("default plan work items = %v, want no full rebuild", plan.WorkItems)
	}
}

// TestSignatureRebuildIsExplicit verifies that a changed prompt is reported but does not widen
// the default work set; the explicit flag plans and processes every live item.
func TestSignatureRebuildIsExplicit(t *testing.T) {
	h := newCascadeHarness(t)
	items := []string{"owner/one", "owner/two"}
	h.writeRunWithItems(baseVersion, noChange, "", items...)
	if _, _, err := h.run(); err != nil {
		t.Fatalf("initial run: %v", err)
	}

	replaceViewPrompt(t, h, "role", "Generate role differently: {{.Document}}")
	h.writeRunWithItems(baseVersion, noChange, "", items...)
	h.reset()

	defaultPlan, err := h.plan()
	if err != nil {
		t.Fatalf("default plan: %v", err)
	}
	if len(defaultPlan.WorkItems) != 0 || len(defaultPlan.Estimate.ChangedSignatures) != 1 {
		t.Fatalf("default plan = work %v, changed signatures %v; want no work and one changed signature",
			defaultPlan.WorkItems, defaultPlan.Estimate.ChangedSignatures)
	}
	_, stats, err := h.run()
	if err != nil {
		t.Fatalf("default run: %v", err)
	}
	if stats.ItemsProcessed != 0 || h.gen.Calls() != 0 {
		t.Errorf("default run processed %d items with %d generator calls, want 0 and 0",
			stats.ItemsProcessed, h.gen.Calls())
	}

	partial := h.runConfig()
	partial.RebuildOnSignatureChange = true
	partial.Only = []string{items[0]}
	_, partialStats, err := Run(h.ctx, h.deps, h.arts, partial)
	if err != nil {
		t.Fatalf("limited rebuild: %v", err)
	}
	if partialStats.ItemsProcessed != 1 || partialStats.ItemsFailed != 0 {
		t.Fatalf("limited rebuild stats = %+v, want one successful item", partialStats)
	}
	h.reset()
	stillChanged, err := h.plan()
	if err != nil {
		t.Fatalf("plan after limited rebuild: %v", err)
	}
	if len(stillChanged.Estimate.ChangedSignatures) != 1 {
		t.Fatalf("limited rebuild recorded signature early: %v", stillChanged.Estimate.ChangedSignatures)
	}

	rc := h.runConfig()
	rc.DryRun = true
	rc.RebuildOnSignatureChange = true
	fullPlan, _, err := Run(h.ctx, h.deps, h.arts, rc)
	if err != nil {
		t.Fatalf("rebuild plan: %v", err)
	}
	if !slices.Equal(fullPlan.WorkItems, items) || !slices.Equal(fullPlan.Estimate.ChangedSignatures, []string{testDatatype + ":view:role"}) {
		t.Fatalf("rebuild plan work/signatures = %v / %v", fullPlan.WorkItems, fullPlan.Estimate.ChangedSignatures)
	}

	rc.DryRun = false
	h.reset()
	_, stats, err = Run(h.ctx, h.deps, h.arts, rc)
	if err != nil {
		t.Fatalf("explicit rebuild: %v", err)
	}
	if stats.ItemsProcessed != len(items) || stats.ItemsFailed != 0 {
		t.Errorf("explicit rebuild stats = %+v, want %d processed and no failures", stats, len(items))
	}
	if h.gen.Calls() != 1 {
		t.Errorf("explicit rebuild generator calls = %d, want one remaining changed view", h.gen.Calls())
	}

	h.reset()
	cleanPlan, err := h.plan()
	if err != nil {
		t.Fatalf("post-rebuild plan: %v", err)
	}
	if len(cleanPlan.WorkItems) != 0 || len(cleanPlan.Estimate.ChangedSignatures) != 0 {
		t.Errorf("post-rebuild plan work/signatures = %v / %v, want none",
			cleanPlan.WorkItems, cleanPlan.Estimate.ChangedSignatures)
	}
}

func TestSignatureRebuildRejectsPartialArtifact(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatalf("initial run: %v", err)
	}
	replaceViewPrompt(t, h, "role", "Generate role differently: {{.Document}}")
	h.scope = artifact.ScopePartial
	h.writeRun(baseVersion, noChange, "")

	rc := h.runConfig()
	rc.DryRun = true
	rc.RebuildOnSignatureChange = true
	if _, _, err := Run(h.ctx, h.deps, h.arts, rc); err == nil || !strings.Contains(err.Error(), "requires a full-scope artifact") {
		t.Fatalf("partial artifact rebuild error = %v, want full-scope guard", err)
	}
}
