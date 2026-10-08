// Explicit, measured cost profiles for read-only planning.
//
// Profiles describe observed provider usage under exact enricher signatures; they do not
// control generation or participate in cache keys. Changing a prompt or model requires a
// fresh matching profile rather than silently extrapolating old measurements.
package pipeline

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// EstimateProfile supplies an optional stage model for one datatype.
type EstimateProfile struct {
	Version  int                     `json:"version"`
	Datatype string                  `json:"datatype"`
	Fragment *StageProfile           `json:"fragment,omitempty"`
	Views    map[string]StageProfile `json:"views"`
}

// StageProfile fits input tokens to complete prompt characters and averages output size.
// PromptChars is only a fallback for non-LLM enrichers without a prompt renderer.
type StageProfile struct {
	Signature           string  `json:"signature"`
	Samples             int     `json:"samples"`
	InputTokensPerChar  float64 `json:"input_tokens_per_char"`
	InputTokenIntercept float64 `json:"input_token_intercept"`
	OutputTokens        float64 `json:"output_tokens"`
	OutputChars         float64 `json:"output_chars"`
	PromptChars         float64 `json:"prompt_chars"`
}

// ExpectedEstimate is a calibrated approximation for the plan's remaining generation work.
// It does not predict retries, embedding charges or a billing guarantee.
type ExpectedEstimate struct {
	InputTokens     float64        `json:"input_tokens"`
	OutputTokens    float64        `json:"output_tokens"`
	CostUSD         float64        `json:"cost_usd"`
	Approximate     bool           `json:"approximate"`
	FragmentSamples int            `json:"fragment_samples"`
	ViewSamples     map[string]int `json:"view_samples"`
	Exclusions      []string       `json:"exclusions"`
}

func validateEstimateProfile(p *EstimateProfile, deps Deps, pricing Pricing) error {
	if p == nil {
		return nil
	}
	if p.Version != 1 || p.Datatype != deps.Config.Name {
		return fmt.Errorf("estimate profile must be version 1 for datatype %s", deps.Config.Name)
	}
	fragmentOn := deps.Config.FragmentEnricher && deps.FragEnricher != nil
	if fragmentOn {
		if p.Fragment == nil {
			return fmt.Errorf("estimate profile requires a fragment stage")
		}
		if err := validateStageProfile("fragment", *p.Fragment, deps.FragEnricher.Signature(), pricing); err != nil {
			return err
		}
		if p.Fragment.OutputChars <= 0 {
			return fmt.Errorf("estimate profile fragment.output_chars must be positive")
		}
	} else if p.Fragment != nil {
		return fmt.Errorf("estimate profile has a fragment stage while enrichment is disabled")
	}
	if len(p.Views) != len(deps.Config.Views) {
		return fmt.Errorf("estimate profile must contain exactly the configured views")
	}
	for _, view := range deps.Config.Views {
		stage, ok := p.Views[view.Name]
		enricher := deps.Enrichers[view.Name]
		if !ok || enricher == nil {
			return fmt.Errorf("estimate profile requires configured view %s", view.Name)
		}
		if err := validateStageProfile("view "+view.Name, stage, enricher.Signature(), pricing); err != nil {
			return err
		}
	}
	return nil
}

func validateStageProfile(name string, stage StageProfile, signature string, pricing Pricing) error {
	if stage.Signature != signature || stage.Signature == "" {
		return fmt.Errorf("estimate profile %s signature does not match the current enricher", name)
	}
	if stage.Samples < 1 {
		return fmt.Errorf("estimate profile %s requires at least one sample", name)
	}
	for _, value := range []float64{stage.InputTokensPerChar, stage.InputTokenIntercept,
		stage.OutputTokens, stage.OutputChars, stage.PromptChars} {
		if !finiteNonNegative(value) {
			return fmt.Errorf("estimate profile %s values must be finite and nonnegative", name)
		}
	}
	if pricing.MaxOutputTokens > 0 && stage.OutputTokens > float64(pricing.MaxOutputTokens) {
		return fmt.Errorf("estimate profile %s output exceeds the configured output budget", name)
	}
	return nil
}

func newExpectedEstimate(p *EstimateProfile) *ExpectedEstimate {
	expected := &ExpectedEstimate{Approximate: true, ViewSamples: make(map[string]int),
		Exclusions: []string{"embedding", "retries"}}
	if p.Fragment != nil {
		expected.FragmentSamples = p.Fragment.Samples
	}
	for name, stage := range p.Views {
		expected.ViewSamples[name] = stage.Samples
	}
	return expected
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (e *ExpectedEstimate) add(stage StageProfile, chars float64) {
	e.InputTokens += stage.InputTokenIntercept + stage.InputTokensPerChar*chars
	e.OutputTokens += stage.OutputTokens
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
