// What a run would spend, reported before it spends it.
//
// `inget plan` exists to make cost visible, so its legacy numbers form a conservative
// generation allowance. An exact count is not obtainable without doing the work: the level-2 guard
// compares a hash of the composed derivations, and the derivations are what the estimate is
// trying to price. So every view whose scope changed is counted as a generation even though
// some of them will turn out to compose to an unchanged document, every call is priced at the
// full output budget rather than the shorter completion it will probably return, and a view's
// input uses raw fragment sizes unless cache-only mode supplies actual cached summary
// lengths. Rendered prompt wrappers are included. Input is bounded by the
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

// surfaceSignatureChanges reports changed scopes. Expanding work to every live item is
// explicit because prompt and model changes can trigger a costly full re-enrichment.
func surfaceSignatureChanges(ctx context.Context, deps Deps, rc RunConfig, result *reconcileResult) error {
	changed, err := changedSignatures(ctx, deps)
	if err != nil {
		return err
	}
	if rc.RebuildOnSignatureChange {
		changed, err = includeUnrecordedSignatures(ctx, deps, changed)
		if err != nil {
			return err
		}
	}
	result.plan.Estimate.ChangedSignatures = changed
	if len(changed) > 0 {
		slog.WarnContext(ctx, "enricher signature changed; full rebuild is opt-in",
			"datatype", deps.Config.Name, "scopes", changed, "items_affected", result.plan.TotalItems,
			"rebuild_enabled", rc.RebuildOnSignatureChange)
	}
	if len(changed) == 0 || !rc.RebuildOnSignatureChange {
		return nil
	}
	if !result.fullScope {
		return fmt.Errorf("--rebuild-on-signature-change requires a full-scope artifact run for %s", deps.Config.Name)
	}
	candidates := make([]string, 0, len(result.records))
	for itemID := range result.records {
		candidates = append(candidates, itemID)
	}
	result.plan.WorkItems, result.partial = selectWork(candidates, rc)
	result.plan.Restricted = result.partial
	if result.partial {
		result.tombstones, result.plan.Tombstones, result.plan.Deleted = nil, nil, 0
	}
	result.rebuild = true
	slog.InfoContext(ctx, "rebuilding items for changed enricher signatures",
		"datatype", deps.Config.Name, "items", len(result.plan.WorkItems), "scopes", changed)
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
	if err := validateEstimateProfile(rc.EstimateProfile, deps, rc); err != nil {
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
		if err := estimateItem(ctx, deps, rc, rec, frags, result.rebuild, &est); err != nil {
			return err
		}
	}

	if est.Expected != nil {
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
func estimateItem(ctx context.Context, deps Deps, rc RunConfig, rec *artifact.Record, frags fragEstimate, forceViews bool, est *Estimate) error {
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
		var allowed []string
		if rc.CachedFragmentsOnly {
			allowed = rc.CachedFragmentSignatures
		}
		cachedKey, hit, err := cachedFragmentKey(ctx, deps.State, frag, frags.sig, allowed)
		if err != nil {
			return fmt.Errorf("checking derivation cache for %s/%s: %w", cfg.Name, frag.Key, err)
		}
		frags.seen[key] = hit
		if !hit {
			cachedKey = key
		}
		if est.Expected != nil {
			if err := expectedFragment(ctx, deps, rc.EstimateProfile, frag, frags, cachedKey, hit, rc.Pricing, est.Expected); err != nil {
				return err
			}
			frags.chars[key] = frags.chars[cachedKey]
		} else if rc.CachedFragmentsOnly && hit {
			frags.chars[key], err = cachedFragmentChars(ctx, deps, frag, cachedKey)
			if err != nil {
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
		est.addTokens(tokensForChars(chars), rc.Pricing)
	}

	for _, view := range cfg.Views {
		if !forceViews && len(view.DependsOn) > 0 && delta.ViewSkippable(changed, view.DependsOn) {
			continue
		}
		scoped := scopedBytes(rec, view.DependsOn)
		if rc.CachedFragmentsOnly {
			scoped = int(composedEstimateChars(rec, view.DependsOn, frags))
		}
		if scoped == 0 {
			continue
		}
		wrapper, err := viewPromptChars(deps, rec, view, 0)
		if err != nil {
			return err
		}
		est.ViewGenerations++
		est.addTokens(tokensForChars(boundChars(scoped, cfg.ComposeMaxChars)+int(wrapper)), rc.viewPricing())
		if est.Expected != nil {
			if err := expectedView(deps, rc.EstimateProfile, rec, view, frags, rc.viewPricing(), est.Expected); err != nil {
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

// scopedBytes sums stored fragments in a view's scope; empty dependsOn covers the whole item.
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
