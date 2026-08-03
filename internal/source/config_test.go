// Tests for FromConfig, the one place a source token is read.
//
// The token is a secret named by configuration rather than a value in it, so the two failures worth
// pinning are an unknown source name and a named environment variable that is not set. Both must fail
// here, at the boundary, rather than as a 401 after a run has taken a lock.
package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/config"
)

// minimalConfig is a valid configuration with one source and one datatype reading it.
const minimalConfig = `
version: 1
log: {level: info, format: json, destination: stderr}
artifacts:
  url: "file://./.inget/artifacts"
  shard_target_bytes: 1048576
  blob_max_bytes: 65536
  max_fragments_per_item: 100
  compression: zstd
retention: {runs: 30d, missing_runs: 3}
enrich: {max_reference_depth: 2, max_cascade_per_run: 5000}
state: {driver: sqlite, path: ./.inget/state.db}
models:
  generator:
    driver: openai
    base_url: "http://localhost:4000/v1"
    model: m
    temperature: 0
    seed: 1
    max_output_tokens: 512
    max_input_chars: 120000
    concurrency: 2
    timeout: 60s
  embedder:
    driver: openai
    base_url: "http://localhost:8080/v1"
    model: e
    dimensions: 64
    batch_size: 8
    concurrency: 2
    timeout: 30s
destinations:
  - name: d
    driver: pgvector
    dsn_env: TEST_DSN
    table: inget_vectors
    storage: halfvec
    hnsw: {m: 16, ef_construction: 64}
    ef_search: 40
    granularity: item
    batch_size: 10
sources:
  - name: github
    driver: github
    auth:
      token_env: TEST_SOURCE_TOKEN
      api_url: "https://api.github.com"
    domain:
      orgs: ["acme"]
    limits:
      requests_per_second: 10
datatypes:
  - name: github/repo
    source: github
    enricher: llm
    destinations: [d]
    views:
      - name: role
        prompt: prompts/github/repo/role.tmpl
        depends_on: ["**"]
`

// load writes the configuration to a temporary file and loads it.
func load(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(minimalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading configuration: %v", err)
	}
	return cfg
}

func TestFromConfigResolvesTheToken(t *testing.T) {
	t.Setenv("TEST_SOURCE_TOKEN", "ghp-not-a-real-token")
	cfg := load(t)

	got, err := FromConfig(cfg, "github")
	if err != nil {
		t.Fatalf("FromConfig() = %v", err)
	}
	if got.Token != "ghp-not-a-real-token" {
		t.Errorf("Token = %q, want the environment value", got.Token)
	}
	if got.Driver != "github" || got.APIURL != "https://api.github.com" {
		t.Errorf("FromConfig() = %+v, want the configured driver and API URL", got)
	}
	if got.Domain["orgs"] == nil {
		t.Error("Domain is empty, so the driver has nothing to enumerate")
	}
	// The connector needs the blob cap to know where to split an oversized file.
	if got.FragmentMaxBytes != 65536 {
		t.Errorf("FragmentMaxBytes = %d, want artifacts.blob_max_bytes", got.FragmentMaxBytes)
	}
}

func TestFromConfigRequiresAToken(t *testing.T) {
	cfg := load(t)

	_, err := FromConfig(cfg, "github")
	if err == nil {
		t.Fatal("FromConfig() with an unset token variable = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "TEST_SOURCE_TOKEN") {
		t.Errorf("error %q should name the variable that is unset", err)
	}
	if strings.Contains(err.Error(), "ghp") {
		t.Error("the error mentions a token value")
	}
}

func TestFromConfigRejectsAnUnknownSource(t *testing.T) {
	t.Setenv("TEST_SOURCE_TOKEN", "token")
	if _, err := FromConfig(load(t), "gitlab"); err == nil {
		t.Fatal("FromConfig() with an unknown source = nil error, want failure")
	}
}
