// Invalid profiles must fail before any model call, and matching profiles remain explicit.
package pipeline

import (
	"math"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
)

func TestEstimateProfileValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*EstimateProfile)
		want   string
	}{
		{"version", func(p *EstimateProfile) { p.Version = 2 }, "version 1"},
		{"datatype", func(p *EstimateProfile) { p.Datatype = "other/type" }, "datatype"},
		{"missing fragment", func(p *EstimateProfile) { p.Fragment = nil }, "fragment stage"},
		{"stale fragment", func(p *EstimateProfile) { p.Fragment.Signature = "old" }, "signature"},
		{"no fragment samples", func(p *EstimateProfile) { p.Fragment.Samples = 0 }, "sample"},
		{"missing fragment length", func(p *EstimateProfile) { p.Fragment.OutputChars = 0 }, "output_chars"},
		{"negative slope", func(p *EstimateProfile) { p.Fragment.InputTokensPerChar = -1 }, "nonnegative"},
		{"nonfinite slope", func(p *EstimateProfile) { p.Fragment.InputTokensPerChar = math.Inf(1) }, "finite"},
		{"nonfinite offset", func(p *EstimateProfile) { p.Fragment.InputTokenIntercept = math.NaN() }, "finite"},
		{"output budget", func(p *EstimateProfile) { p.Fragment.OutputTokens = 513 }, "output budget"},
		{"missing view", func(p *EstimateProfile) { delete(p.Views, "role") }, "views"},
		{"extra view", func(p *EstimateProfile) { p.Views["new"] = StageProfile{} }, "views"},
		{"stale view", func(p *EstimateProfile) {
			s := p.Views["role"]
			s.Signature = "old"
			p.Views["role"] = s
		}, "signature"},
		{"no view samples", func(p *EstimateProfile) {
			s := p.Views["role"]
			s.Samples = 0
			p.Views["role"] = s
		}, "sample"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCascadeHarness(t)
			p := profileFor(h)
			tc.change(p)
			err := validateEstimateProfile(p, h.deps, h.runConfig())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want containing %s", err, tc.want)
			}
			if h.gen.Calls() != 0 {
				t.Fatal("profile validation generated content")
			}
		})
	}
}

func TestEstimateProfileCannotOverflowExpectedTokens(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.Views = nil
	p := profileFor(h)
	p.Views = nil
	p.Fragment.InputTokensPerChar = math.MaxFloat64
	rec := oneFragmentRecord(100, sha256Hex("content"))
	result := &reconcileResult{plan: Plan{WorkItems: []string{rec.ItemID}}, records: map[string]*artifact.Record{rec.ItemID: rec}}
	rc := h.runConfig()
	rc.EstimateProfile = p
	if err := estimateWork(h.ctx, h.deps, rc, result); err == nil || !strings.Contains(err.Error(), "non-finite") {
		t.Fatalf("error=%v, want non-finite estimate rejected", err)
	}
}

func TestExpectedEstimateHonorsViewScope(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.Views = []config.View{{Name: "role", DependsOn: []string{"missing/**"}}}
	p := profileFor(h)
	p.Views = map[string]StageProfile{"role": p.Views["role"]}
	rec := oneFragmentRecord(100, sha256Hex("content"))
	est := estimateRecords(t, h, p, rec)
	if est.ViewGenerations != 0 || est.Expected.OutputTokens != p.Fragment.OutputTokens {
		t.Fatalf("unmatched view scope was priced: %+v", est)
	}
}
