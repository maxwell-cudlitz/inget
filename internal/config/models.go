// Model-role configuration. Query reranking has its own optional generation settings
// so changing retrieval behaviour never changes indexing model signatures.
package config

// Models holds generation, embedding and optional query reranking roles.
type Models struct {
	Generator Generator `mapstructure:"generator"`
	Embedder  Embedder  `mapstructure:"embedder"`
	Reranker  Generator `mapstructure:"reranker"`
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
