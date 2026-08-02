// Shared pieces of the OpenAI-compatible driver.
//
// One driver serves both model roles because every supported provider (Kimi K3,
// DeepSeek V4, TEI, Ollama, any hosted endpoint) speaks the same wire format:
// /v1/chat/completions and /v1/embeddings. The only differences are base_url, model and
// API key, so the roles share this file, retry.go and wire.go, and differ only in
// generator.go and embedder.go.
//
// Retry and backoff live in retry.go. Concurrency bounding is the caller's
// responsibility: the pipeline owns the worker pool, so a client here is a
// one-call-at-a-time object.
package model

import (
	"math"
	"strings"
)

// endpointURL joins a configured base URL with an endpoint path, tolerating a trailing
// slash on the base. The base is expected to already include the API version segment
// (for example http://localhost:4000/v1), because providers differ on whether they serve
// /v1 at all.
func endpointURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

// normalize returns a unit-length copy of v. A zero vector has no direction to preserve
// and is returned unchanged.
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
