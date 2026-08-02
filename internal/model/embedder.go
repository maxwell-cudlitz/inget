// Embedding role of the OpenAI-compatible driver.
//
// Embed batches texts to /v1/embeddings and returns one unit vector per input, in input
// order. Repeated texts are sent once and fanned back out. Matryoshka (MRL) truncation to
// TruncateDims happens client-side and is followed by re-normalization, so the vector
// contract (D8) holds regardless of what the provider supports.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// OpenAIEmbedderConfig configures the embedder client. It mirrors the models.embedder
// config block; Concurrency is absent for the same reason as in the generator.
type OpenAIEmbedderConfig struct {
	BaseURL      string
	Model        string
	APIKey       string
	Dimensions   int // the model's native width
	TruncateDims int // MRL target width; 0 means native
	BatchSize    int
	Timeout      time.Duration
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

// Embed returns one vector per text, in the same order as texts. Batching is sequential:
// parallelism across batches belongs to the pipeline's worker pool.
//
// Repeated texts are sent once. Beyond saving tokens, that is a correctness fix: TEI 1.9.3 on
// Metal was measured returning a wrong — internally consistent but unrelated — vector for a
// text that appeared more than once in one request, nondeterministically, while the same text
// sent alone embedded correctly. Identical text must produce an identical vector, so the
// duplicate never reaches the server.
func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	unique, at := dedupe(texts)
	batchSize := e.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = len(unique)
	}

	embedded := make([][]float32, 0, len(unique))
	for i := 0; i < len(unique); i += batchSize {
		batch := unique[i:min(i+batchSize, len(unique))]

		vecs, err := e.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		embedded = append(embedded, vecs...)
	}

	// A repeated text gets its own copy of the vector, so one caller's slice cannot be
	// aliased by another's.
	all := make([][]float32, len(texts))
	handed := make([]bool, len(unique))
	for i, source := range at {
		if handed[source] {
			all[i] = slices.Clone(embedded[source])
			continue
		}
		handed[source] = true
		all[i] = embedded[source]
	}
	return all, nil
}

// dedupe returns the distinct texts in first-appearance order, and per input the index of its
// text in that list.
func dedupe(texts []string) ([]string, []int) {
	unique := make([]string, 0, len(texts))
	at := make([]int, len(texts))
	seen := make(map[string]int, len(texts))
	for i, text := range texts {
		index, ok := seen[text]
		if !ok {
			index = len(unique)
			seen[text] = index
			unique = append(unique, text)
		}
		at[i] = index
	}
	return unique, at
}

// Model returns the configured model identifier.
func (e *openAIEmbedder) Model() string {
	return e.cfg.Model
}

// Dims returns the effective vector width: the MRL truncation target when set, otherwise
// the model's native width. This is the width destinations bind their columns to.
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

// embedBatch sends one request and maps the response back onto the batch.
//
// Vectors are placed by the index each object reports rather than by arrival order. The
// OpenAI schema does not promise ordered data, and a mismatch here is invisible: every
// vector would be stored against the wrong view, with no error and no bad search result
// until someone noticed the answers were subtly wrong.
func (e *openAIEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	data, err := json.Marshal(embeddingRequest{Model: e.cfg.Model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("model: marshaling embedding request: %w", err)
	}

	url := endpointURL(e.cfg.BaseURL, "/embeddings")
	respBody, err := doWithRetry(ctx, e.client, url, e.cfg.APIKey, data)
	if err != nil {
		return nil, err
	}
	defer func() { _ = respBody.Close() }()

	var resp embeddingResponse
	if err := json.NewDecoder(respBody).Decode(&resp); err != nil {
		return nil, fmt.Errorf("model: decoding embedding response from %s: %w", url, err)
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("model: %s returned %d embeddings for %d inputs", url, len(resp.Data), len(texts))
	}

	vecs := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("model: %s returned embedding index %d, outside the %d inputs sent",
				url, d.Index, len(texts))
		}
		if vecs[d.Index] != nil {
			return nil, fmt.Errorf("model: %s returned two embeddings for index %d: the provider is not "+
				"reporting distinct index values, so results cannot be matched to inputs", url, d.Index)
		}
		vec, err := e.fit(d.Embedding)
		if err != nil {
			return nil, err
		}
		vecs[d.Index] = vec
	}

	slog.Debug("model: embedded", "model", e.cfg.Model, "inputs", len(texts), "dims", e.Dims())
	return vecs, nil
}

// fit truncates a returned vector to the configured width and re-normalizes it.
//
// Truncation is client-side rather than requested through the API's "dimensions"
// parameter: TEI and most self-hosted OpenAI-compatible servers do not implement that
// parameter, and for an MRL model, truncating a unit vector and re-normalizing is what
// the server-side implementation does anyway. Re-normalizing unconditionally also
// guarantees the unit-length contract when a provider returns un-normalized vectors.
func (e *openAIEmbedder) fit(vec []float32) ([]float32, error) {
	if e.cfg.TruncateDims > 0 && len(vec) > e.cfg.TruncateDims {
		vec = vec[:e.cfg.TruncateDims]
	}
	if want := e.Dims(); len(vec) != want {
		return nil, fmt.Errorf("model: %s returned a %d-dimension vector, want %d: correct "+
			"models.embedder.dimensions, or set truncate_dims to the width you want stored",
			e.cfg.Model, len(vec), want)
	}
	return normalize(vec), nil
}
