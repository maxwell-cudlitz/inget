// Model-role configuration. Optional view and query roles isolate their generation
// budgets and signatures from per-fragment derivations.
package config

import "reflect"

// Models holds fragment generation, embedding and optional view/query generation roles.
type Models struct {
	Generator             Generator `mapstructure:"generator"`
	ViewGenerator         Generator `mapstructure:"view_generator"`
	Embedder              Embedder  `mapstructure:"embedder"`
	Reranker              Generator `mapstructure:"reranker"`
	viewGeneratorDeclared bool
}

// HasViewGenerator reports whether a view role was declared, including an incomplete
// declaration that validation must reject rather than silently inheriting the fragment role.
func (m Models) HasViewGenerator() bool {
	return m.viewGeneratorDeclared || !reflect.ValueOf(m.ViewGenerator).IsZero()
}

// EffectiveViewGenerator preserves the shared generator for configurations that omit
// the optional role; a declared role supplies all settings rather than inheriting fields.
func (m Models) EffectiveViewGenerator() Generator {
	if m.HasViewGenerator() {
		return m.ViewGenerator
	}
	return m.Generator
}

// ModelClient is the transport-level configuration shared by all model roles.
type ModelClient struct {
	Driver      string    `mapstructure:"driver"`
	BaseURL     string    `mapstructure:"base_url"`
	Model       string    `mapstructure:"model"`
	APIKeyEnv   SecretRef `mapstructure:"api_key_env"`
	Concurrency int       `mapstructure:"concurrency"`
	Timeout     Duration  `mapstructure:"timeout"`
}

// Generator configures the text generation role, including the prices `inget plan` uses
// to estimate what a run will cost before it spends anything.
type Generator struct {
	ModelClient     `mapstructure:",squash"`
	Temperature     float64 `mapstructure:"temperature"`
	Seed            int     `mapstructure:"seed"`
	MaxOutputTokens int     `mapstructure:"max_output_tokens"`
	MaxInputChars   int     `mapstructure:"max_input_chars"`
	PricePerMTokIn  float64 `mapstructure:"price_per_mtok_in"`
	PricePerMTokOut float64 `mapstructure:"price_per_mtok_out"`
	// RequestOptions are merged into the chat completion request body, for the parameters a
	// specific provider adds to the OpenAI shape — DeepSeek's `thinking`, a reasoning effort,
	// a top_k. They feed the generator signature, because a parameter that changes output must
	// invalidate what was cached under the previous value. Keys this client sets itself
	// (model, messages, temperature, seed, max_tokens, stream) are rejected.
	RequestOptions map[string]any `mapstructure:"request_options"`
}

// Embedder configures the embedding role. TruncateDims is the Matryoshka target width;
// zero means the model's native Dimensions.
type Embedder struct {
	ModelClient  `mapstructure:",squash"`
	Dimensions   int `mapstructure:"dimensions"`
	TruncateDims int `mapstructure:"truncate_dims"`
	BatchSize    int `mapstructure:"batch_size"`
}
