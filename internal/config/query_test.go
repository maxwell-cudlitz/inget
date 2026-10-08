// Query configuration tests cover opt-in compatibility, complete role validation,
// environment and secret layering, and isolation from artifact hashes.
package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestQueryDefaultsPreserveExistingConfiguration(t *testing.T) {
	cfg := valid(t)
	want := Rerank{Candidates: 50, MaxCandidateChars: 200, Prompt: "prompts/query/rerank.tmpl"}
	if cfg.Query.Rerank != want {
		t.Errorf("query.rerank = %+v, want %+v", cfg.Query.Rerank, want)
	}
	if !reflect.DeepEqual(cfg.Models.Reranker, Generator{}) {
		t.Errorf("omitted reranker = %+v, want an absent optional role", cfg.Models.Reranker)
	}
	if cfg.Models.Generator.Model != "gen-1" || cfg.Models.Embedder.Model != "embed-1" {
		t.Error("query defaults changed the indexing models")
	}
}

func TestRerankEnabledRequiresItsOwnModel(t *testing.T) {
	cfg := valid(t)
	cfg.Query.Rerank.Enabled = true
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "models.reranker.model is required") {
		t.Fatalf("enabled reranking with no role: %v, want a reranker model error", err)
	}
	cfg.Models.Reranker = cfg.Models.Generator
	if err := cfg.Validate(); err != nil {
		t.Fatalf("complete reranker role: %v", err)
	}
}

func TestDeclaredRerankerIsValidatedWhileDisabled(t *testing.T) {
	for _, field := range []string{"driver", "model", "base_url"} {
		t.Run(field, func(t *testing.T) {
			cfg := valid(t)
			switch field {
			case "driver":
				cfg.Models.Reranker.Driver = "openai"
			case "model":
				cfg.Models.Reranker.Model = "ranker"
			case "base_url":
				cfg.Models.Reranker.BaseURL = "http://localhost:4000/v1"
			}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "models.reranker.max_output_tokens") {
				t.Fatalf("declared incomplete role: %v, want complete model validation", err)
			}
		})
	}
}

func TestQueryRejectsExplicitMalformedLimits(t *testing.T) {
	tests := []struct{ setting, want string }{
		{"candidates: 0", "query.rerank.candidates"},
		{"candidates: -1", "query.rerank.candidates"},
		{"candidates: 1001", "want at most 1000"},
		{"max_candidate_chars: 0", "query.rerank.max_candidate_chars"},
		{"max_candidate_chars: -1", "query.rerank.max_candidate_chars"},
		{"prompt: \"\"", "query.rerank.prompt is required"},
	}
	for _, tt := range tests {
		t.Run(tt.setting, func(t *testing.T) {
			base := fixture(t, "minimal.yaml")
			writeLocal(t, base, "query:\n  rerank:\n    "+tt.setting+"\n")
			_, err := Load(base)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load = %v, want an error mentioning %s", err, tt.want)
			}
		})
	}
}

func TestRerankerSharesGenerationValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Generator)
		want   string
	}{
		{"driver", func(g *Generator) { g.Driver = "" }, "driver is required"},
		{"endpoint", func(g *Generator) { g.BaseURL = "localhost:4000" }, "base_url"},
		{"model", func(g *Generator) { g.Model = "" }, "model is required"},
		{"temperature", func(g *Generator) { g.Temperature = 3 }, "temperature"},
		{"output", func(g *Generator) { g.MaxOutputTokens = 0 }, "max_output_tokens"},
		{"input", func(g *Generator) { g.MaxInputChars = 0 }, "max_input_chars"},
		{"concurrency", func(g *Generator) { g.Concurrency = 0 }, "concurrency"},
		{"timeout", func(g *Generator) { g.Timeout = 0 }, "timeout"},
		{"input price", func(g *Generator) { g.PricePerMTokIn = -1 }, "price_per_mtok_in"},
		{"output price", func(g *Generator) { g.PricePerMTokOut = -1 }, "price_per_mtok_out"},
		{"reserved option", func(g *Generator) { g.RequestOptions = map[string]any{"max_tokens": 10} }, "request_options"},
		{"empty option", func(g *Generator) { g.RequestOptions = map[string]any{"": 10} }, "request_options"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid(t)
			cfg.Query.Rerank.Enabled = true
			cfg.Models.Reranker = cfg.Models.Generator
			tt.mutate(&cfg.Models.Reranker)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "models.reranker."+tt.want) {
				t.Fatalf("Validate = %v, want a reranker %s error", err, tt.want)
			}
		})
	}
}

func TestRerankEnvironmentAndSecretReferences(t *testing.T) {
	base := fixture(t, "minimal.yaml")
	writeLocal(t, base, "models:\n  reranker:\n    driver: openai\n    base_url: http://localhost:4000/v1\n    model: rank-file\n    max_output_tokens: 128\n    max_input_chars: 4096\n    concurrency: 1\n    timeout: 30s\n")
	for key, value := range map[string]string{
		"query.rerank.enabled": "true", "query.rerank.candidates": "25",
		"query.rerank.max_candidate_chars": "300", "query.rerank.prompt": "prompts/custom-rank.tmpl",
		"models.reranker.model": "rank-env", "models.reranker.api_key_env": "INGET_TEST_RERANKER_KEY",
	} {
		if !slices.Contains(EnvKeys(), key) {
			t.Fatalf("EnvKeys is missing %s", key)
		}
		t.Setenv(EnvName(key), value)
	}
	t.Setenv("INGET_TEST_RERANKER_KEY", "rank-key-before-load")
	cfg := load(t, base)
	want := Rerank{Enabled: true, Candidates: 25, MaxCandidateChars: 300, Prompt: "prompts/custom-rank.tmpl"}
	if cfg.Query.Rerank != want || cfg.Models.Reranker.Model != "rank-env" {
		t.Errorf("effective query/model did not use environment overrides: %+v %s", cfg.Query, cfg.Models.Reranker.Model)
	}
	t.Setenv("INGET_TEST_RERANKER_KEY", "rank-key-after-load")
	secret, err := cfg.Secret(cfg.Models.Reranker.APIKeyEnv)
	if err != nil || secret != "rank-key-before-load" {
		t.Fatalf("reranker secret was not snapshotted at load: %v", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling configuration: %v", err)
	}
	if strings.Contains(string(encoded), "rank-key-before-load") {
		t.Error("marshaled config exposes the resolved reranker secret")
	}
}

func TestQueryChangesDoNotChangeArtifactHashes(t *testing.T) {
	cfg := valid(t)
	wantConfig, wantDomain := hashes(t, cfg)
	cfg.Query.Rerank = Rerank{Enabled: true, Candidates: 100, MaxCandidateChars: 300, Prompt: "other.tmpl"}
	cfg.Models.Reranker = cfg.Models.Generator
	cfg.Models.Reranker.Model = "query-only-model"
	cfg.Models.Reranker.RequestOptions = map[string]any{"thinking": map[string]any{"type": "disabled"}}
	gotConfig, gotDomain := hashes(t, cfg)
	if gotConfig != wantConfig || gotDomain != wantDomain {
		t.Error("query-only changes altered source/datatype artifact hashes")
	}
}
