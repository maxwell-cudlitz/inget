// Fragment-level LLM enricher.
//
// FragmentLLMEnricher enriches individual fragment content before composition. It uses a
// separate prompt template that receives FragmentTemplateData and produces derived text
// that replaces the raw fragment content in the composed document.
//
// The per-fragment input bound is enforced here, at the only place that knows what is about to
// be sent. It is the pipeline's main cost control: fragment derivation is one call per file, so
// an unbounded file is an unbounded bill.
package enrich

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/model"
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

// fragmentSchemaVersion is the fragment derivation contract version. It is 2 because
// max_input_chars became a truncation rather than a value that only fed the signature:
// derivations cached under version 1 were produced from untruncated content and are not the
// output this enricher would produce now.
const fragmentSchemaVersion = 2

// NewFragmentLLMEnricher creates a fragment enricher with the given generator and prompt.
func NewFragmentLLMEnricher(gen model.Generator, prompt *PromptRenderer, cfg FragmentEnricherConfig) *FragmentLLMEnricher {
	sig := delta.BuildSignature(delta.SignatureInput{
		ModelID:         gen.Signature(),
		PromptBytes:     prompt.Bytes(),
		MaxInputChars:   cfg.MaxInputChars,
		MaxOutputTokens: cfg.MaxOutputTokens,
		SchemaVersion:   fragmentSchemaVersion,
	})
	return &FragmentLLMEnricher{gen: gen, prompt: prompt, sig: sig, cfg: cfg}
}

// MaxInputChars is the per-fragment input bound this enricher applies. `inget plan` reads it
// so that the estimate prices the content that will actually be sent.
func (e *FragmentLLMEnricher) MaxInputChars() int {
	return e.cfg.MaxInputChars
}

// Enrich renders the fragment prompt, calls the generator, and validates the result.
//
// Content over max_input_chars is truncated, not rejected. A summary of the first 8,000
// characters of a 1 MB file is a usable description of what the file is; an error is not, and
// paying to send the rest buys a summary of the same length.
func (e *FragmentLLMEnricher) Enrich(ctx context.Context, data FragmentTemplateData) (string, error) {
	if truncated, cut := truncateChars(data.Content, e.cfg.MaxInputChars); cut {
		slog.DebugContext(ctx, "fragment content truncated for enrichment",
			"key", data.Key, "max_input_chars", e.cfg.MaxInputChars)
		data.Content = truncated
	}
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
