// Behavioral coverage of rebuilding only repositories with successful prior checkpoints.
package pipeline

import (
	"slices"
	"testing"
)

func TestIndexedOnlyRestrictsSignatureRebuildAndCombinesSelectors(t *testing.T) {
	all := []string{"owner/01-unindexed", "owner/02-indexed", "owner/03-indexed", "owner/04-indexed"}
	tests := []struct {
		name  string
		only  []string
		limit int
		want  []string
	}{
		{name: "all previously indexed", want: all[1:]},
		{name: "only intersection before limit", only: []string{all[0], all[2], all[3]}, limit: 1, want: []string{all[2]}},
		{name: "empty only intersection", only: []string{all[0]}, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newCascadeHarness(t)
			h.writeRunWithItems(baseVersion, noChange, "", all...)
			seed := h.runConfig()
			seed.Only = all[1:]
			if _, _, err := h.runWith(seed); err != nil {
				t.Fatal(err)
			}
			replaceViewPrompt(t, h, "role", "Generate a detailed role: {{.Document}}")
			h.reset()
			rc := h.runConfig()
			rc.IndexedOnly = true
			rc.CachedFragmentsOnly = true
			rc.RebuildOnSignatureChange = true
			rc.Only = tt.only
			rc.Limit = tt.limit
			rc.DryRun = true
			plan, _, err := h.runWith(rc)
			if err != nil {
				t.Fatalf("indexed-only plan: %v", err)
			}
			if !slices.Equal(plan.WorkItems, tt.want) || !plan.Restricted {
				t.Fatalf("work/restricted = %v/%t, want %v/true", plan.WorkItems, plan.Restricted, tt.want)
			}
			if h.gen.Calls() != 0 || h.emb.Embedded() != 0 {
				t.Fatal("indexed-only planning made model calls")
			}
			rc.DryRun = false
			_, stats, err := h.runWith(rc)
			if err != nil {
				t.Fatalf("indexed-only rebuild: %v", err)
			}
			if stats.ItemsProcessed != len(tt.want) || stats.ItemsFailed != 0 || stats.FragmentsEnrich != 0 || h.gen.Calls() != len(tt.want) {
				t.Fatalf("stats/calls = %+v/%d, want exactly %d view-only rebuilds", stats, h.gen.Calls(), len(tt.want))
			}
			fingerprints, err := h.store.ItemFingerprints(h.ctx, testDatatype)
			if err != nil {
				t.Fatal(err)
			}
			if _, indexed := fingerprints[all[0]]; indexed {
				t.Fatal("indexed-only pass indexed a previously unindexed repository")
			}
		})
	}
}

func TestIndexedOnlyWithNoIndexedItemsSelectsNothing(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRunWithItems(baseVersion, noChange, "", "owner/one", "owner/two")
	rc := h.runConfig()
	rc.IndexedOnly = true
	rc.CachedFragmentsOnly = true
	rc.RebuildOnSignatureChange = true
	rc.DryRun = true
	plan, _, err := h.runWith(rc)
	if err != nil {
		t.Fatalf("empty indexed-only plan: %v", err)
	}
	if len(plan.WorkItems) != 0 || !plan.Restricted || plan.Estimate.FragmentDerivations != 0 || plan.Estimate.ViewGenerations != 0 {
		t.Fatalf("empty indexed set expanded to work: %+v", plan)
	}
	rc.DryRun = false
	_, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ItemsProcessed != 0 || stats.ItemsFailed != 0 || h.gen.Calls() != 0 || h.emb.Embedded() != 0 {
		t.Fatalf("empty indexed-only pass stats/calls = %+v/%d, want no work", stats, h.gen.Calls())
	}
}

func TestIndexedOnlyResumeCannotInheritUnindexedWork(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.runInterrupted(); err != nil {
		t.Fatal(err)
	}
	rc := h.runConfig()
	rc.IndexedOnly = true
	plan, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Resuming || len(plan.WorkItems) != 0 || stats.ItemsProcessed != 0 || stats.ItemsFailed != 0 || h.gen.Calls() != 0 {
		t.Fatalf("indexed-only resume inherited unindexed work: plan %+v, stats %+v, calls %d", plan, stats, h.gen.Calls())
	}
}
