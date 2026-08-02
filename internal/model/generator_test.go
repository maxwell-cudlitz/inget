package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

		resp := chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "world"}}},
			Usage:   chatUsageBlock{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
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

func TestGenerator429RetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		resp := chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
			Usage:   chatUsageBlock{TotalTokens: 1},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{
		BaseURL: srv.URL,
		Model:   "m",
		Timeout: 10 * time.Second,
	})

	text, _, err := g.Generate(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestGenerator5xxRetryThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(502)
			w.Write([]byte("bad gateway"))
			return
		}
		resp := chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "recovered"}}},
			Usage:   chatUsageBlock{TotalTokens: 2},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{
		BaseURL: srv.URL,
		Model:   "m",
		Timeout: 10 * time.Second,
	})

	text, _, err := g.Generate(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if text != "recovered" {
		t.Errorf("text = %q, want %q", text, "recovered")
	}
}

func TestGeneratorMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{
		BaseURL: srv.URL,
		Model:   "m",
		Timeout: 5 * time.Second,
	})

	_, _, err := g.Generate(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
}

func TestGeneratorNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatResponse{Choices: nil, Usage: chatUsageBlock{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{
		BaseURL: srv.URL,
		Model:   "m",
		Timeout: 5 * time.Second,
	})

	_, _, err := g.Generate(context.Background(), "test")
	if err == nil {
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
	}
	g1 := NewGenerator(cfg)
	g2 := NewGenerator(cfg)
	if g1.Signature() != g2.Signature() {
		t.Errorf("signatures differ: %s vs %s", g1.Signature(), g2.Signature())
	}

	cfg.Seed = 42
	g3 := NewGenerator(cfg)
	if g1.Signature() == g3.Signature() {
		t.Error("seed change did not change signature")
	}
}

func TestGenerator4xxNonRetryable(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{
		BaseURL: srv.URL,
		Model:   "m",
		Timeout: 5 * time.Second,
	})

	_, _, err := g.Generate(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}
