// Enricher and model-client builders, plus plan reporting.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/enrich"
	"github.com/maxwellcudlitz/inget/internal/model"
	"github.com/maxwellcudlitz/inget/internal/pipeline"
)

// buildViewEnrichers creates an enricher per view based on the datatype's enricher type.
func buildViewEnrichers(cfg *config.Config, gen model.Generator, dt config.Datatype) (map[string]enrich.Enricher, error) {
	enrichers := make(map[string]enrich.Enricher, len(dt.Views))
	for _, view := range dt.Views {
		switch dt.Enricher {
		case "llm":
			prompt, err := enrich.LoadPrompt(view.Prompt)
			if err != nil {
				return nil, fmt.Errorf("loading prompt for view %s: %w", view.Name, err)
			}
			enrichers[view.Name] = enrich.NewLLMEnricher(gen, prompt, enrich.LLMConfig{
				// The input bound is compose.max_chars, which shapes the document before the
				// prompt sees it; the output bound is the generator's, passed here so that
				// generated text is length-checked before it is stored.
				MaxInputChars:   composeMaxChars(dt),
				MaxOutputTokens: cfg.Models.Generator.MaxOutputTokens,
				SchemaVersion:   1,
			})
		case "passthrough":
			enrichers[view.Name] = enrich.NewPassthrough()
		default:
			return nil, fmt.Errorf("unknown enricher type %q for datatype %s", dt.Enricher, dt.Name)
		}
	}
	return enrichers, nil
}

// buildFragmentEnricher creates the fragment-level enricher from config.
func buildFragmentEnricher(cfg *config.Config, gen model.Generator, dt config.Datatype) (*enrich.FragmentLLMEnricher, error) {
	prompt, err := enrich.LoadPrompt(dt.FragmentEnricher.Prompt)
	if err != nil {
		return nil, fmt.Errorf("loading fragment prompt for %s: %w", dt.Name, err)
	}
	return enrich.NewFragmentLLMEnricher(gen, prompt, enrich.FragmentEnricherConfig{
		MaxInputChars:   dt.FragmentEnricher.MaxInputChars,
		MaxOutputTokens: cfg.Models.Generator.MaxOutputTokens,
	}), nil
}

// selectDatatypes resolves the optional datatype argument to the set a command operates on:
// the named one, or every configured datatype when none is named.
func selectDatatypes(cfg *config.Config, args []string) ([]config.Datatype, error) {
	if len(args) == 0 {
		return cfg.Datatypes, nil
	}
	dt, ok := cfg.Datatype(args[0])
	if !ok {
		return nil, fmt.Errorf("unknown datatype: %s", args[0])
	}
	return []config.Datatype{*dt}, nil
}

// composeOrder returns the compose order, falling back to the documented default.
func composeOrder(dt config.Datatype) string {
	if dt.Compose.Order != "" {
		return dt.Compose.Order
	}
	return config.DefaultComposeOrder
}

// composeMaxChars returns the compose max chars, falling back to the documented default.
func composeMaxChars(dt config.Datatype) int {
	if dt.Compose.MaxChars > 0 {
		return dt.Compose.MaxChars
	}
	return config.DefaultComposeMaxChars
}

// buildGenerator creates the Generator from config.
func buildGenerator(cfg *config.Config) (model.Generator, error) {
	mc := cfg.Models.Generator
	apiKey, err := modelAPIKey(cfg, mc.APIKeyEnv, "generator")
	if err != nil {
		return nil, err
	}
	return model.NewGenerator(model.OpenAIGeneratorConfig{
		BaseURL:         mc.BaseURL,
		Model:           mc.Model,
		APIKey:          apiKey,
		Temperature:     mc.Temperature,
		Seed:            mc.Seed,
		MaxOutputTokens: mc.MaxOutputTokens,
		MaxInputChars:   mc.MaxInputChars,
		Timeout:         mc.Timeout.Duration(),
	}), nil
}

// buildEmbedder creates the Embedder from config.
func buildEmbedder(cfg *config.Config) (model.Embedder, error) {
	mc := cfg.Models.Embedder
	apiKey, err := modelAPIKey(cfg, mc.APIKeyEnv, "embedder")
	if err != nil {
		return nil, err
	}
	return model.NewEmbedder(model.OpenAIEmbedderConfig{
		BaseURL:      mc.BaseURL,
		Model:        mc.Model,
		APIKey:       apiKey,
		Dimensions:   mc.Dimensions,
		TruncateDims: mc.TruncateDims,
		BatchSize:    mc.BatchSize,
		Timeout:      mc.Timeout.Duration(),
	}), nil
}

// modelAPIKey resolves a model client's credential.
//
// An absent api_key_env is a valid configuration: a locally served embedder or generator
// needs no credential. Naming a variable that turns out to be empty is not — that is a
// misconfiguration whose only other symptom is a 401 after the run has started.
func modelAPIKey(cfg *config.Config, ref config.SecretRef, role string) (string, error) {
	if ref == "" {
		return "", nil
	}
	key, err := cfg.Secret(ref)
	if err != nil {
		return "", fmt.Errorf("resolving models.%s.api_key_env: %w", role, err)
	}
	return key, nil
}

// reportPlan writes the plan as JSON on stdout and a summary line to the log.
//
// Stdout carries data and stderr carries diagnostics, so `inget plan | jq` works while the
// human-readable summary still reaches a terminal.
func reportPlan(plan *pipeline.Plan) error {
	est := plan.Estimate
	slog.Info("plan computed",
		"datatype", plan.Datatype,
		"artifact_run_id", plan.ArtifactRunID,
		"total_items", plan.TotalItems,
		"added", plan.Added,
		"modified", plan.Modified,
		"deleted", plan.Deleted,
		"unchanged", plan.Unchanged,
		"invalidated", plan.Invalidated,
		"deferred", plan.Deferred,
		"work_items", len(plan.WorkItems),
		"tombstones", len(plan.Tombstones),
		"fragment_derivations", est.FragmentDerivations,
		"view_generations", est.ViewGenerations,
		"input_tokens", est.InputTokens,
		"output_tokens", est.OutputTokens,
		"cost_usd", est.CostUSD,
		"changed_signatures", est.ChangedSignatures)

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling plan: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
