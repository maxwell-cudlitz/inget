// Generation role of the OpenAI-compatible driver.
//
// Generate posts a single user message to /v1/chat/completions and returns the first
// choice plus token usage. Determinism comes from temperature 0 and a fixed seed, both
// covered by the signature so a change to either invalidates cached generations (D2, D4).
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// OpenAIGeneratorConfig configures the generator client. It mirrors the
// models.generator config block; Concurrency is deliberately absent because the pipeline,
// not the client, owns the worker pool.
type OpenAIGeneratorConfig struct {
	BaseURL         string
	Model           string
	APIKey          string
	Temperature     float64
	Seed            int
	MaxOutputTokens int
	MaxInputChars   int // 0 disables the check
	Timeout         time.Duration
	// RequestOptions are provider-specific fields merged into the request body, from
	// models.generator.request_options. See options.go.
	RequestOptions map[string]any
}

// openAIGenerator implements Generator over the /v1/chat/completions endpoint.
type openAIGenerator struct {
	client *http.Client
	cfg    OpenAIGeneratorConfig
	sig    string
}

// NewGenerator creates a Generator backed by an OpenAI-compatible completions API.
func NewGenerator(cfg OpenAIGeneratorConfig) Generator {
	g := &openAIGenerator{
		client: &http.Client{Timeout: cfg.Timeout},
		cfg:    cfg,
	}
	g.sig = g.buildSignature()
	return g
}

// Generate sends a single-message chat completion request and returns the response text.
//
// A prompt over MaxInputChars is rejected here rather than truncated. Truncating would
// silently drop the tail of a composed document and cache the degraded result under a
// key that claims the full input; the provider would reject it anyway, one round trip and
// one failed item later.
func (g *openAIGenerator) Generate(ctx context.Context, prompt string) (string, Usage, error) {
	if g.cfg.MaxInputChars > 0 {
		if n := utf8.RuneCountInString(prompt); n > g.cfg.MaxInputChars {
			return "", Usage{}, fmt.Errorf(
				"model: prompt is %d characters, over the %d allowed by models.generator.max_input_chars: "+
					"lower the datatype's compose.max_chars or shorten the prompt template",
				n, g.cfg.MaxInputChars)
		}
	}

	body := chatRequest{
		Model:       g.cfg.Model,
		Messages:    []chatMessage{{Role: "user", Content: prompt}},
		Temperature: g.cfg.Temperature,
		Seed:        g.cfg.Seed,
		MaxTokens:   g.cfg.MaxOutputTokens,
	}

	data, err := encodeRequest(body, g.cfg.RequestOptions)
	if err != nil {
		return "", Usage{}, fmt.Errorf("model: marshaling request: %w", err)
	}

	url := endpointURL(g.cfg.BaseURL, "/chat/completions")
	respBody, err := doWithRetry(ctx, g.client, url, g.cfg.APIKey, data)
	if err != nil {
		return "", Usage{}, err
	}
	defer func() { _ = respBody.Close() }()

	var resp chatResponse
	if err := json.NewDecoder(respBody).Decode(&resp); err != nil {
		return "", Usage{}, fmt.Errorf("model: decoding response from %s: %w", url, err)
	}

	if len(resp.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("model: %s returned no choices", url)
	}

	choice := resp.Choices[0]
	reasoning := resp.Usage.CompletionTokensDetails.ReasoningTokens
	if choice.FinishReason == "length" {
		return "", Usage{}, fmt.Errorf(
			"model: %s returned finish_reason \"length\": the output was truncated at "+
				"max_output_tokens=%d after %d reasoning tokens%s",
			url, g.cfg.MaxOutputTokens, reasoning, budgetAdvice(reasoning, g.cfg.MaxOutputTokens))
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return "", Usage{}, fmt.Errorf(
			"model: %s returned empty content after %d reasoning tokens: the model produced no "+
				"answer%s", url, reasoning, budgetAdvice(reasoning, g.cfg.MaxOutputTokens))
	}

	usage := Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		CacheHitTokens:   resp.Usage.PromptTokensDetails.CachedTokens,
		ReasoningTokens:  reasoning,
	}
	slog.Debug("model: generated",
		"model", g.cfg.Model,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"reasoning_tokens", usage.ReasoningTokens,
		"cache_hit_tokens", usage.CacheHitTokens)

	return choice.Message.Content, usage, nil
}

// budgetAdvice names the likely fix, which depends on where the allowance went. A thinking
// model that spent most of its budget reasoning cannot be fixed by a shorter prompt: the tokens
// were gone before it began answering.
func budgetAdvice(reasoningTokens, budget int) string {
	if budget > 0 && reasoningTokens*2 >= budget {
		return "; reasoning consumed the allowance — disable thinking through " +
			"models.generator.request_options or raise models.generator.max_output_tokens"
	}
	return "; ask the prompt for a shorter answer or raise models.generator.max_output_tokens"
}

// Signature returns a stable identifier covering model, temperature, seed and limits.
func (g *openAIGenerator) Signature() string {
	return g.sig
}

// buildSignature lists every request parameter that can change the output. The format is
// readable rather than hashed on purpose: the enricher signature in internal/delta hashes
// this string along with prompt bytes, so hashing twice would only cost debuggability.
//
// MaxInputChars is deliberately absent, and its absence is the same judgement that keeps BaseURL
// out: neither can change what the model returns. The input bound is a pre-flight guard — an
// oversized prompt is rejected here and never sent, so it produces no cached output to go stale,
// and two configurations differing only in that bound generate identically. Including it made
// every cached derivation depend on a limit that only decides whether a request is attempted,
// so raising the bound to fit more of a composed document re-derived a corpus for nothing.
// A bound that *truncates* is a different thing and does belong to a signature: see
// fragment_enricher.max_input_chars, which is carried by the fragment enricher's own.
func (g *openAIGenerator) buildSignature() string {
	parts := []string{
		"max_output_tokens=" + strconv.Itoa(g.cfg.MaxOutputTokens),
		"model=" + g.cfg.Model,
		"seed=" + strconv.Itoa(g.cfg.Seed),
		"temperature=" + strconv.FormatFloat(g.cfg.Temperature, 'f', -1, 64),
	}
	if rendered := renderRequestOptions(g.cfg.RequestOptions); rendered != "" {
		// Sorted into place rather than appended: the list is read as a stable rendering of
		// every parameter that can change output, and request options are one of them.
		parts = append(parts, "request_options="+rendered)
		sort.Strings(parts)
	}
	return "openai:" + strings.Join(parts, ",")
}
