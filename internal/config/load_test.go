// Tests for layering and decoding: precedence between the three layers, nested
// environment overrides, the documented inability to address list elements from the
// environment, strict rejection of unknown keys, and the defaults filled in for list
// elements that no layer can reach.
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixture copies a testdata config into a temp directory and returns its path, so a test
// can drop a config.local.yaml beside it without touching the repository.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), DefaultPath)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

// writeLocal writes the override layer beside base.
func writeLocal(t *testing.T, base, body string) {
	t.Helper()
	local := filepath.Join(filepath.Dir(base), LocalName)
	if err := os.WriteFile(local, []byte(body), 0o600); err != nil {
		t.Fatalf("writing local override: %v", err)
	}
}

// load fails the test if the configuration does not load.
func load(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s) returned error: %v", path, err)
	}
	return cfg
}

func TestLoadShippedConfig(t *testing.T) {
	cfg := load(t, filepath.Join("..", "..", DefaultPath))

	if cfg.Version != SupportedVersion {
		t.Errorf("version = %d, want %d", cfg.Version, SupportedVersion)
	}
	if got, want := cfg.Retention.Runs.Duration(), 30*Day; got != want {
		t.Errorf("retention.runs = %v, want %v", got, want)
	}
	if got, want := cfg.Models.Generator.Timeout.Duration(), 300*time.Second; got != want {
		t.Errorf("models.generator.timeout = %v, want %v", got, want)
	}
	if got := cfg.Models.Embedder.EffectiveDims(); got != 1024 {
		t.Errorf("embedder effective dimensions = %d, want 1024", got)
	}
	repo, ok := cfg.Datatype("github/repo")
	if !ok {
		t.Fatal("shipped config has no github/repo datatype")
	}
	if len(repo.Views) != 8 {
		t.Errorf("github/repo has %d views, want 8", len(repo.Views))
	}
	if _, ok := cfg.Source(repo.Source); !ok {
		t.Errorf("github/repo names source %q, which does not resolve", repo.Source)
	}
}

func TestListElementDefaultsAreFilledIn(t *testing.T) {
	cfg := load(t, filepath.Join("..", "..", DefaultPath))

	item, ok := cfg.Datatype("monday/item")
	if !ok {
		t.Fatal("shipped config has no monday/item datatype")
	}
	// monday/item declares no compose block; viper cannot supply defaults inside a list.
	if item.Compose.Order != DefaultComposeOrder || item.Compose.MaxChars != DefaultComposeMaxChars {
		t.Errorf("monday/item compose = %+v, want the documented defaults", item.Compose)
	}
}

// TestEvalDefaultsAndOverride covers both halves of the eval block's contract: a
// configuration that omits it gates on D14's numbers, and an environment override still
// reaches a threshold, since the whole block is scalars.
func TestEvalDefaultsAndOverride(t *testing.T) {
	base := fixture(t, "minimal.yaml") // declares no eval block

	cfg := load(t, base)
	if cfg.Eval.SampleSize != DefaultEvalSampleSize {
		t.Errorf("eval.sample_size = %d, want the default %d", cfg.Eval.SampleSize, DefaultEvalSampleSize)
	}
	if cfg.Eval.Thresholds.SelfRetrieval != DefaultSelfRetrieval {
		t.Errorf("eval.thresholds.self_retrieval = %v, want the default %v",
			cfg.Eval.Thresholds.SelfRetrieval, DefaultSelfRetrieval)
	}

	t.Setenv(EnvName("eval.thresholds.self_retrieval"), "0.5")
	t.Setenv(EnvName("eval.sample_size"), "5")
	cfg = load(t, base)
	if cfg.Eval.Thresholds.SelfRetrieval != 0.5 || cfg.Eval.SampleSize != 5 {
		t.Errorf("eval = %+v, want the environment values 0.5 and 5", cfg.Eval)
	}
}

func TestPrecedenceBaseLocalEnv(t *testing.T) {
	base := fixture(t, "minimal.yaml")

	if got := load(t, base).Models.Generator.Model; got != "gen-1" {
		t.Fatalf("base layer model = %q, want gen-1", got)
	}

	writeLocal(t, base, "models:\n  generator:\n    model: gen-local\n")
	if got := load(t, base).Models.Generator.Model; got != "gen-local" {
		t.Errorf("local layer model = %q, want gen-local; local must beat base", got)
	}

	t.Setenv(EnvName("models.generator.model"), "gen-env")
	if got := load(t, base).Models.Generator.Model; got != "gen-env" {
		t.Errorf("env layer model = %q, want gen-env; env must beat local", got)
	}
}

func TestNestedEnvOverridesAcrossTypes(t *testing.T) {
	base := fixture(t, "minimal.yaml")
	t.Setenv("INGET_ARTIFACTS__BLOB_MAX_BYTES", "4096")
	t.Setenv("INGET_MODELS__EMBEDDER__DIMENSIONS", "16")
	t.Setenv("INGET_MODELS__GENERATOR__TEMPERATURE", "0.7")
	t.Setenv("INGET_RETENTION__RUNS", "12h")
	t.Setenv("INGET_ENRICH__MAX_CASCADE_PER_RUN", "7")

	cfg := load(t, base)

	if cfg.Artifacts.BlobMaxBytes != 4096 {
		t.Errorf("artifacts.blob_max_bytes = %d, want 4096", cfg.Artifacts.BlobMaxBytes)
	}
	if cfg.Models.Embedder.Dimensions != 16 {
		t.Errorf("models.embedder.dimensions = %d, want 16", cfg.Models.Embedder.Dimensions)
	}
	if cfg.Models.Generator.Temperature != 0.7 {
		t.Errorf("models.generator.temperature = %v, want 0.7", cfg.Models.Generator.Temperature)
	}
	if got := cfg.Retention.Runs.Duration(); got != 12*time.Hour {
		t.Errorf("retention.runs = %v, want 12h", got)
	}
	if cfg.Enrich.MaxCascadePerRun != 7 {
		t.Errorf("enrich.max_cascade_per_run = %d, want 7", cfg.Enrich.MaxCascadePerRun)
	}
}

// TestListElementEnvOverrideUnsupported documents the viper limitation the design states
// rather than works around: list elements are not addressable by environment variable, so
// list-valued settings are overridden through config.local.yaml.
func TestListElementEnvOverrideUnsupported(t *testing.T) {
	base := fixture(t, "minimal.yaml")
	t.Setenv("INGET_SOURCES__0__NAME", "not-applied")

	cfg := load(t, base)

	if cfg.Sources[0].Name != "git" {
		t.Errorf("sources[0].name = %q, want git: list elements must not be env-overridable", cfg.Sources[0].Name)
	}
	// The same block is reachable through the local override layer, which replaces the
	// whole list rather than merging element by element.
	writeLocal(t, base, "sources:\n  - name: git\n    driver: github\n    domain:\n      orgs: [\"other\"]\n")
	overridden := load(t, base)
	if got := overridden.Sources[0].Domain["orgs"]; !reflect.DeepEqual(got, []any{"other"}) {
		t.Errorf("sources[0].domain.orgs = %v, want [other] from the local layer", got)
	}
	if got := overridden.Sources[0].Auth.TokenEnv; got != "" {
		t.Errorf("sources[0].auth.token_env = %q, want empty: overriding a list replaces the element", got)
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	base := fixture(t, "minimal.yaml")
	writeLocal(t, base, "artifacts:\n  shard_target_byte: 2048\n")

	_, err := Load(base)
	if err == nil {
		t.Fatal("Load with a misspelled key = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "shard_target_byte") {
		t.Errorf("error %q should name the unknown key", err)
	}
}

func TestMissingBaseFileIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load of a missing file = nil error, want failure")
	}
}

func TestMalformedYAMLIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultPath)
	if err := os.WriteFile(path, []byte("version: [1\n"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load of malformed YAML = nil error, want failure")
	}
}

func TestLoadLogToleratesMissingFileAndReadsBlock(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	opts, err := LoadLog(missing)
	if err != nil {
		t.Fatalf("LoadLog with no config file returned error: %v", err)
	}
	if opts.Level != "" || opts.Format != "" {
		t.Errorf("LoadLog with no config file = %+v, want empty options", opts)
	}

	base := fixture(t, "minimal.yaml")
	writeLocal(t, base, "log:\n  level: debug\n")
	t.Setenv("INGET_LOG__FORMAT", "text")

	opts, err = LoadLog(base)
	if err != nil {
		t.Fatalf("LoadLog returned error: %v", err)
	}
	if opts.Level != "debug" || opts.Format != "text" || opts.Destination != "stderr" {
		t.Errorf("LoadLog() = %+v, want level=debug format=text destination=stderr", opts)
	}
}
