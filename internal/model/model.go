// Package model provides Generator and Embedder clients for OpenAI-compatible APIs.
//
// One driver implementation ("openai") serves both generation and embedding because all
// supported providers speak the same wire format (D10). Fakes that produce deterministic
// output from input hashes are included for CI, where no network or credentials are
// available.
//
// Interfaces are defined here, consumed by internal/enrich and internal/pipeline.
package model

import "context"

// Generator produces text from a prompt via an OpenAI-compatible chat completions API.
type Generator interface {
	Generate(ctx context.Context, prompt string) (text string, usage Usage, err error)
	Signature() string
}

// Embedder produces dense vectors from text via an OpenAI-compatible embeddings API.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
	Dims() int
	Signature() string
}

// Usage reports token consumption for cost accounting.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheHitTokens   int
}
