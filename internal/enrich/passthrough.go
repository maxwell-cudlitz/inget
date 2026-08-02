// Passthrough enricher — returns input unchanged.
//
// Used for datatypes whose enricher is "passthrough" or when no LLM enrichment is
// desired. The signature is a fixed string so the cache key stays stable unless the
// caller explicitly switches enricher types.
package enrich

import "context"

const passthroughSignature = "passthrough:v1"

// Passthrough returns input text as-is, performing no LLM calls.
type Passthrough struct{}

// NewPassthrough creates a passthrough enricher.
func NewPassthrough() *Passthrough {
	return &Passthrough{}
}

// Enrich returns the composed document unchanged.
func (p *Passthrough) Enrich(_ context.Context, data TemplateData) (string, error) {
	return data.Document, nil
}

// Signature returns a fixed identifier.
func (p *Passthrough) Signature() string {
	return passthroughSignature
}
