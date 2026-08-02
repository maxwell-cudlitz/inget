// OpenAI-compatible HTTP client for generation and embedding.
//
// One implementation serves both roles because all supported providers (Kimi K3,
// DeepSeek V4, TEI, Ollama, any hosted endpoint) speak the same wire format:
// /v1/chat/completions and /v1/embeddings. The only differences are base_url, model,
// and API key.
//
// Retry is handled by doWithRetry in retry.go; concurrency bounding is the caller's
// responsibility via errgroup.SetLimit.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenAIGeneratorConfig configures the generator client.
type OpenAIGeneratorConfig struct {
	BaseURL         string
	Model           string
	APIKey          string
	Temperature     float64
	Seed            int
	MaxOutputTokens int
	MaxInputChars   int
	Concurrency     int
	Timeout         time.Duration
}

// OpenAIEmbedderConfig configures the embedder client.
type OpenAIEmbedderConfig struct {
	BaseURL      string
	Model        string
	APIKey       string
	Dimensions   int
	TruncateDims int
	BatchSize    int
	Concurrency  int
	Timeout      time.Duration
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
func (g *openAIGenerator) Generate(ctx context.Context, prompt string) (string, Usage, error) {
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

	url := strings.TrimRight(g.cfg.BaseURL, "/") + "/chat/completions"
	respBody, err := doWithRetry(ctx, g.client, url, g.cfg.APIKey, data)
	if err != nil {
		return "", Usage{}, err
	}
	defer respBody.Close()

	var resp chatResponse
	if err := json.NewDecoder(respBody).Decode(&resp); err != nil {
		return "", Usage{}, fmt.Errorf("model: decoding response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("model: response contains no choices")
	}

	usage := Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		CacheHitTokens:   resp.Usage.CacheHitTokens,
	}

	return resp.Choices[0].Message.Content, usage, nil
}

// Signature returns a stable hash covering model, temperature, seed, and limits.
func (g *openAIGenerator) Signature() string {
	return g.sig
}

func (g *openAIGenerator) buildSignature() string {
	parts := []string{
		"max_output_tokens=" + strconv.Itoa(g.cfg.MaxOutputTokens),
		"model=" + g.cfg.Model,
		"seed=" + strconv.Itoa(g.cfg.Seed),
		"temperature=" + strconv.FormatFloat(g.cfg.Temperature, 'f', -1, 64),
	}
	return "openai:" + strings.Join(parts, ",")
}

// openAIEmbedder implements Embedder over the /v1/embeddings endpoint.
type openAIEmbedder struct {
	client *http.Client
	cfg    OpenAIEmbedderConfig
	sig    string
}

// NewEmbedder creates an Embedder backed by an OpenAI-compatible embeddings API.
func NewEmbedder(cfg OpenAIEmbedderConfig) Embedder {
	e := &openAIEmbedder{
		client: &http.Client{Timeout: cfg.Timeout},
		cfg:    cfg,
	}
	e.sig = e.buildSignature()
	return e
}

// Embed sends a batch of texts and returns one vector per text. If TruncateDims is set,
// vectors are MRL-truncated and re-normalized to unit length.
func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var all [][]float32
	batchSize := e.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = len(texts)
	}

	for i := 0; i < len(texts); i += batchSize {
		end := min(i+batchSize, len(texts))
		batch := texts[i:end]

		vecs, err := e.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		all = append(all, vecs...)
	}

	return all, nil
}

// Model returns the configured model identifier.
func (e *openAIEmbedder) Model() string {
	return e.cfg.Model
}

// Dims returns the effective vector width (MRL truncation target or native).
func (e *openAIEmbedder) Dims() int {
	if e.cfg.TruncateDims > 0 {
		return e.cfg.TruncateDims
	}
	return e.cfg.Dimensions
}

// Signature returns a stable identifier covering model and effective dimensions.
func (e *openAIEmbedder) Signature() string {
	return e.sig
}

func (e *openAIEmbedder) buildSignature() string {
	parts := []string{
		"dimensions=" + strconv.Itoa(e.Dims()),
		"model=" + e.cfg.Model,
	}
	return "openai:" + strings.Join(parts, ",")
}

func (e *openAIEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body := embeddingRequest{
		Model: e.cfg.Model,
		Input: texts,
	}
	if e.cfg.TruncateDims > 0 {
		body.Dimensions = e.cfg.TruncateDims
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("model: marshaling embedding request: %w", err)
	}

	url := strings.TrimRight(e.cfg.BaseURL, "/") + "/embeddings"
	respBody, err := doWithRetry(ctx, e.client, url, e.cfg.APIKey, data)
	if err != nil {
		return nil, err
	}
	defer respBody.Close()

	var resp embeddingResponse
	if err := json.NewDecoder(respBody).Decode(&resp); err != nil {
		return nil, fmt.Errorf("model: decoding embedding response: %w", err)
	}

	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("model: expected %d embeddings, got %d", len(texts), len(resp.Data))
	}

	vecs := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		vec := d.Embedding
		if e.cfg.TruncateDims > 0 && len(vec) > e.cfg.TruncateDims {
			vec = vec[:e.cfg.TruncateDims]
		}
		vecs[i] = normalize(vec)
	}

	return vecs, nil
}

// normalize returns a unit-length vector. A zero vector is returned unchanged.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	norm := float32(math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}
