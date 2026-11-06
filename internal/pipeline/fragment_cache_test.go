// Historical fragment reuse is explicit, fingerprint-exact, ordered, and never relabeled.
package pipeline

import (
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

func TestHistoricalFragmentSummariesRebuildViewsWithoutRelabeling(t *testing.T) {
	h := newCascadeHarness(t)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	oldSig := h.deps.FragEnricher.Signature()
	rec, _ := buildCascadeRecord(testItemID, baseVersion, noChange, "")
	frag := rec.Fragments[0]
	oldKey := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, oldSig)
	oldText, found, err := h.store.PeekDerivation(h.ctx, oldKey)
	if err != nil || !found {
		t.Fatalf("old cached summary = %q, %t, %v", oldText, found, err)
	}
	replaceFragmentPrompt(t, h)
	replaceViewPrompt(t, h, "role", "Generate role: richer detail {{.Document}}")
	h.reset()
	rc := h.runConfig()
	rc.CachedFragmentsOnly = true
	rc.RebuildOnSignatureChange = true
	rc.CachedFragmentSignatures = []string{oldSig}
	rc.DryRun = true
	plan, _, err := h.runWith(rc)
	if err != nil || plan.Estimate.FragmentDerivations != 0 {
		t.Fatalf("historical-cache plan = %+v, %v; want zero fragment allowance", plan, err)
	}
	rc.DryRun = false
	_, stats, err := h.runWith(rc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FragmentsEnrich != 0 || stats.ItemsFailed != 0 || h.gen.Calls() != stats.ViewsGenerated || stats.ViewsGenerated == 0 {
		t.Fatalf("historical-cache stats/calls = %+v/%d; want successful views without files", stats, h.gen.Calls())
	}
	if !strings.Contains(h.gen.promptFor("role"), oldText) {
		t.Fatal("view generation did not consume the historical cached output")
	}
	currentKey := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, h.deps.FragEnricher.Signature())
	if hit, err := h.store.HasDerivation(h.ctx, currentKey); err != nil || hit {
		t.Fatalf("historical output was relabeled under the current cache key: %t, %v", hit, err)
	}
	keptText, found, err := h.store.PeekDerivation(h.ctx, oldKey)
	if err != nil || !found || keptText != oldText {
		t.Fatalf("old cache changed: %q, %t, %v", keptText, found, err)
	}
	storedSig, err := h.store.Signature(h.ctx, testDatatype+":fragment")
	if err != nil || storedSig != oldSig {
		t.Fatalf("fragment scope falsely recorded current provenance: %q, %v", storedSig, err)
	}
}

func TestHistoricalFragmentReuseRequiresAllowlistAndExactFingerprint(t *testing.T) {
	for _, changeFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "no default reuse", true: "no fingerprint fallback"}[changeFile], func(t *testing.T) {
			h := newCascadeHarness(t)
			h.writeRun(baseVersion, noChange, "")
			if _, _, err := h.run(); err != nil {
				t.Fatal(err)
			}
			oldSig := h.deps.FragEnricher.Signature()
			replaceFragmentPrompt(t, h)
			rc := h.runConfig()
			rc.CachedFragmentsOnly = true
			rc.RebuildOnSignatureChange = true
			wantMissing := "100 unique fragment summaries"
			if changeFile {
				h.writeRun(baseVersion, 50, "v2")
				rc.CachedFragmentSignatures = []string{oldSig}
				wantMissing = "1 unique fragment summaries"
			}
			h.reset()
			_, _, err := h.runWith(rc)
			if err == nil || !strings.Contains(err.Error(), wantMissing) {
				t.Fatalf("cache refusal = %v, want %s", err, wantMissing)
			}
			if h.gen.Calls() != 0 || h.emb.Embedded() != 0 {
				t.Fatal("historical cache refusal made model calls")
			}
		})
	}
}

func TestHistoricalFragmentCachePreferenceMatchesPreflightAndReads(t *testing.T) {
	for _, currentExists := range []bool{true, false} {
		t.Run(map[bool]string{true: "current preferred", false: "ordered historical fallback"}[currentExists], func(t *testing.T) {
			h := newCascadeHarness(t)
			frag := artifact.Fragment{Key: "README.md", Fingerprint: "exact-fingerprint", Blob: "unused"}
			signatures := []string{"old-first", "old-second"}
			want := "old-first"
			if currentExists {
				signatures = append(signatures, "current")
				want = "current"
			}
			for _, sig := range signatures {
				if err := h.store.PutDerivation(h.ctx, state.Derivation{
					CacheKey: delta.FragmentCacheKey(frag.Key, frag.Fingerprint, sig), Datatype: testDatatype,
					ItemID: testItemID, FragKey: frag.Key, Signature: sig, Output: sig,
				}); err != nil {
					t.Fatal(err)
				}
			}
			allowed := []string{"old-first", "old-second", "old-first"}
			key, found, err := cachedFragmentKey(h.ctx, h.store, frag, "current", allowed)
			if err != nil || !found || key != delta.FragmentCacheKey(frag.Key, frag.Fingerprint, want) {
				t.Fatalf("preflight preference = %q, %t, %v; want %s", key, found, err, want)
			}
			ex := &execution{deps: h.deps, fragSig: "current", cacheOnly: true, cachedFragmentSignatures: allowed}
			text, err := deriveFragment(h.ctx, ex, testItemID, frag)
			if err != nil || text != want || h.gen.Calls() != 0 {
				t.Fatalf("cache preference = %q, %v, calls %d; want %s without generation", text, err, h.gen.Calls(), want)
			}
		})
	}
}

func TestHistoricalFragmentAllowlistValidation(t *testing.T) {
	for _, rc := range []RunConfig{
		{CachedFragmentSignatures: []string{"old"}},
		{CachedFragmentsOnly: true, CachedFragmentSignatures: []string{""}},
		{CachedFragmentsOnly: true, CachedFragmentSignatures: []string{" \t"}},
	} {
		if err := validateCachedFragmentSignatures(rc); err == nil {
			t.Fatalf("invalid historical reuse config accepted: %+v", rc)
		}
	}
}

// replaceFragmentPrompt changes only the current fragment contract, leaving its old cache.
func replaceFragmentPrompt(t *testing.T, h *cascadeHarness) {
	t.Helper()
	prompt, err := enrich.ParsePrompt("fragment", []byte("A new file-summary contract: {{.Content}}"))
	if err != nil {
		t.Fatal(err)
	}
	h.deps.FragEnricher = enrich.NewFragmentLLMEnricher(h.gen, prompt, enrich.FragmentEnricherConfig{MaxInputChars: 8000})
}
