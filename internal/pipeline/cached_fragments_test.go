// Behavioral coverage of view rebuilds that must never generate file summaries.
package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

func TestCachedFragmentsOnlyRebuildsViewsFromExistingSummaries(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	replaceViewPrompt(t, h, "role", "Generate role with more detail: {{.Document}}")
	h.reset()
	rc := h.runConfig()
	rc.RebuildOnSignatureChange = true
	rc.CachedFragmentsOnly = true
	rc.DryRun = true
	plan, _, err := h.runWith(rc)
	if err != nil {
		t.Fatalf("cached rebuild plan: %v", err)
	}
	if plan.Estimate.FragmentDerivations != 0 || plan.Estimate.ViewGenerations == 0 || h.gen.Calls() != 0 {
		t.Fatalf("plan estimate = %+v, generator calls = %d; want only view allowance and no calls",
			plan.Estimate, h.gen.Calls())
	}
	rc.DryRun = false
	_, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatalf("cached rebuild: %v", err)
	}
	if stats.ItemsProcessed != 1 || stats.FragmentsEnrich != 0 || stats.ViewsGenerated != 1 || h.gen.Calls() != 1 {
		t.Fatalf("cached rebuild stats = %+v, calls = %d; want one changed view and no fragment generation",
			stats, h.gen.Calls())
	}
	if h.emb.Embedded() != 1 || h.dest.rows() != 1 {
		t.Fatalf("embeddings/rows = %d/%d, want one regenerated view", h.emb.Embedded(), h.dest.rows())
	}
}

func TestCachedFragmentsOnlyPreflightsBeforeAnyModelCall(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(map[bool]string{true: "plan", false: "run"}[dryRun], func(t *testing.T) {
			h := newCascadeHarness(t)
			// The two repositories share cache keys; count missing unique derivations once.
			h.writeRunWithItems(baseVersion, noChange, "", "owner/first", "owner/second")
			rc := h.runConfig()
			rc.DryRun = dryRun
			rc.CachedFragmentsOnly = true
			_, _, err := h.runWith(rc)
			if err == nil || !strings.Contains(err.Error(), "100 unique fragment summaries are missing") {
				t.Fatalf("cache-only error = %v, want exact missing count", err)
			}
			if h.gen.Calls() != 0 || h.emb.Embedded() != 0 || h.dest.upserts() != 0 {
				t.Fatalf("preflight failure made model or destination calls: generator %d, embedder %d, upserts %d",
					h.gen.Calls(), h.emb.Embedded(), h.dest.upserts())
			}
		})
	}
}

func TestCachedFragmentsOnlyRejectsDifferentFragmentSignature(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	prompt, err := enrich.ParsePrompt("fragment", []byte("Different file summary: {{.Content}}"))
	if err != nil {
		t.Fatal(err)
	}
	h.deps.FragEnricher = enrich.NewFragmentLLMEnricher(h.gen, prompt, enrich.FragmentEnricherConfig{MaxInputChars: 8000})
	h.reset()
	rc := h.runConfig()
	rc.CachedFragmentsOnly = true
	rc.RebuildOnSignatureChange = true
	_, _, err = h.runWith(rc)
	if err == nil || !strings.Contains(err.Error(), "current fragment signature") {
		t.Fatalf("cache-only error = %v, want signature-specific cache refusal", err)
	}
	if h.gen.Calls() != 0 || h.emb.Embedded() != 0 {
		t.Fatal("generated or embedded using an incompatible cached file summary")
	}
}

func TestCachedFragmentsOnlyWorksWithoutSignatureRebuild(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.writeRun(baseVersion, 50, "v2")
	rec, _ := buildCascadeRecord(testItemID, baseVersion, 50, "v2")
	frag := rec.Fragments[50]
	sig := h.deps.FragEnricher.Signature()
	if err := h.store.PutDerivation(h.ctx, state.Derivation{
		CacheKey: delta.FragmentCacheKey(frag.Key, frag.Fingerprint, sig),
		Datatype: testDatatype, ItemID: testItemID, FragKey: frag.Key,
		Signature: sig, Output: "An existing summary for the changed file.",
	}); err != nil {
		t.Fatal(err)
	}
	h.reset()
	rc := h.runConfig()
	rc.CachedFragmentsOnly = true
	_, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FragmentsEnrich != 0 || stats.ViewsGenerated != 3 || h.gen.Calls() != 3 {
		t.Fatalf("cache-only delta stats = %+v, calls = %d; want three scoped views and no file generation",
			stats, h.gen.Calls())
	}
}

func TestCachedFragmentsOnlyRefusesMissAfterSuccessfulPreflight(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	replaceViewPrompt(t, h, "role", "Generate role with more detail: {{.Document}}")
	// HasDerivation still sees the cache, but execution sees the entry disappear.
	h.deps.State = &evictedDerivationsStore{Store: h.store}
	h.reset()
	rc := h.runConfig()
	rc.CachedFragmentsOnly = true
	rc.RebuildOnSignatureChange = true
	_, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ItemsFailed != 1 || stats.FragmentsEnrich != 0 || stats.ViewsGenerated != 0 {
		t.Fatalf("post-preflight eviction stats = %+v, want failure without generation", stats)
	}
	if h.gen.Calls() != 0 || h.emb.Embedded() != 0 || h.dest.upserts() != 0 {
		t.Fatal("cache eviction fell back to fragment generation or generated incomplete views")
	}
}

// TestCachedFragmentsOnlyResumeUsesPreflightedSelection excludes the old run's unselected work.
func TestCachedFragmentsOnlyResumeUsesPreflightedSelection(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRunWithItems(baseVersion, noChange, "", "owner/first")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.writeRunWithItems("v2", noChange, "", "owner/second")
	h.reset()
	if _, _, err := h.runInterrupted(); err != nil {
		t.Fatal(err)
	}
	// The old queue contains the uncached second item. The new restricted selection
	// contains no changed items, so none of that old queue was preflighted or authorized.
	rc := h.runConfig()
	rc.CachedFragmentsOnly = true
	rc.Only = []string{"owner/first"}
	plan, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Resuming || len(plan.WorkItems) != 0 || stats.ItemsProcessed != 0 || stats.ItemsFailed != 0 {
		t.Fatalf("resumed plan/stats = %+v / %+v, want empty restricted work without inherited failures", plan, stats)
	}
	if h.gen.Calls() != 0 || h.emb.Embedded() != 0 {
		t.Fatal("resumed cache-only pass processed unselected work")
	}
}

// evictedDerivationsStore models cache eviction between preflight and fragment processing.
type evictedDerivationsStore struct{ state.Store }

func (*evictedDerivationsStore) Derivation(context.Context, string) (string, bool, error) {
	return "", false, nil
}
