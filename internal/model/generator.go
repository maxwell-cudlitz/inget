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

	data, err := json.Marshal(body)
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
	if choice.FinishReason == "length" {
		return "", Usage{}, fmt.Errorf(
			"model: %s returned finish_reason \"length\": the output was truncated; "+
				"raise models.generator.max_output_tokens", url)
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return "", Usage{}, fmt.Errorf(
			"model: %s returned empty content: the model produced no text; "+
				"raise models.generator.max_output_tokens or check the prompt", url)
	}

	usage := Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		CacheHitTokens:   resp.Usage.PromptTokensDetails.CachedTokens,
	}
	slog.Debug("model: generated",
		"model", g.cfg.Model,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"cache_hit_tokens", usage.CacheHitTokens)

	return choice.Message.Content, usage, nil
}

// Signature returns a stable identifier covering model, temperature, seed and limits.
func (g *openAIGenerator) Signature() string {
	return g.sig
}

// buildSignature lists every request parameter that can change the output. The format is
// readable rather than hashed on purpose: the enricher signature in internal/delta hashes
// this string along with prompt bytes, so hashing twice would only cost debuggability.
func (g *openAIGenerator) buildSignature() string {
	parts := []string{
		"max_input_chars=" + strconv.Itoa(g.cfg.MaxInputChars),
		"max_output_tokens=" + strconv.Itoa(g.cfg.MaxOutputTokens),
		"model=" + g.cfg.Model,
		"seed=" + strconv.Itoa(g.cfg.Seed),
		"temperature=" + strconv.FormatFloat(g.cfg.Temperature, 'f', -1, 64),
	}
	return "openai:" + strings.Join(parts, ",")
}
