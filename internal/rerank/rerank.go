// Package rerank orders retrieved candidates through the existing OpenAI-compatible
// generator. It performs no embedding or persistence: an order is accepted only when
// the model returns a complete permutation of the supplied candidate IDs.
package rerank

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/model"
)

// Ranker combines a chat generator with a disk-backed ranking prompt.
type Ranker struct {
	gen      model.Generator
	prompt   *enrich.PromptRenderer
	maxChars int
}

// New creates a ranker whose candidate previews are bounded in Unicode characters.
// The generator enforces the overall prompt bound; it never silently truncates it.
func New(gen model.Generator, prompt *enrich.PromptRenderer, maxCandidateChars int) *Ranker {
	return &Ranker{gen: gen, prompt: prompt, maxChars: maxCandidateChars}
}

// Order returns zero-based indices into candidates, most relevant first. Candidate
// IDs in the wire response are one-based. Errors leave fallback policy to the caller.
func (r *Ranker) Order(ctx context.Context, query string, candidates []string) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reranking: %w", err)
	}
	if len(candidates) < 2 {
		order := make([]int, len(candidates))
		return order, nil
	}
	if r.maxChars <= 0 {
		return nil, fmt.Errorf("reranking: candidate character bound must be positive")
	}
	type candidate struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
	}
	data := struct {
		Query      string      `json:"query"`
		Candidates []candidate `json:"candidates"`
	}{Query: query}
	for i, text := range candidates {
		preview := []rune(text)
		if len(preview) > r.maxChars {
			preview = preview[:r.maxChars]
		}
		data.Candidates = append(data.Candidates, candidate{ID: i + 1, Text: string(preview)})
	}
	// json.Marshal escapes angle brackets, so input cannot close the prompt's data
	// delimiters. Template syntax inside an input string is never executed.
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encoding rerank candidates: %w", err)
	}
	prompt, err := r.prompt.Render(enrich.TemplateData{Document: string(encoded)})
	if err != nil {
		return nil, fmt.Errorf("rendering rerank prompt: %w", err)
	}
	answer, _, err := r.gen.Generate(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("generating rerank order: %w", err)
	}
	return parseOrder(answer, len(candidates))
}

// parseOrder accepts exactly one JSON object with every candidate ID once. It never
// extracts numbers from prose or logs model output, which can repeat source content.
func parseOrder(answer string, count int) ([]int, error) {
	var response struct {
		Ranking []int `json:"ranking"`
	}
	decoder := json.NewDecoder(strings.NewReader(answer))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decoding rerank JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("rerank response contains trailing data")
	}
	if len(response.Ranking) != count {
		return nil, fmt.Errorf("rerank returned %d IDs, want %d", len(response.Ranking), count)
	}
	seen := make([]bool, count)
	order := make([]int, count)
	for i, id := range response.Ranking {
		if id < 1 || id > count {
			return nil, fmt.Errorf("rerank returned an out-of-range candidate ID")
		}
		if seen[id-1] {
			return nil, fmt.Errorf("rerank returned a duplicate candidate ID")
		}
		seen[id-1] = true
		order[i] = id - 1
	}
	return order, nil
}
