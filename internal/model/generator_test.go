// Tests for the generator client against an httptest server.
//
// Alongside the happy path these pin the things a reproducible generation depends on:
// temperature and seed are sent even when zero, an over-long prompt is refused rather than
// silently truncated, a response with no choices is an error, and nested usage details are
// decoded so cached-token accounting is not silently zero.
package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGeneratorHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
		}

		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "test-model" {
			t.Errorf("model = %s, want test-model", req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
			t.Errorf("unexpected messages: %+v", req.Messages)
		}

		writeJSON(t, w, chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "world"}}},
			Usage:   chatUsageBlock{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		})
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{
		BaseURL:         srv.URL,
		Model:           "test-model",
		APIKey:          "test-key",
		Temperature:     0,
		Seed:            1,
		MaxOutputTokens: 1024,
		Timeout:         5 * time.Second,
	})

	text, usage, err := g.Generate(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if text != "world" {
		t.Errorf("text = %q, want %q", text, "world")
	}
	if usage.PromptTokens != 5 || usage.CompletionTokens != 3 || usage.TotalTokens != 8 {
		t.Errorf("usage = %+v", usage)
	}
}

// Cache-hit tokens arrive in a nested object. Decoding raw JSON is the point of the test:
// a flat field with a dotted tag matches nothing and silently reports zero cache hits,
// which would make every `inget plan` estimate overstate cost.
func TestGeneratorCacheHitTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeString(t, w, `{
			"choices": [{"message": {"role": "assistant", "content": "cached"}}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 20,
				"total_tokens": 120,
				"prompt_tokens_details": {"cached_tokens": 64}
			}
		}`)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 5 * time.Second})

	_, usage, err := g.Generate(context.Background(), "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheHitTokens != 64 {
		t.Errorf("CacheHitTokens = %d, want 64", usage.CacheHitTokens)
	}
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", usage.PromptTokens)
	}
}

// A prompt over the limit fails locally, before spending a round trip.
func TestGeneratorPromptOverMaxInputChars(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(t, w, chatResponse{Choices: []chatChoice{{Message: chatMessage{Content: "x"}}}})
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{
		BaseURL:       srv.URL,
		Model:         "m",
		MaxInputChars: 10,
		Timeout:       5 * time.Second,
	})

	_, _, err := g.Generate(context.Background(), strings.Repeat("x", 11))
	if err == nil {
		t.Fatal("expected an error for a prompt over max_input_chars")
	}
	if !strings.Contains(err.Error(), "max_input_chars") {
		t.Errorf("error should name the setting to change, got: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("server calls = %d, want 0", got)
	}

	// Multi-byte characters count as one each, matching compose.max_chars.
	if _, _, err := g.Generate(context.Background(), strings.Repeat("日", 10)); err != nil {
		t.Errorf("10 runes should be within a 10-character limit, got: %v", err)
	}
}

func TestGeneratorMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeString(t, w, `{invalid json`)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 5 * time.Second})

	if _, _, err := g.Generate(context.Background(), "test"); err == nil {
		t.Fatal("expected error for malformed response")
	}
}

func TestGeneratorNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, chatResponse{Choices: nil, Usage: chatUsageBlock{}})
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 5 * time.Second})

	if _, _, err := g.Generate(context.Background(), "test"); err == nil {
		t.Fatal("expected error for no choices")
	}
}

func TestGeneratorSignatureStability(t *testing.T) {
	cfg := OpenAIGeneratorConfig{
		BaseURL:         "http://example.com/v1",
		Model:           "deepseek-v4-flash",
		Temperature:     0,
		Seed:            1,
		MaxOutputTokens: 1024,
		MaxInputChars:   120000,
	}
	g1 := NewGenerator(cfg)
	g2 := NewGenerator(cfg)
	if g1.Signature() != g2.Signature() {
		t.Errorf("signatures differ: %s vs %s", g1.Signature(), g2.Signature())
	}

	mutations := []struct {
		name   string
		mutate func(OpenAIGeneratorConfig) OpenAIGeneratorConfig
	}{
		{"seed", func(c OpenAIGeneratorConfig) OpenAIGeneratorConfig { c.Seed = 42; return c }},
		{"model", func(c OpenAIGeneratorConfig) OpenAIGeneratorConfig { c.Model = "kimi-k3"; return c }},
		{"temperature", func(c OpenAIGeneratorConfig) OpenAIGeneratorConfig { c.Temperature = 0.7; return c }},
		{"max_output_tokens", func(c OpenAIGeneratorConfig) OpenAIGeneratorConfig { c.MaxOutputTokens = 2048; return c }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			if NewGenerator(tt.mutate(cfg)).Signature() == g1.Signature() {
				t.Errorf("%s change did not change the signature", tt.name)
			}
		})
	}

	// The base URL is transport, not behavior: moving a proxy must not invalidate cache.
	moved := cfg
	moved.BaseURL = "http://other.example.com/v1"
	if NewGenerator(moved).Signature() != g1.Signature() {
		t.Error("base_url change altered the signature; it should not")
	}

	// Nor is the input bound behaviour. It rejects an oversized prompt before it is sent, so it
	// cannot have produced output that a later change would make stale — and if it were covered,
	// fitting more of a document into a view would re-derive every fragment in the corpus.
	rebounded := cfg
	rebounded.MaxInputChars = 8000
	if NewGenerator(rebounded).Signature() != g1.Signature() {
		t.Error("max_input_chars change altered the signature; it should not")
	}
}

// Seed 0 must reach the provider: omitting it would leave generation unpinned while the
// signature still claimed a fixed seed.
func TestGeneratorSendsZeroSeedAndTemperature(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, w, chatResponse{Choices: []chatChoice{{Message: chatMessage{Content: "x"}}}})
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Seed: 0, Timeout: 5 * time.Second})
	if _, _, err := g.Generate(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}

	seed, ok := body["seed"]
	if !ok {
		t.Fatal("request omitted seed")
	}
	if seed != float64(0) {
		t.Errorf("seed = %v, want 0", seed)
	}
	if _, ok := body["temperature"]; !ok {
		t.Error("request omitted temperature")
	}
}
