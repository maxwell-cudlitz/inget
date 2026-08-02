// Package config loads the single root configuration read by both binaries.
//
// The schema in this file mirrors the "Configuration" section of docs/feature-design.md
// one key at a time; the shipped config.yaml at the repository root is the documented
// default instance of it. Layering, environment binding and validation live in load.go
// and validate.go, hashing in hash.go, and secret indirection in secret.go.
//
// Two deliberate shape choices:
//
//   - A source's `domain` and `limits` blocks are raw maps. Their keys differ per driver
//     (GitHub enumerates orgs and repos, Monday enumerates workspaces and boards), so
//     the connector that owns the driver decodes them with DecodeInto rather than this
//     package growing a case per source.
//   - Fields whose zero value is a meaningful setting and which also have a documented
//     default are pointers, so "absent" and "explicitly zero" stay distinguishable.
package config

// SupportedVersion is the only accepted value of the top-level `version` key. A bump
// means the schema changed incompatibly and old files must be migrated, not guessed at.
const SupportedVersion = 1

// Documented defaults for per-datatype settings. These live in code rather than in
// config.yaml alone because viper cannot address list elements, so a datatype that
// omits the key has no layer to inherit it from.
const (
	DefaultComposeOrder    = "tier"
	DefaultComposeMaxChars = 120000
)

// Config is the effective configuration after layering, decoding and validation.
type Config struct {
	Version      int           `mapstructure:"version"`
	Log          Log           `mapstructure:"log"`
	Artifacts    Artifacts     `mapstructure:"artifacts"`
	Retention    Retention     `mapstructure:"retention"`
	Enrich       Enrich        `mapstructure:"enrich"`
	State        State         `mapstructure:"state"`
	Models       Models        `mapstructure:"models"`
	Destinations []Destination `mapstructure:"destinations"`
	Sources      []Source      `mapstructure:"sources"`
	Datatypes    []Datatype    `mapstructure:"datatypes"`

	// secrets holds the values of every declared *_env variable, snapshotted at load.
	// It is unexported so that no marshaller can reach it and so that a mid-run
	// environment change cannot alter what the run is using. Read it with Secret.
	secrets map[string]string
}

// Log configures the process logger. The LOG_LEVEL, LOG_FORMAT and LOG_DESTINATION
// environment variables override these values inside internal/logging, so they win over
// their INGET_LOG__* equivalents.
type Log struct {
	Level       string `mapstructure:"level"`       // debug | info | warn | error
	Format      string `mapstructure:"format"`      // json | text
	Destination string `mapstructure:"destination"` // stderr | stdout
}

// Artifacts configures the blob store and shard sizing used by inget-fetch.
type Artifacts struct {
	URL                 string `mapstructure:"url"` // file://, s3:// or gs://
	ShardTargetBytes    int64  `mapstructure:"shard_target_bytes"`
	BlobMaxBytes        int64  `mapstructure:"blob_max_bytes"`
	MaxFragmentsPerItem int    `mapstructure:"max_fragments_per_item"`
	Compression         string `mapstructure:"compression"` // zstd | none
}

// Retention bounds how long artifact runs survive and how many runs a fragment may be
// missing from before its derivations are collectable.
type Retention struct {
	Runs        Duration `mapstructure:"runs"`
	MissingRuns int      `mapstructure:"missing_runs"`
}

// Enrich bounds reference resolution depth and the per-run invalidation cascade.
type Enrich struct {
	MaxReferenceDepth int `mapstructure:"max_reference_depth"`
	MaxCascadePerRun  int `mapstructure:"max_cascade_per_run"`
}

// State selects the state store driver and how to reach it.
type State struct {
	Driver string    `mapstructure:"driver"` // postgres | sqlite
	DSNEnv SecretRef `mapstructure:"dsn_env"`
	Path   string    `mapstructure:"path"` // sqlite only
}

// Models holds the two model roles. One driver serves both; see D10.
type Models struct {
	Generator Generator `mapstructure:"generator"`
	Embedder  Embedder  `mapstructure:"embedder"`
}

// ModelClient is the transport-level configuration shared by both model roles.
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
}

// Embedder configures the embedding role. TruncateDims is the Matryoshka target width;
// zero means the model's native Dimensions.
type Embedder struct {
	ModelClient  `mapstructure:",squash"`
	Dimensions   int `mapstructure:"dimensions"`
	TruncateDims int `mapstructure:"truncate_dims"`
	BatchSize    int `mapstructure:"batch_size"`
}

// Destination is one vector sink. The storage, table and HNSW settings are pgvector's
// vocabulary; a future driver may ignore them, but they are typed because the dimension
// bounds they imply are worth validating before a run starts.
type Destination struct {
	Name        string    `mapstructure:"name"`
	Driver      string    `mapstructure:"driver"`
	DSNEnv      SecretRef `mapstructure:"dsn_env"`
	Table       string    `mapstructure:"table"`
	Storage     string    `mapstructure:"storage"` // halfvec | vector
	HNSW        HNSW      `mapstructure:"hnsw"`
	EFSearch    int       `mapstructure:"ef_search"`
	Granularity string    `mapstructure:"granularity"` // item | fragment
	BatchSize   int       `mapstructure:"batch_size"`
}

// HNSW holds the index build parameters.
type HNSW struct {
	M              int `mapstructure:"m"`
	EFConstruction int `mapstructure:"ef_construction"`
}

// Source is one connector instance. Domain and Limits are driver-specific and stay raw;
// see the package comment.
type Source struct {
	Name   string         `mapstructure:"name"`
	Driver string         `mapstructure:"driver"`
	Auth   Auth           `mapstructure:"auth"`
	Domain map[string]any `mapstructure:"domain"`
	Limits map[string]any `mapstructure:"limits"`
}

// Auth locates a source's credential by environment variable name and pins its API
// surface. No credential value is ever stored here.
type Auth struct {
	TokenEnv   SecretRef `mapstructure:"token_env"`
	APIURL     string    `mapstructure:"api_url"`
	APIVersion string    `mapstructure:"api_version"`
}

// Datatype binds a source to an enricher, a set of views and one or more destinations.
type Datatype struct {
	Name             string            `mapstructure:"name"`
	Source           string            `mapstructure:"source"`
	Enricher         string            `mapstructure:"enricher"`
	FragmentEnricher FragmentEnricher  `mapstructure:"fragment_enricher"`
	Compose          Compose           `mapstructure:"compose"`
	Destinations     []string          `mapstructure:"destinations"`
	References       []Reference       `mapstructure:"references"`
	MetadataFields   map[string]string `mapstructure:"metadata_fields"`
	Views            []View            `mapstructure:"views"`
}

// FragmentEnricher configures per-fragment derivation. Disabled datatypes compose raw
// fragment content instead.
type FragmentEnricher struct {
	Enabled       bool   `mapstructure:"enabled"`
	Prompt        string `mapstructure:"prompt"`
	MaxInputChars int    `mapstructure:"max_input_chars"`
}

// Compose controls deterministic assembly of fragments into a view's input document.
type Compose struct {
	Order    string `mapstructure:"order"` // tier | path
	MaxChars int    `mapstructure:"max_chars"`
}

// Reference pulls data from another datatype or an external system into enrichment
// input. Every reference is a signature input; see D2 and D12.
//
// KeyFrom locates the key in the referring item: "metadata:<field>" reads item metadata,
// and anything else is a glob matched against fragment keys, whose content becomes the
// key. KeyRegex then narrows that raw value to its capture group, which is how a column
// holding "https://github.com/acme/thing" resolves against a github/repo item ID.
type Reference struct {
	Name     string    `mapstructure:"name"`
	Resolver string    `mapstructure:"resolver"`
	Datatype string    `mapstructure:"datatype"`  // inget resolver only
	Endpoint string    `mapstructure:"endpoint"`  // http resolver only
	TokenEnv SecretRef `mapstructure:"token_env"` // http resolver only
	KeyFrom  string    `mapstructure:"key_from"`
	KeyRegex string    `mapstructure:"key_regex"` // optional; one capture group
	Fields   []string  `mapstructure:"fields"`
	InjectAs string    `mapstructure:"inject_as"` // fragment | metadata
}

// View is one generated, embedded document per item. DependsOn globs decide what
// regenerates when a fragment changes (D3).
type View struct {
	Name      string   `mapstructure:"name"`
	Prompt    string   `mapstructure:"prompt"`
	DependsOn []string `mapstructure:"depends_on"`
}
