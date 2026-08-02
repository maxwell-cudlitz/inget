// What a run would spend, reported before it spends it.
//
// `inget plan` exists to make cost visible, so the numbers here are deliberately upper
// bounds. An exact count is not obtainable without doing the work: the level-2 guard
// compares a hash of the composed derivations, and the derivations are what the estimate is
// trying to price. So every view whose scope changed is counted as a generation even though
// some of them will turn out to compose to an unchanged document, and the token counts use
// raw fragment sizes because derived sizes are not known yet.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/delta"
)

// charsPerToken converts characters to tokens for estimation. Four is the usual ratio for
// English prose and source code in byte-pair encodings; it is an estimate feeding an
// estimate, which is why the result is labelled an upper bound and not a quote.
const charsPerToken = 4

// Estimate is what a run would cost, as an upper bound.
type Estimate struct {
	FragmentDerivations int      `json:"fragment_derivations"`
	ViewGenerations     int      `json:"view_generations"`
	InputTokens         int      `json:"input_tokens"`
	OutputTokens        int      `json:"output_tokens"`
	CostUSD             float64  `json:"cost_usd"`
	ChangedSignatures   []string `json:"changed_signatures,omitempty"`
	UpperBound          bool     `json:"upper_bound"`
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

	var fragSig string
	if cfg.FragmentEnricher && deps.FragEnricher != nil {
		fragSig = deps.FragEnricher.Signature()
	}

	for _, itemID := range result.plan.WorkItems {
		rec, ok := result.records[itemID]
		if !ok {
			continue
		}
		if err := estimateItem(ctx, deps, rc, rec, fragSig, &est); err != nil {
			return err
		}
	}

	est.CostUSD = float64(est.InputTokens)/1e6*rc.Pricing.PerMTokIn +
		float64(est.OutputTokens)/1e6*rc.Pricing.PerMTokOut
	result.plan.Estimate = est
	return nil
}

// estimateItem adds one item's derivations and view generations to est.
func estimateItem(ctx context.Context, deps Deps, rc RunConfig, rec *artifact.Record, fragSig string, est *Estimate) error {
	cfg := deps.Config

	cached, err := deps.State.Fragments(ctx, cfg.Name, rec.ItemID)
	if err != nil {
		return fmt.Errorf("loading fragments for %s/%s: %w", cfg.Name, rec.ItemID, err)
	}
	changed := changedKeys(delta.Reconcile(fingerprints(cached), incoming(rec)))

	for _, frag := range rec.Fragments {
		if fragSig == "" {
			break // no fragment enrichment: raw content is composed, nothing is generated
		}
		key := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, fragSig)
		hit, err := deps.State.HasDerivation(ctx, key)
		if err != nil {
			return fmt.Errorf("checking derivation cache for %s/%s: %w", cfg.Name, frag.Key, err)
		}
		if hit {
			continue
		}
		est.FragmentDerivations++
		est.InputTokens += tokensForChars(int(frag.Bytes))
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
		if cfg.ComposeMaxChars > 0 {
			scoped = min(scoped, cfg.ComposeMaxChars)
		}
		est.ViewGenerations++
		est.InputTokens += tokensForChars(scoped)
		est.OutputTokens += rc.Pricing.MaxOutputTokens
	}
	return nil
}

// scopedBytes sums the sizes of the fragments a view depends on. An empty dependsOn depends
// on the whole item.
func scopedBytes(rec *artifact.Record, dependsOn []string) int {
	total := 0
	for _, frag := range rec.Fragments {
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

// currentSignatures returns the signature of every enricher scope this datatype uses, keyed
// by the scope name recorded in state.
func currentSignatures(deps Deps) map[string]string {
	sigs := make(map[string]string, len(deps.Enrichers)+1)
	for name, enricher := range deps.Enrichers {
		sigs[deps.Config.Name+":view:"+name] = enricher.Signature()
	}
	if deps.Config.FragmentEnricher && deps.FragEnricher != nil {
		sigs[deps.Config.Name+":fragment"] = deps.FragEnricher.Signature()
	}
	return sigs
}

// changedSignatures returns the scopes whose recorded signature differs from the current
// one, sorted. A scope that has never been recorded is not a change: everything is new once.
func changedSignatures(ctx context.Context, deps Deps) ([]string, error) {
	var changed []string
	for scope, sig := range currentSignatures(deps) {
		previous, err := deps.State.Signature(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("reading recorded signature for %s: %w", scope, err)
		}
		if previous != "" && previous != sig {
			changed = append(changed, scope)
		}
	}
	sort.Strings(changed)
	return changed, nil
}
