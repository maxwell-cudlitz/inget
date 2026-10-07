// Query-time settings are separate from indexing settings and artifact hashes.
// Defaults sit in the lowest configuration layer so explicit zero or empty values
// are rejected instead of being silently replaced during normalization.
package config

import "github.com/spf13/viper"

const (
	// DefaultRerankCandidates bounds the vector pool requested per datatype.
	DefaultRerankCandidates = 50
	// DefaultRerankMaxCandidateChars bounds each document supplied to the reranker.
	DefaultRerankMaxCandidateChars = 200
	// DefaultRerankPrompt is the shipped query ranking prompt.
	DefaultRerankPrompt = "prompts/query/rerank.tmpl"
	// MaxRerankCandidates prevents an accidentally unbounded query-time model request.
	MaxRerankCandidates = 1000
)

// Query configures optional processing of vector search results.
type Query struct {
	Rerank Rerank `mapstructure:"rerank"`
}

// Rerank configures LLM ranking after vector retrieval. It is disabled by default.
type Rerank struct {
	Enabled           bool   `mapstructure:"enabled"`
	Candidates        int    `mapstructure:"candidates"`
	MaxCandidateChars int    `mapstructure:"max_candidate_chars"`
	Prompt            string `mapstructure:"prompt"`
}

// setQueryDefaults leaves explicit file and environment values above the defaults.
func setQueryDefaults(v *viper.Viper) {
	v.SetDefault("query.rerank.candidates", DefaultRerankCandidates)
	v.SetDefault("query.rerank.max_candidate_chars", DefaultRerankMaxCandidateChars)
	v.SetDefault("query.rerank.prompt", DefaultRerankPrompt)
}

// validateQuery checks limits even when ranking is disabled, catching dormant typos.
func (c *Config) validateQuery(v *validator) {
	r := c.Query.Rerank
	v.positive("query.rerank.candidates", int64(r.Candidates))
	if r.Candidates > MaxRerankCandidates {
		v.failf("query.rerank.candidates = %d, want at most %d", r.Candidates, MaxRerankCandidates)
	}
	v.positive("query.rerank.max_candidate_chars", int64(r.MaxCandidateChars))
	v.required("query.rerank.prompt", r.Prompt)
}
