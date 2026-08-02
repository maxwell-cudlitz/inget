package model

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbedderHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.Input) != 2 {
			t.Fatalf("expected 2 inputs, got %d", len(req.Input))
		}

		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: []float32{1, 0, 0}, Index: 0},
				{Embedding: []float32{0, 1, 0}, Index: 1},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:    srv.URL,
		Model:      "test-embed",
		Dimensions: 3,
		Timeout:    5 * time.Second,
	})

	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	assertUnitVector(t, vecs[0])
	assertUnitVector(t, vecs[1])
}

func TestEmbedderTruncateAndRenormalize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Dimensions != 2 {
			t.Errorf("expected dimensions=2 in request, got %d", req.Dimensions)
		}
		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: []float32{3, 4, 99, 99}, Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:      srv.URL,
		Model:        "test-embed",
		Dimensions:   4,
		TruncateDims: 2,
		Timeout:      5 * time.Second,
	})

	if e.Dims() != 2 {
		t.Errorf("Dims() = %d, want 2", e.Dims())
	}

	vecs, err := e.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vecs))
	}
	if len(vecs[0]) != 2 {
		t.Errorf("vector length = %d, want 2", len(vecs[0]))
	}
	assertUnitVector(t, vecs[0])

	// [3,4] normalized is [0.6, 0.8].
	if math.Abs(float64(vecs[0][0])-0.6) > 0.001 {
		t.Errorf("vecs[0][0] = %f, want ~0.6", vecs[0][0])
	}
	if math.Abs(float64(vecs[0][1])-0.8) > 0.001 {
		t.Errorf("vecs[0][1] = %f, want ~0.8", vecs[0][1])
	}
}

func TestEmbedderBatching(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)

		data := make([]embeddingData, len(req.Input))
		for i := range data {
			data[i] = embeddingData{Embedding: []float32{1, 0}, Index: i}
		}
		resp := embeddingResponse{Data: data}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:    srv.URL,
		Model:      "test-embed",
		Dimensions: 2,
		BatchSize:  3,
		Timeout:    5 * time.Second,
	})

	// 7 texts with batch_size=3 should produce 3 calls (3+3+1).
	texts := []string{"a", "b", "c", "d", "e", "f", "g"}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 7 {
		t.Errorf("got %d vectors, want 7", len(vecs))
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("API calls = %d, want 3", got)
	}
}

func TestEmbedderMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:    srv.URL,
		Model:      "m",
		Dimensions: 3,
		Timeout:    5 * time.Second,
	})

	_, err := e.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
}

func TestEmbedderCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Data: []embeddingData{{Embedding: []float32{1, 0, 0}, Index: 0}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:    srv.URL,
		Model:      "m",
		Dimensions: 3,
		Timeout:    5 * time.Second,
	})

	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for count mismatch")
	}
}

func TestEmbedderEmptyInput(t *testing.T) {
	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:    "http://should-not-be-called",
		Model:      "m",
		Dimensions: 3,
		Timeout:    5 * time.Second,
	})

	vecs, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if vecs != nil {
		t.Errorf("expected nil, got %v", vecs)
	}
}

func TestEmbedderSignatureStability(t *testing.T) {
	cfg := OpenAIEmbedderConfig{
		BaseURL:    "http://example.com/v1",
		Model:      "Qwen/Qwen3-Embedding-0.6B",
		Dimensions: 1024,
	}
	e1 := NewEmbedder(cfg)
	e2 := NewEmbedder(cfg)
	if e1.Signature() != e2.Signature() {
		t.Errorf("signatures differ: %s vs %s", e1.Signature(), e2.Signature())
	}

	cfg.TruncateDims = 512
	e3 := NewEmbedder(cfg)
	if e1.Signature() == e3.Signature() {
		t.Error("truncate_dims change did not change signature")
	}
}

func assertUnitVector(t *testing.T, v []float32) {
	t.Helper()
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if math.Abs(norm-1.0) > 0.001 {
		t.Errorf("vector norm = %f, want 1.0", norm)
	}
}
