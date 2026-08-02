// LLM-backed enricher implementation.
//
// LLMEnricher renders a prompt template with the provided data, sends it to a Generator,
// and returns the response. The enricher's signature covers the model, prompt bytes, and
// all generation parameters, so a change to any of them invalidates cached derivations.
package enrich

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/model"
)

// LLMEnricher enriches text by rendering a prompt template and calling a Generator.
type LLMEnricher struct {
	gen    model.Generator
	prompt *PromptRenderer
	sig    string
	cfg    LLMConfig
}

// LLMConfig holds parameters for the LLM enricher that feed into its signature.
type LLMConfig struct {
	MaxInputChars   int
	MaxOutputTokens int
	SchemaVersion   int
	Options         map[string]string
}

// NewLLMEnricher creates an enricher backed by the given generator and prompt template.
func NewLLMEnricher(gen model.Generator, prompt *PromptRenderer, cfg LLMConfig) *LLMEnricher {
	sig := delta.BuildSignature(delta.SignatureInput{
		ModelID:         gen.Signature(),
		PromptBytes:     prompt.Bytes(),
		MaxInputChars:   cfg.MaxInputChars,
		MaxOutputTokens: cfg.MaxOutputTokens,
		SchemaVersion:   cfg.SchemaVersion,
		EnricherOptions: cfg.Options,
	})
	return &LLMEnricher{gen: gen, prompt: prompt, sig: sig, cfg: cfg}
}

// Enrich renders the prompt template with data, calls the generator, and returns the
// validated response text.
func (e *LLMEnricher) Enrich(ctx context.Context, data TemplateData) (string, error) {
	prompt, err := e.prompt.Render(data)
	if err != nil {
		return "", fmt.Errorf("enriching view %s: %w", data.ViewName, err)
	}

	text, usage, err := e.gen.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("enriching view %s: %w", data.ViewName, err)
	}
	validated, err := validateOutput("view "+data.ViewName, text, e.cfg.MaxOutputTokens)
	if err != nil {
		return "", err
	}
	slog.DebugContext(ctx, "llm enrichment complete",
		"view", data.ViewName,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens)
	return validated, nil
}

// Signature returns the enricher's cache-invalidation signature.
func (e *LLMEnricher) Signature() string {
	return e.sig
}
