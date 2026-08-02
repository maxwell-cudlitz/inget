// Package enrich provides the interface and implementations for LLM-based text
// enrichment. An Enricher takes a composed document (or fragment) and produces a derived
// text representation suitable for embedding and search.
//
// Two implementations exist:
//   - LLMEnricher: calls a Generator with a prompt template to produce enriched text.
//   - Passthrough: returns input unchanged, for datatypes that need no generation.
//
// Everything an enricher reads is a field of the template data it is given, and every
// field of that data is covered by a cache key or a signature. That is the invariant the
// invalidation cascade rests on (D2): a prompt that could read something else would be a
// prompt whose output changes without any key changing.
package enrich

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// charsPerToken is the ratio used to turn a token budget into a character bound for output
// validation. It is deliberately generous: the check exists to catch a model that ignored
// its limit or returned a runaway repetition, not to second-guess a tokenizer.
const charsPerToken = 8

// Enricher generates a derived text from input. Implementations are safe for concurrent
// use.
type Enricher interface {
	// Enrich produces derived text from data. The composed document, item metadata and
	// view name are all fields of data; nothing else reaches the prompt.
	Enrich(ctx context.Context, data TemplateData) (string, error)

	// Signature returns a stable hash that changes whenever a parameter affecting output
	// changes. It feeds the invalidation cascade's cache keys.
	Signature() string
}

// TemplateData is the data passed to view prompt templates during enrichment.
type TemplateData struct {
	Metadata map[string]string
	Document string // the composed text, scoped to the view's depends_on globs
	ViewName string
}

// FragmentTemplateData is the data passed to fragment enrichment prompt templates.
//
// It carries no item metadata on purpose. A derivation is cached under the fragment key,
// the fragment fingerprint and the enricher signature (delta.FragmentCacheKey), so a
// prompt that could read the item it arrived with would produce output the cache key does
// not distinguish — two items holding the same file would share one summary written about
// whichever of them was processed first.
type FragmentTemplateData struct {
	Content string
	Key     string
}

// validateOutput checks generated text for shape and length before it is stored, and
// returns it trimmed.
//
// Trimming is part of the contract rather than a courtesy: the level-3 guard hashes this
// text, so leading whitespace a model added on one call and not the next would otherwise
// read as drift and pay for a re-embed. maxTokens <= 0 disables the length bound.
func validateOutput(what, text string, maxTokens int) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", fmt.Errorf("%s produced no text", what)
	}
	if maxTokens > 0 {
		if n, limit := utf8.RuneCountInString(trimmed), maxTokens*charsPerToken; n > limit {
			return "", fmt.Errorf("%s produced %d characters, over the %d allowed for a %d-token budget",
				what, n, limit, maxTokens)
		}
	}
	return trimmed, nil
}
