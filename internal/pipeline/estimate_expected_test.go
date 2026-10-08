// Behavioral tests for globally shared derivations and measured prompt accounting.
// All calls use the sqlite harness and counting fakes; no model or network is used.
package pipeline

import (
	"context"
	"math"
	"testing"
	"unicode/utf8"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

type estimateCountingState struct {
	state.Store
	has, peek int
}

func (s *estimateCountingState) HasDerivation(ctx context.Context, key string) (bool, error) {
	s.has++
	return s.Store.HasDerivation(ctx, key)
}

func (s *estimateCountingState) PeekDerivation(ctx context.Context, key string) (string, bool, error) {
	s.peek++
	return s.Store.PeekDerivation(ctx, key)
}

func profileFor(h *cascadeHarness) *EstimateProfile {
	p := &EstimateProfile{Version: 1, Datatype: testDatatype, Views: make(map[string]StageProfile),
		Fragment: &StageProfile{Signature: h.deps.FragEnricher.Signature(), Samples: 7,
			InputTokensPerChar: .25, InputTokenIntercept: 2, OutputTokens: 10, OutputChars: 40}}
	for name, enricher := range h.deps.Enrichers {
		p.Views[name] = StageProfile{Signature: enricher.Signature(), Samples: 3,
			InputTokensPerChar: .5, InputTokenIntercept: 1, OutputTokens: 20}
	}
	return p
}

func estimateRecords(t *testing.T, h *cascadeHarness, p *EstimateProfile, records ...*artifact.Record) Estimate {
	t.Helper()
	result := &reconcileResult{records: make(map[string]*artifact.Record)}
	for _, rec := range records {
		result.records[rec.ItemID] = rec
		result.plan.WorkItems = append(result.plan.WorkItems, rec.ItemID)
	}
	rc := h.runConfig()
	rc.EstimateProfile = p
	if err := estimateWork(h.ctx, h.deps, rc, result); err != nil {
		t.Fatal(err)
	}
	if h.gen.Calls() != 0 || h.emb.Embedded() != 0 || h.dest.upserts() != 0 {
		t.Fatal("estimate executed generation, embedding or destination writes")
	}
	return result.plan.Estimate
}

func TestEstimateDeduplicatesKeysAcrossItems(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.Views = nil
	s := &estimateCountingState{Store: h.store}
	h.deps.State = s
	first := oneFragmentRecord(100, sha256Hex("content"))
	second := *first
	second.ItemID = "owner/two"
	second.Fragments = append([]artifact.Fragment(nil), first.Fragments...)
	third := *first
	third.ItemID = "owner/three"
	third.Fragments = append([]artifact.Fragment(nil), first.Fragments...)
	third.Fragments[0].Key = "other.txt"
	fourth := *first
	fourth.ItemID = "owner/four"
	fourth.Fragments = append([]artifact.Fragment(nil), first.Fragments...)
	fourth.Fragments[0].Fingerprint = sha256Hex("changed")
	est := estimateRecords(t, h, nil, first, &second, &third, &fourth)
	if est.FragmentDerivations != 3 || s.has != 3 || s.peek != 0 {
		t.Fatalf("derivations=%d cache checks=%d peeks=%d, want 3/3/0", est.FragmentDerivations, s.has, s.peek)
	}
	if est.Expected != nil {
		t.Fatal("unrequested expected estimate was emitted")
	}
}

func TestExpectedEstimateUsesCachedTextWrappersAndSaturation(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.Views = []config.View{{Name: "role", DependsOn: []string{"**"}}}
	h.deps.Config.ComposeMaxChars = 30
	p := profileFor(h)
	p.Views = map[string]StageProfile{"role": p.Views["role"]}
	rec := oneFragmentRecord(100, sha256Hex("content"))
	cached := rec.Fragments[0]
	cached.Key = "cached.txt"
	rec.Fragments = append(rec.Fragments, cached)
	key := delta.FragmentCacheKey(cached.Key, cached.Fingerprint, p.Fragment.Signature)
	if err := h.store.PutDerivation(h.ctx, state.Derivation{CacheKey: key, Signature: p.Fragment.Signature, Output: "éé"}); err != nil {
		t.Fatal(err)
	}
	s := &estimateCountingState{Store: h.store}
	h.deps.State = s
	est := estimateRecords(t, h, p, rec)
	fixedFragment, err := h.deps.FragEnricher.PromptChars(enrich.FragmentTemplateData{Key: "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	wantInput := 2 + .25*float64(100+fixedFragment) + 1 + .5*float64(30+len("Generate role: "))
	if math.Abs(est.Expected.InputTokens-wantInput) > 1e-9 || est.Expected.OutputTokens != 30 {
		t.Fatalf("expected=%+v, want input %g output 30", est.Expected, wantInput)
	}
	if s.peek != 1 || est.FragmentDerivations != 1 || est.Expected.FragmentSamples != 7 || est.Expected.ViewSamples["role"] != 3 {
		t.Fatalf("cache/sample accounting: peek=%d estimate=%+v", s.peek, est)
	}
	frags := fragEstimate{sig: p.Fragment.Signature, chars: map[string]float64{
		delta.FragmentCacheKey("big.txt", cached.Fingerprint, p.Fragment.Signature): 40, key: 2}}
	wantChars := float64(4 + utf8.RuneCountInString("big.txt") + 40 + 5 + 4 + utf8.RuneCountInString("cached.txt") + 2)
	if got := composedEstimateChars(rec, []string{"**"}, frags); got != wantChars {
		t.Fatalf("composed chars=%g want %g", got, wantChars)
	}
	if !est.Expected.Approximate || len(est.Expected.Exclusions) < 2 || est.Expected.CostUSD <= 0 {
		t.Fatalf("missing expected cost qualifications: %+v", est.Expected)
	}
}

func TestExpectedEstimateNoChangesCostsZero(t *testing.T) {
	h := newCascadeHarness(t)
	est := estimateRecords(t, h, profileFor(h))
	if est.Expected == nil || est.Expected.InputTokens != 0 || est.Expected.OutputTokens != 0 || est.Expected.CostUSD != 0 {
		t.Fatalf("no-work estimate=%+v", est)
	}
}

func TestExpectedEstimateRawFragmentsWhenEnrichmentDisabled(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.FragmentEnricher = false
	h.deps.Config.Views = []config.View{{Name: "role", DependsOn: []string{"big.txt"}}}
	p := profileFor(h)
	p.Fragment = nil
	p.Views = map[string]StageProfile{"role": p.Views["role"]}
	rec := oneFragmentRecord(100, sha256Hex("content"))
	est := estimateRecords(t, h, p, rec)
	want := 1 + .5*float64(100+4+len("big.txt")+len("Generate role: "))
	if est.Expected.InputTokens != want || est.FragmentDerivations != 0 {
		t.Fatalf("raw-fragment estimate=%+v, want input %g", est, want)
	}
}
