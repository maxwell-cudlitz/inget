// Validation tests. Each case starts from a valid configuration, breaks exactly one
// thing, and asserts that Validate names it. The fixture loading successfully is itself
// the positive case.
package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// valid returns a freshly loaded, valid configuration for a case to mutate.
func valid(t *testing.T) *Config {
	t.Helper()
	return load(t, filepath.Join("testdata", "minimal.yaml"))
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"unsupported version", func(c *Config) { c.Version = 2 }, "version = 2"},
		{"bad log level", func(c *Config) { c.Log.Level = "trace" }, "log: invalid log level"},
		{"bad log format", func(c *Config) { c.Log.Format = "logfmt" }, "log: invalid log format"},
		{"bad log destination", func(c *Config) { c.Log.Destination = "/tmp/x.log" }, "log: invalid log destination"},

		{"artifacts url missing", func(c *Config) { c.Artifacts.URL = "" }, "artifacts.url is required"},
		{"artifacts url scheme", func(c *Config) { c.Artifacts.URL = "ftp://host/x" }, "artifacts.url scheme"},
		{"shard size", func(c *Config) { c.Artifacts.ShardTargetBytes = 0 }, "artifacts.shard_target_bytes"},
		{"blob size", func(c *Config) { c.Artifacts.BlobMaxBytes = -1 }, "artifacts.blob_max_bytes"},
		{"fragment cap", func(c *Config) { c.Artifacts.MaxFragmentsPerItem = 0 }, "artifacts.max_fragments_per_item"},
		{"compression", func(c *Config) { c.Artifacts.Compression = "gzip" }, "artifacts.compression"},

		{"retention runs", func(c *Config) { c.Retention.Runs = 0 }, "retention.runs"},
		{"retention missing runs", func(c *Config) { c.Retention.MissingRuns = 0 }, "retention.missing_runs"},

		{"reference depth", func(c *Config) { c.Enrich.MaxReferenceDepth = -1 }, "enrich.max_reference_depth"},
		{"cascade cap", func(c *Config) { c.Enrich.MaxCascadePerRun = 0 }, "enrich.max_cascade_per_run"},

		{"state driver", func(c *Config) { c.State.Driver = "mysql" }, "state.driver"},
		{"postgres needs dsn", func(c *Config) { c.State.Driver, c.State.DSNEnv = "postgres", "" }, "state.dsn_env is required"},
		{"sqlite needs path", func(c *Config) { c.State.Path = "" }, "state.path is required"},

		{"generator driver", func(c *Config) { c.Models.Generator.Driver = "" }, "models.generator.driver is required"},
		{"generator base url", func(c *Config) { c.Models.Generator.BaseURL = "localhost:4000" }, "models.generator.base_url"},
		{"generator model", func(c *Config) { c.Models.Generator.Model = "" }, "models.generator.model is required"},
		{"generator temperature", func(c *Config) { c.Models.Generator.Temperature = 3 }, "models.generator.temperature"},
		{"generator output tokens", func(c *Config) { c.Models.Generator.MaxOutputTokens = 0 }, "models.generator.max_output_tokens"},
		{"generator input chars", func(c *Config) { c.Models.Generator.MaxInputChars = 0 }, "models.generator.max_input_chars"},
		{"generator concurrency", func(c *Config) { c.Models.Generator.Concurrency = 0 }, "models.generator.concurrency"},
		{"generator timeout", func(c *Config) { c.Models.Generator.Timeout = 0 }, "models.generator.timeout"},
		{"generator price", func(c *Config) { c.Models.Generator.PricePerMTokIn = -0.1 }, "models.generator.price_per_mtok_in"},

		{"embedder dimensions", func(c *Config) { c.Models.Embedder.Dimensions = 0 }, "models.embedder.dimensions"},
		{"embedder negative truncation", func(c *Config) { c.Models.Embedder.TruncateDims = -2 }, "models.embedder.truncate_dims"},
		{"embedder truncation widens", func(c *Config) { c.Models.Embedder.TruncateDims = 99 }, "exceeds dimensions"},
		{"embedder batch size", func(c *Config) { c.Models.Embedder.BatchSize = 0 }, "models.embedder.batch_size"},

		{"no destinations", func(c *Config) { c.Destinations = nil }, "destinations is required"},
		{"duplicate destinations", func(c *Config) { c.Destinations = append(c.Destinations, c.Destinations[0]) }, "more than one entry named"},
		{"destination dsn", func(c *Config) { c.Destinations[0].DSNEnv = "" }, "dsn_env is required"},
		{"destination table", func(c *Config) { c.Destinations[0].Table = "" }, "table is required"},
		{"destination storage", func(c *Config) { c.Destinations[0].Storage = "float8" }, "storage"},
		{"destination granularity", func(c *Config) { c.Destinations[0].Granularity = "view" }, "granularity"},
		{"destination batch size", func(c *Config) { c.Destinations[0].BatchSize = 0 }, "batch_size"},
		{"hnsw m", func(c *Config) { c.Destinations[0].HNSW.M = 0 }, "hnsw.m"},
		{"hnsw ef construction", func(c *Config) { c.Destinations[0].HNSW.EFConstruction = 0 }, "hnsw.ef_construction"},
		{"ef search", func(c *Config) { c.Destinations[0].EFSearch = 0 }, "ef_search"},
		{"dimensions exceed halfvec index", func(c *Config) { c.Models.Embedder.Dimensions = 4096 }, "indexes at most 4000 dimensions"},
		{"dimensions exceed vector index", func(c *Config) {
			c.Destinations[0].Storage = "vector"
			c.Models.Embedder.Dimensions = 3072
		}, "indexes at most 2000 dimensions"},

		{"no sources", func(c *Config) { c.Sources = nil }, "sources is required"},
		{"duplicate sources", func(c *Config) { c.Sources = append(c.Sources, c.Sources[0]) }, "more than one entry named"},
		{"source driver", func(c *Config) { c.Sources[0].Driver = "" }, "driver is required"},
		{"source api url", func(c *Config) { c.Sources[0].Auth.APIURL = "api.github.com" }, "auth.api_url"},
		{"source domain", func(c *Config) { c.Sources[0].Domain = nil }, "domain is required"},

		{"no datatypes", func(c *Config) { c.Datatypes = nil }, "datatypes is required"},
		{"duplicate datatypes", func(c *Config) { c.Datatypes = append(c.Datatypes, c.Datatypes[0]) }, "more than one entry named"},
		{"datatype enricher", func(c *Config) { c.Datatypes[0].Enricher = "" }, "enricher is required"},
		{"unknown source", func(c *Config) { c.Datatypes[0].Source = "gitlab" }, "which no source declares"},
		{"no destinations bound", func(c *Config) { c.Datatypes[0].Destinations = nil }, "destinations is required"},
		{"unknown destination", func(c *Config) { c.Datatypes[0].Destinations = []string{"elsewhere"} }, "which no destination declares"},
		{"fragment enricher prompt", func(c *Config) { c.Datatypes[0].FragmentEnricher.Prompt = "" }, "fragment_enricher.prompt is required"},
		{"fragment enricher chars", func(c *Config) { c.Datatypes[0].FragmentEnricher.MaxInputChars = 0 }, "fragment_enricher.max_input_chars"},
		{"compose order", func(c *Config) { c.Datatypes[0].Compose.Order = "alphabetical" }, "compose.order"},
		{"compose max chars", func(c *Config) { c.Datatypes[0].Compose.MaxChars = 0 }, "compose.max_chars"},
		{"drift threshold", func(c *Config) { threshold := 1.5; c.Datatypes[0].DriftThreshold = &threshold }, "drift_threshold"},
		{"metadata field key", func(c *Config) { c.Datatypes[0].MetadataFields = map[string]string{"": "x"} }, "metadata_fields key is required"},

		{"no views", func(c *Config) { c.Datatypes[0].Views = nil }, "views is required"},
		{"duplicate views", func(c *Config) { c.Datatypes[0].Views = append(c.Datatypes[0].Views, c.Datatypes[0].Views[0]) }, "more than one entry named"},
		{"view prompt", func(c *Config) { c.Datatypes[0].Views[0].Prompt = "" }, "prompt is required"},
		{"view depends_on", func(c *Config) { c.Datatypes[0].Views[0].DependsOn = nil }, "depends_on is required"},
		{"view glob syntax", func(c *Config) { c.Datatypes[0].Views[0].DependsOn = []string{"src/[a-"} }, "not a valid glob pattern"},

		{"reference resolver", func(c *Config) { c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.Resolver = "" })} }, "resolver is required"},
		{"reference key_from", func(c *Config) { c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.KeyFrom = "" })} }, "key_from is required"},
		{"reference inject_as", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.InjectAs = "vector" })}
		}, "inject_as"},
		{"reference fields", func(c *Config) { c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.Fields = nil })} }, "fields is required"},
		{"reference datatype", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.Datatype = "git/nothing" })}
		}, "which no datatype declares"},
		{"http reference endpoint", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.Resolver, r.Datatype = "http", "" })}
		}, "endpoint is required"},
		{"reference key_from glob syntax", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.KeyFrom = "column:[a-" })}
		}, "not a valid glob pattern"},
		{"reference key_regex syntax", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.KeyRegex = "([a-" })}
		}, "key_regex"},
		{"reference key_regex without a capture group", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.KeyRegex = `github\.com` })}
		}, "want exactly 1"},
		{"reference key_regex with two capture groups", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(func(r *Reference) { r.KeyRegex = `(a)/(b)` })}
		}, "want exactly 1"},
		{"duplicate references", func(c *Config) {
			c.Datatypes[0].References = []Reference{ref(nil), ref(nil)}
		}, "more than one entry named"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid(t)
			tt.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// ref builds a valid reference against the fixture's own datatype, optionally broken by
// the supplied mutator.
func ref(mutate func(*Reference)) Reference {
	r := Reference{
		Name:     "linked",
		Resolver: "inget",
		Datatype: "git/repo",
		KeyFrom:  "column:repo_url",
		Fields:   []string{"description"},
		InjectAs: "fragment",
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

// TestValidateReportsEveryProblem checks that a file with several mistakes reports all of
// them, so one fix-and-rerun cycle is enough.
func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := valid(t)
	cfg.Version = 7
	cfg.Artifacts.Compression = "gzip"
	cfg.Datatypes[0].Source = "absent"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want three problems")
	}
	for _, want := range []string{"version = 7", "artifacts.compression", "no source declares"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}
