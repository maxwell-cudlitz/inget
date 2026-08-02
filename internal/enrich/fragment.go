// Fragment-level LLM enricher.
//
// FragmentLLMEnricher enriches individual fragment content before composition. It uses a
// separate prompt template that receives FragmentTemplateData and produces derived text
// that replaces the raw fragment content in the composed document.
package enrich

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/model"
)

// FragmentLLMEnricher enriches individual fragments before composition.
type FragmentLLMEnricher struct {
	gen    model.Generator
	prompt *PromptRenderer
	sig    string
	cfg    FragmentEnricherConfig
}

// FragmentEnricherConfig holds parameters for the fragment enricher signature.
type FragmentEnricherConfig struct {
	MaxInputChars   int
	MaxOutputTokens int
}

// NewFragmentLLMEnricher creates a fragment enricher with the given generator and prompt.
func NewFragmentLLMEnricher(gen model.Generator, prompt *PromptRenderer, cfg FragmentEnricherConfig) *FragmentLLMEnricher {
	sig := delta.BuildSignature(delta.SignatureInput{
		ModelID:         gen.Signature(),
		PromptBytes:     prompt.Bytes(),
		MaxInputChars:   cfg.MaxInputChars,
		MaxOutputTokens: cfg.MaxOutputTokens,
		SchemaVersion:   1,
	})
	return &FragmentLLMEnricher{gen: gen, prompt: prompt, sig: sig, cfg: cfg}
}

// Enrich renders the fragment prompt, calls the generator, and validates the result.
func (e *FragmentLLMEnricher) Enrich(ctx context.Context, data FragmentTemplateData) (string, error) {
	prompt, err := e.prompt.Render(data)
	if err != nil {
		return "", fmt.Errorf("enriching fragment %s: %w", data.Key, err)
	}

	text, usage, err := e.gen.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("enriching fragment %s: %w", data.Key, err)
	}
	validated, err := validateOutput("fragment "+data.Key, text, e.cfg.MaxOutputTokens)
	if err != nil {
		return "", err
	}
	slog.DebugContext(ctx, "fragment enrichment complete",
		"key", data.Key,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens)
	return validated, nil
}

// Signature returns the fragment enricher's cache-invalidation signature.
func (e *FragmentLLMEnricher) Signature() string {
	return e.sig
}
