// Deterministic fake implementations of Generator and Embedder for CI.
//
// FakeGenerator produces a hash-derived string from the input prompt, and FakeEmbedder
// produces a hash-derived unit vector of the configured width. Both are deterministic
// across runs: identical inputs always produce identical outputs.
package model

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
)

// Compile-time proof that the fakes stay substitutable for the real clients.
var (
	_ Generator = (*FakeGenerator)(nil)
	_ Embedder  = (*FakeEmbedder)(nil)
)

// FakeGenerator returns deterministic text derived from the input prompt hash.
type FakeGenerator struct {
	model string
}

// NewFakeGenerator creates a FakeGenerator with the given model identifier.
func NewFakeGenerator(model string) *FakeGenerator {
	return &FakeGenerator{model: model}
}

// Generate returns a deterministic string derived from the SHA-256 of the prompt.
func (f *FakeGenerator) Generate(_ context.Context, prompt string) (string, Usage, error) {
	h := sha256.Sum256([]byte(prompt))
	text := fmt.Sprintf("fake-generation:%s", hex.EncodeToString(h[:16]))
	usage := Usage{
		PromptTokens:     len(prompt) / 4, // rough approximation
		CompletionTokens: len(text) / 4,
		TotalTokens:      (len(prompt) + len(text)) / 4,
	}
	return text, usage, nil
}

// Signature returns a stable identifier for the fake generator.
func (f *FakeGenerator) Signature() string {
	return "fake:" + f.model
}

// FakeEmbedder returns deterministic unit vectors derived from input text hashes.
type FakeEmbedder struct {
	model string
	dims  int
}

// NewFakeEmbedder creates a FakeEmbedder with the given model name and dimension count.
func NewFakeEmbedder(model string, dims int) *FakeEmbedder {
	return &FakeEmbedder{model: model, dims: dims}
}

// Embed returns one unit vector per text, deterministically derived from each text's
// SHA-256 hash. The vector is constructed by expanding the hash into float32 values and
// normalizing to unit length.
func (f *FakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vecs[i] = f.hashToVector(text)
	}
	return vecs, nil
}

// Model returns the configured model identifier.
func (f *FakeEmbedder) Model() string {
	return f.model
}

// Dims returns the configured vector width.
func (f *FakeEmbedder) Dims() int {
	return f.dims
}

// Signature returns a stable identifier for the fake embedder.
func (f *FakeEmbedder) Signature() string {
	return fmt.Sprintf("fake:%s,dimensions=%d", f.model, f.dims)
}

// hashToVector produces a deterministic unit vector from text. It uses chained SHA-256
// hashes to fill the required dimensions, then normalizes.
func (f *FakeEmbedder) hashToVector(text string) []float32 {
	vec := make([]float32, f.dims)
	h := sha256.Sum256([]byte(text))

	for i := range vec {
		// Every 8 floats, rehash to get fresh bytes (32 bytes / 4 bytes per float = 8).
		if i > 0 && i%8 == 0 {
			h = sha256.Sum256(h[:])
		}
		offset := (i % 8) * 4
		bits := binary.LittleEndian.Uint32(h[offset : offset+4])
		// Map uint32 to [-1, 1].
		vec[i] = float32(bits)/float32(math.MaxUint32)*2 - 1
	}

	// Normalize to unit length.
	var sum float64
	for _, x := range vec {
		sum += float64(x) * float64(x)
	}
	if sum > 0 {
		norm := float32(math.Sqrt(sum))
		for i := range vec {
			vec[i] /= norm
		}
	}

	return vec
}
