// Tests for the cost estimate.
//
// The estimate is an upper bound, but an upper bound is still a number a person decides with, so
// what it must not do is price work the run will not perform: content that truncation will cut
// before it is sent, and fragments that hold no content at all.
package pipeline

import (
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
)

// estimateFor prices one record with the harness's stores, with views under the caller's control.
func estimateFor(t *testing.T, h *cascadeHarness, frags fragEstimate, views []config.View, rec *artifact.Record) Estimate {
	t.Helper()
	h.deps.Config.Views = views
	var est Estimate
	if err := estimateItem(h.ctx, h.deps, h.runConfig(), rec, frags, &est); err != nil {
		t.Fatalf("estimating: %v", err)
	}
	return est
}

// oneFragmentRecord is a record holding a single fragment of the given size, with or without a
// stored blob.
func oneFragmentRecord(bytes int64, blob string) *artifact.Record {
	return &artifact.Record{
		Datatype: testDatatype,
		ItemID:   testItemID,
		Fragments: []artifact.Fragment{{
			Key:         "big.txt",
			Fingerprint: sha256Hex("big.txt"),
			Blob:        blob,
			Bytes:       bytes,
			Tier:        3,
		}},
		FragmentCount: 1,
	}
}

// A fragment larger than the enricher's input bound is priced at the bound, because that is what
// the enricher will send.
func TestEstimateBoundsFragmentInputByMaxInputChars(t *testing.T) {
	h := newCascadeHarness(t)
	frags := fragEstimate{sig: h.deps.FragEnricher.Signature(), maxChars: 400}

	est := estimateFor(t, h, frags, nil, oneFragmentRecord(1<<20, sha256Hex("content")))

	if est.FragmentDerivations != 1 {
		t.Fatalf("fragment derivations = %d, want 1", est.FragmentDerivations)
	}
	if want := 400 / 4; est.InputTokens != want {
		t.Errorf("input tokens = %d, want %d (the bound, not the file)", est.InputTokens, want)
	}
}

// A view's input is bounded by compose.max_chars for the same reason.
func TestEstimateBoundsViewInputByComposeMaxChars(t *testing.T) {
	h := newCascadeHarness(t)
	h.deps.Config.ComposeMaxChars = 800
	views := []config.View{{Name: "role", Prompt: "role.tmpl", DependsOn: []string{"**"}}}

	est := estimateFor(t, h, fragEstimate{}, views, oneFragmentRecord(1<<20, sha256Hex("content")))

	if est.ViewGenerations != 1 {
		t.Fatalf("view generations = %d, want 1", est.ViewGenerations)
	}
	if want := 800 / 4; est.InputTokens != want {
		t.Errorf("input tokens = %d, want %d", est.InputTokens, want)
	}
}

// A fragment with no stored content — a detected secret, a file over the split budget — costs
// nothing: nothing is sent for it and it reaches no composed document.
func TestEstimateSkipsFragmentsWithoutContent(t *testing.T) {
	h := newCascadeHarness(t)
	frags := fragEstimate{sig: h.deps.FragEnricher.Signature(), maxChars: 8000}
	views := []config.View{{Name: "role", Prompt: "role.tmpl", DependsOn: []string{"**"}}}

	est := estimateFor(t, h, frags, views, oneFragmentRecord(1<<20, ""))

	if est.FragmentDerivations != 0 {
		t.Errorf("fragment derivations = %d, want 0", est.FragmentDerivations)
	}
	if est.ViewGenerations != 0 {
		t.Errorf("view generations = %d, want 0 (the view has nothing to compose)", est.ViewGenerations)
	}
	if est.InputTokens != 0 || est.OutputTokens != 0 {
		t.Errorf("tokens = %d in, %d out, want 0 and 0", est.InputTokens, est.OutputTokens)
	}
}

// With fragment enrichment off no derivation is priced, whatever the fragments look like.
func TestEstimateSkipsDerivationsWhenEnrichmentIsOff(t *testing.T) {
	h := newCascadeHarness(t)

	est := estimateFor(t, h, fragEstimate{}, nil, oneFragmentRecord(4000, sha256Hex("content")))

	if est.FragmentDerivations != 0 || est.InputTokens != 0 {
		t.Errorf("estimate = %+v, want no derivations and no tokens", est)
	}
}
