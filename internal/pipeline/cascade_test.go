// Cascade integration test: the gate for step 8 acceptance.
//
// Seeds state for a 100-fragment item with 8 views, changes one fragment, and asserts:
//   - Exactly one fragment derivation occurs, counted at the generator rather than in the
//     pipeline's own statistics.
//   - Only views whose globs match that fragment regenerate.
//   - Only those views embed and upsert.
//   - A second identical run makes zero generator calls, embeds nothing and upserts nothing.
package pipeline

import (
	"testing"
)

// TestCascadeOneFragmentChange verifies the enrichment cascade does exactly the minimum work
// when one fragment changes.
func TestCascadeOneFragmentChange(t *testing.T) {
	h := newCascadeHarness(t)

	// --- Phase 1: initial full run over 100 fragments ---
	h.writeRun(baseVersion, noChange, "")

	plan1, stats1, err := h.run()
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if plan1.Added != 1 {
		t.Errorf("first run: added = %d, want 1", plan1.Added)
	}
	if stats1.ItemsProcessed != 1 {
		t.Errorf("first run: processed = %d, want 1", stats1.ItemsProcessed)
	}
	if stats1.FragmentsEnrich != numFrags {
		t.Errorf("first run: fragments enriched = %d, want %d", stats1.FragmentsEnrich, numFrags)
	}
	if stats1.ViewsGenerated != numViews {
		t.Errorf("first run: views generated = %d, want %d", stats1.ViewsGenerated, numViews)
	}
	// Every generator call is one fragment derivation or one view generation; nothing else in
	// the pipeline may reach a model.
	if want, got := numFrags+numViews, h.gen.Calls(); got != want {
		t.Errorf("first run: generator calls = %d, want %d", got, want)
	}
	if got := h.emb.Embedded(); got != numViews {
		t.Errorf("first run: texts embedded = %d, want %d", got, numViews)
	}
	if h.dest.rows() != numViews {
		t.Errorf("first run: dest rows = %d, want %d", h.dest.rows(), numViews)
	}

	// --- Phase 2: second identical run does nothing ---
	h.reset()
	h.writeRun(baseVersion, noChange, "")

	plan2, stats2, err := h.run()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if plan2.Unchanged != 1 {
		t.Errorf("second run: unchanged = %d, want 1", plan2.Unchanged)
	}
	if len(plan2.WorkItems) != 0 {
		t.Errorf("second run: work items = %d, want 0", len(plan2.WorkItems))
	}
	if stats2.ViewsGenerated != 0 {
		t.Errorf("second run: views generated = %d, want 0", stats2.ViewsGenerated)
	}
	if h.gen.Calls() != 0 {
		t.Errorf("second run: generator calls = %d, want 0", h.gen.Calls())
	}
	if h.emb.Embedded() != 0 {
		t.Errorf("second run: texts embedded = %d, want 0", h.emb.Embedded())
	}
	if h.dest.upserts() != 0 {
		t.Errorf("second run: dest upsert calls = %d, want 0", h.dest.upserts())
	}

	// --- Phase 3: one fragment changes ---
	// The changed key is src/internal/file_0050.go. It matches "**" (role, internals, aliases)
	// and none of the specific globs, so three views regenerate and five are skipped.
	h.reset()
	h.writeRun(baseVersion, 50, "v2")

	plan3, stats3, err := h.run()
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if plan3.Modified != 1 {
		t.Errorf("third run: modified = %d, want 1", plan3.Modified)
	}
	if stats3.ItemsProcessed != 1 {
		t.Errorf("third run: processed = %d, want 1", stats3.ItemsProcessed)
	}
	if stats3.FragmentsEnrich != 1 {
		t.Errorf("third run: fragments enriched = %d, want 1", stats3.FragmentsEnrich)
	}
	if stats3.ViewsGenerated != 3 {
		t.Errorf("third run: views generated = %d, want 3 (role, internals, aliases)", stats3.ViewsGenerated)
	}
	if stats3.ViewsSkipped != 5 {
		t.Errorf("third run: views skipped = %d, want 5", stats3.ViewsSkipped)
	}
	// One derivation plus three generations, and nothing else: the other 99 fragments came
	// from the derivation cache.
	if want, got := 1+3, h.gen.Calls(); got != want {
		t.Errorf("third run: generator calls = %d, want %d", got, want)
	}
	if got := h.emb.Embedded(); got != 3 {
		t.Errorf("third run: texts embedded = %d, want 3", got)
	}
	if h.dest.rows() != 3 {
		t.Errorf("third run: dest rows = %d, want 3", h.dest.rows())
	}
	// The three vectors arrive in one upsert per destination, not one per view.
	if h.dest.upserts() != 1 {
		t.Errorf("third run: dest upsert calls = %d, want 1", h.dest.upserts())
	}
}

// TestCascadeScopesViewInputToDependencies asserts that a view sees only the fragments it
// depends on. Without scoping, every prompt would carry the whole item, which is both the
// design's compose rule and the difference between a small prompt and a 120k-character one.
func TestCascadeScopesViewInputToDependencies(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}

	stack := h.gen.promptFor("stack")
	if stack == "" {
		t.Fatal("no prompt recorded for the stack view")
	}
	// stack depends on go.mod, package.json and Makefile.
	for _, want := range []string{"go.mod", "Makefile"} {
		if !contains(stack, want) {
			t.Errorf("stack prompt does not mention %q", want)
		}
	}
	for _, unwanted := range []string{"README.md", "src/internal/file_0050.go", "CODEOWNERS"} {
		if contains(stack, unwanted) {
			t.Errorf("stack prompt includes out-of-scope fragment %q", unwanted)
		}
	}

	// A "**" view still sees everything.
	role := h.gen.promptFor("role")
	if !contains(role, "src/internal/file_0050.go") {
		t.Error("role prompt is missing a fragment it depends on")
	}
}
