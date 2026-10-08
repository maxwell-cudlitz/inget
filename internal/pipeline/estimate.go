// What a run would spend, reported before it spends it.
//
// `inget plan` exists to make cost visible, so its legacy numbers form a conservative
// generation allowance. An exact count is not obtainable without doing the work: the level-2 guard
// compares a hash of the composed derivations, and the derivations are what the estimate is
// trying to price. So every view whose scope changed is counted as a generation even though
// some of them will turn out to compose to an unchanged document, every call is priced at the
// full output budget rather than the shorter completion it will probably return, and a view's
// input is the raw size of the fragments in its scope even though what it composes is their
// shorter derived text. Rendered prompt wrappers are included. Input is bounded by the
// same fragment and composition truncation the run applies, because pricing
// characters no call sends is not conservatism, it is a wrong number.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/delta"
)

// charsPerToken converts characters to tokens for estimation. Four is the usual ratio for
// English prose and source code in byte-pair encodings. Tokenization, retries and embedding
// charges mean the legacy upper_bound label is not a guaranteed provider-bill ceiling.
const charsPerToken = 4

// Estimate carries the legacy generation allowance and optional calibrated expectation.
type Estimate struct {
	FragmentDerivations int               `json:"fragment_derivations"`
	ViewGenerations     int               `json:"view_generations"`
	InputTokens         int               `json:"input_tokens"`
	OutputTokens        int               `json:"output_tokens"`
	CostUSD             float64           `json:"cost_usd"`
	ChangedSignatures   []string          `json:"changed_signatures,omitempty"`
	UpperBound          bool              `json:"upper_bound"`
	Expected            *ExpectedEstimate `json:"expected,omitempty"`
}

// surfaceSignatureChanges warns about any enricher signature that no longer matches what was
// last recorded, and records the affected scopes on the plan.
//
// It runs on every invocation, plan or not, because a prompt edit silently reusing cached
// output is the failure it exists to prevent and the check costs one query per view.
func surfaceSignatureChanges(ctx context.Context, deps Deps, result *reconcileResult) error {
	changed, err := changedSignatures(ctx, deps)
	if err != nil {
		return err
	}
	result.plan.Estimate.ChangedSignatures = changed
	if len(changed) > 0 {
		// The affected count is every live item, because a signature is part of every one of
		// their cache keys.
		slog.WarnContext(ctx, "enricher signature changed, cached derivations and views are invalid",
			"datatype", deps.Config.Name, "scopes", changed, "items_affected", result.plan.TotalItems)
	}
	return nil
}

// estimateWork fills the plan's Estimate.
//
// Only plan mode calls it. The count walks every fragment of every changed item and asks the
// derivation cache about each one, which is affordable when the answer is the whole point and
// wasteful ahead of a run that is about to look those same keys up anyway.
func estimateWork(ctx context.Context, deps Deps, rc RunConfig, result *reconcileResult) error {
	cfg := deps.Config
	est := result.plan.Estimate
	est.UpperBound = true
	if err := validateEstimateProfile(rc.EstimateProfile, deps, rc.Pricing); err != nil {
		return err
	}
	if rc.EstimateProfile != nil {
		est.Expected = newExpectedEstimate(rc.EstimateProfile)
	}

	var fragSig string
	fragMaxChars := 0
	if cfg.FragmentEnricher && deps.FragEnricher != nil {
		fragSig = deps.FragEnricher.Signature()
		fragMaxChars = deps.FragEnricher.MaxInputChars()
	}

	frags := fragEstimate{sig: fragSig, maxChars: fragMaxChars, seen: make(map[string]bool), chars: make(map[string]float64)}
	for _, itemID := range result.plan.WorkItems {
		rec, ok := result.records[itemID]
		if !ok {
			continue
		}
		if err := estimateItem(ctx, deps, rc, rec, frags, &est); err != nil {
			return err
		}
	}

	est.CostUSD = float64(est.InputTokens)/1e6*rc.Pricing.PerMTokIn +
		float64(est.OutputTokens)/1e6*rc.Pricing.PerMTokOut
	if est.Expected != nil {
		est.Expected.CostUSD = est.Expected.InputTokens/1e6*rc.Pricing.PerMTokIn +
			est.Expected.OutputTokens/1e6*rc.Pricing.PerMTokOut
		if !finiteNonNegative(est.Expected.InputTokens) || !finiteNonNegative(est.Expected.OutputTokens) ||
			!finiteNonNegative(est.Expected.CostUSD) {
			return fmt.Errorf("estimate profile produced a non-finite cost or token estimate")
		}
	}
	result.plan.Estimate = est
	return nil
}

// fragEstimate is what pricing a fragment needs to know about the fragment enricher: its
// signature, which decides cache hits, and its input bound, which decides tokens. A zero
// signature means fragment enrichment is off and no fragment is derived.
type fragEstimate struct {
	sig      string
	maxChars int
	seen     map[string]bool
	chars    map[string]float64
}

// estimateItem adds one item's derivations and view generations to est.
func estimateItem(ctx context.Context, deps Deps, rc RunConfig, rec *artifact.Record, frags fragEstimate, est *Estimate) error {
	cfg := deps.Config
	if frags.seen == nil {
		frags.seen = make(map[string]bool)
	}
	if frags.chars == nil {
		frags.chars = make(map[string]float64)
	}

	cached, err := deps.State.Fragments(ctx, cfg.Name, rec.ItemID)
	if err != nil {
		return fmt.Errorf("loading fragments for %s/%s: %w", cfg.Name, rec.ItemID, err)
	}
	changed := changedKeys(delta.Reconcile(fingerprints(cached), incoming(rec)))

	for _, frag := range rec.Fragments {
		if frags.sig == "" {
			break // no fragment enrichment: raw content is composed, nothing is generated
		}
		if frag.Blob == "" {
			// Withheld or empty content — a detected secret, a file over the split budget.
			// There is nothing to send, so the pipeline makes no call for it.
			continue
		}
		key := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, frags.sig)
		if _, seen := frags.seen[key]; seen {
			continue
		}
		hit, err := deps.State.HasDerivation(ctx, key)
		if err != nil {
			return fmt.Errorf("checking derivation cache for %s/%s: %w", cfg.Name, frag.Key, err)
		}
		frags.seen[key] = hit
		if est.Expected != nil {
			if err := expectedFragment(ctx, deps, rc.EstimateProfile, frag, frags, key, hit, est.Expected); err != nil {
				return err
			}
		}
		if hit {
			continue
		}
		chars, err := fragmentPromptChars(deps, frag, frags.maxChars)
		if err != nil {
			return err
		}
		est.FragmentDerivations++
		est.InputTokens += tokensForChars(chars)
		est.OutputTokens += rc.Pricing.MaxOutputTokens
	}

	for _, view := range cfg.Views {
		if len(view.DependsOn) > 0 && delta.ViewSkippable(changed, view.DependsOn) {
			continue
		}
		scoped := scopedBytes(rec, view.DependsOn)
		if scoped == 0 {
			continue
		}
		wrapper, err := viewPromptChars(deps, rec, view, 0)
		if err != nil {
			return err
		}
		est.ViewGenerations++
		est.InputTokens += tokensForChars(boundChars(scoped, cfg.ComposeMaxChars) + int(wrapper))
		est.OutputTokens += rc.Pricing.MaxOutputTokens
		if est.Expected != nil {
			if err := expectedView(deps, rc.EstimateProfile, rec, view, frags, est.Expected); err != nil {
				return err
			}
		}
	}
	return nil
}

// boundChars applies a character bound, where a bound of zero or less means unbounded. Both
// stages truncate their input, so pricing the untruncated size would quote tokens no call sends.
func boundChars(chars, max int) int {
	if max <= 0 {
		return chars
	}
	return min(chars, max)
}

// scopedBytes sums the sizes of the fragments a view depends on. An empty dependsOn depends
// on the whole item. Fragments with no stored content are skipped: composition drops them, so
// they are not part of any prompt.
func scopedBytes(rec *artifact.Record, dependsOn []string) int {
	total := 0
	for _, frag := range rec.Fragments {
		if frag.Blob == "" {
			continue
		}
		if len(dependsOn) == 0 || delta.MatchesAny(frag.Key, dependsOn) {
			total += int(frag.Bytes)
		}
	}
	return total
}

// tokensForChars converts a character count to an estimated token count.
func tokensForChars(chars int) int {
	return chars / charsPerToken
}
