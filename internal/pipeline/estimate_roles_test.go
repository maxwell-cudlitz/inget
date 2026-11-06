// Pricing and historical-cache regressions use stored derivations and no model calls.
package pipeline

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

func TestEstimateUsesSeparateViewBudgetAndPrices(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.Views = []config.View{{Name: "role", DependsOn: []string{"**"}}}
	p := profileFor(h)
	p.Views = map[string]StageProfile{"role": p.Views["role"]}
	rec := oneFragmentRecord(100, sha256Hex("content"))
	result := &reconcileResult{plan: Plan{WorkItems: []string{rec.ItemID}}, records: map[string]*artifact.Record{rec.ItemID: rec}}
	rc := h.runConfig()
	rc.Pricing = Pricing{PerMTokIn: 2, PerMTokOut: 3, MaxOutputTokens: 64}
	rc.ViewPricing = &Pricing{PerMTokIn: 11, PerMTokOut: 17, MaxOutputTokens: 2048}
	rc.EstimateProfile = p
	if err := estimateWork(h.ctx, h.deps, rc, result); err != nil {
		t.Fatal(err)
	}
	est := result.plan.Estimate
	wrapper, err := h.deps.FragEnricher.PromptChars(enrich.FragmentTemplateData{Key: "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	fragmentInput := (100 + wrapper) / 4
	viewInput := (100 + len("Generate role: ")) / 4
	want := float64(fragmentInput*2+64*3+viewInput*11+2048*17) / 1e6
	if est.OutputTokens != 2112 || math.Abs(est.CostUSD-want) > 1e-12 {
		t.Fatalf("estimate=%+v want output=2112 cost=%g", est, want)
	}
	expectedFragmentInput := 2 + .25*float64(100+wrapper)
	expectedViewInput := 1 + .5*float64(4+len("big.txt")+40+len("Generate role: "))
	wantExpected := (expectedFragmentInput*2 + 10*3 + expectedViewInput*11 + 20*17) / 1e6
	if math.Abs(est.Expected.CostUSD-wantExpected) > 1e-12 {
		t.Fatalf("expected cost=%g want %g", est.Expected.CostUSD, wantExpected)
	}
	stage := p.Views["role"]
	stage.OutputTokens = 65 // above fragment budget, inside view budget
	p.Views["role"] = stage
	if err := validateEstimateProfile(p, h.deps, rc); err != nil {
		t.Fatalf("view profile was checked against the fragment budget: %v", err)
	}
	stage.OutputTokens = 2049
	p.Views["role"] = stage
	if err := validateEstimateProfile(p, h.deps, rc); err == nil || !strings.Contains(err.Error(), "output budget") {
		t.Fatalf("view output budget was ignored: %v", err)
	}
}

func TestExpectedEstimateReadsAllowedHistoricalCacheText(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.Views = []config.View{{Name: "role", DependsOn: []string{"**"}}}
	p := profileFor(h)
	p.Views = map[string]StageProfile{"role": p.Views["role"]}
	rec := oneFragmentRecord(100, sha256Hex("content"))
	key := delta.FragmentCacheKey("big.txt", rec.Fragments[0].Fingerprint, "historical")
	if err := h.store.PutDerivation(h.ctx, state.Derivation{CacheKey: key, Signature: "historical", Output: "éé"}); err != nil {
		t.Fatal(err)
	}
	rc := h.runConfig()
	rc.CachedFragmentsOnly = true
	rc.CachedFragmentSignatures = []string{"historical"}
	rc.EstimateProfile = p
	result := &reconcileResult{plan: Plan{WorkItems: []string{rec.ItemID}}, records: map[string]*artifact.Record{rec.ItemID: rec}}
	if err := estimateWork(h.ctx, h.deps, rc, result); err != nil {
		t.Fatal(err)
	}
	est := result.plan.Estimate
	want := 1 + .5*float64(4+len("big.txt")+2+len("Generate role: "))
	if est.FragmentDerivations != 0 || est.Expected.InputTokens != want {
		t.Fatalf("historical summary was not reused for estimation: %+v; want expected input %g", est, want)
	}
	rc.EstimateProfile = nil
	result = &reconcileResult{plan: Plan{WorkItems: []string{rec.ItemID}}, records: map[string]*artifact.Record{rec.ItemID: rec}}
	if err := estimateWork(h.ctx, h.deps, rc, result); err != nil {
		t.Fatal(err)
	}
	native := result.plan.Estimate
	wantNative := (4 + len("big.txt") + 2 + len("Generate role: ")) / 4
	if native.Expected != nil || native.FragmentDerivations != 0 || native.InputTokens != wantNative {
		t.Fatalf("native cache-only estimate priced raw files or missed historical text: %+v; want input %d", native, wantNative)
	}
}

func TestExplicitRebuildHandlesUnrecordedSignature(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.deps.State = unrecordedSignatureStore{Store: h.store}
	h.reset()
	rc := h.runConfig()
	rc.DryRun = true
	plan, _, err := Run(h.ctx, h.deps, h.arts, rc)
	if err != nil || len(plan.WorkItems) != 0 {
		t.Fatalf("default behavior changed for unrecorded signatures: plan=%+v err=%v", plan, err)
	}
	rc.RebuildOnSignatureChange = true
	plan, _, err = Run(h.ctx, h.deps, h.arts, rc)
	if err != nil || len(plan.WorkItems) != 1 || len(plan.Estimate.ChangedSignatures) == 0 || plan.Estimate.FragmentDerivations != 0 {
		t.Fatalf("explicit rebuild omitted existing index or lost cache: plan=%+v err=%v", plan, err)
	}
}

type unrecordedSignatureStore struct{ state.Store }

func (s unrecordedSignatureStore) Signature(context.Context, string) (string, error) {
	return "", nil
}

func TestLimitedSignatureRebuildDoesNotApplyTombstones(t *testing.T) {
	h := newCascadeHarness(t)
	seedDeletionItem(t, h, "owner/gone")
	writeDeletionRun(t, h, artifact.ScopeFull, artifact.CommitInfo{}, "owner/one", "owner/two")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	replaceViewPrompt(t, h, "role", "New role: {{.Document}}")
	writeDeletionRun(t, h, artifact.ScopeFull, artifact.CommitInfo{Tombstones: []string{"owner/gone"}}, "owner/one", "owner/two")
	rc := h.runConfig()
	rc.RebuildOnSignatureChange = true
	rc.Limit = 1
	rc.DryRun = true
	plan, _, err := Run(h.ctx, h.deps, h.arts, rc)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Restricted || len(plan.WorkItems) != 1 || len(plan.Tombstones) != 0 || plan.Deleted != 0 {
		t.Fatalf("limited signature widening retained full-scope tombstones: %+v", plan)
	}
	rc.DryRun = false
	_, stats, err := Run(h.ctx, h.deps, h.arts, rc)
	if err != nil || stats.TombstonesApplied != 0 {
		t.Fatalf("limited rebuild applied tombstones: stats=%+v error=%v", stats, err)
	}
	assertDeletionItem(t, h, "owner/gone", true)
}
