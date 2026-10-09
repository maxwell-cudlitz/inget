// Model validation shares generation rules across indexing and optional query ranking.
// An omitted reranker remains valid for existing configurations; a declared or enabled
// role must be complete rather than inheriting an indexing model by accident.
package config

import "github.com/maxwell-cudlitz/inget/internal/model"

// validateModels checks model clients and the embedding truncation relationship.
func (c *Config) validateModels(v *validator) {
	v.generation("models.generator", c.Models.Generator)
	if c.Models.HasViewGenerator() {
		v.generation("models.view_generator", c.Models.ViewGenerator)
	}
	r := c.Models.Reranker
	if c.Query.Rerank.Enabled || r.Driver != "" || r.Model != "" || r.BaseURL != "" {
		v.generation("models.reranker", r)
	}

	e := c.Models.Embedder
	v.client("models.embedder", e.ModelClient)
	v.positive("models.embedder.dimensions", int64(e.Dimensions))
	v.positive("models.embedder.batch_size", int64(e.BatchSize))
	switch {
	case e.TruncateDims < 0:
		v.failf("models.embedder.truncate_dims = %d, want 0 for native width or a positive width", e.TruncateDims)
	case e.TruncateDims > e.Dimensions:
		v.failf("models.embedder.truncate_dims = %d exceeds dimensions = %d; truncation cannot widen a vector",
			e.TruncateDims, e.Dimensions)
	}
}

// generation validates every generation setting consistently for either model role.
func (v *validator) generation(path string, g Generator) {
	v.client(path, g.ModelClient)
	if g.Temperature < 0 || g.Temperature > 2 {
		v.failf("%s.temperature = %v, want between 0 and 2", path, g.Temperature)
	}
	v.positive(path+".max_output_tokens", int64(g.MaxOutputTokens))
	v.positive(path+".max_input_chars", int64(g.MaxInputChars))
	v.nonNegative(path+".price_per_mtok_in", g.PricePerMTokIn)
	v.nonNegative(path+".price_per_mtok_out", g.PricePerMTokOut)
	if err := model.ValidateRequestOptions(g.RequestOptions); err != nil {
		v.failf("%s.request_options: %s", path, err)
	}
}
