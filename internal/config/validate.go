// Validation of the global blocks, plus the accumulator the rules share.
//
// Every rule is checked and every failure reported: an operator fixing a config file
// should see all of its problems at once, not one per run. Rules cover what this package
// can know — required fields, closed enumerations, numeric bounds, referential integrity
// between blocks, and glob syntax. Two things are deliberately not checked here:
//
//   - Driver, enricher and resolver names. Those are registry keys owned by
//     internal/source, internal/enrich and internal/destination; validating them here
//     would mean editing this package to add an implementation.
//   - Prompt file existence. Templates land with the pipeline; a missing template is a
//     run-time error reported by the enricher that tried to read it.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Validate reports every problem in the configuration as a joined error.
func (c *Config) Validate() error {
	v := &validator{}

	if c.Version != SupportedVersion {
		v.failf("version = %d, want %d", c.Version, SupportedVersion)
	}
	v.wrap("log", c.Log.Options().Validate())
	c.validateArtifacts(v)
	c.validateRetention(v)
	c.validateEnrich(v)
	c.validateState(v)
	c.validateModels(v)
	c.validateDestinations(v)
	c.validateSources(v)
	c.validateDatatypes(v)

	return v.err()
}

// validateArtifacts checks the blob store URL and the sizing limits.
func (c *Config) validateArtifacts(v *validator) {
	a := c.Artifacts
	if v.required("artifacts.url", a.URL) {
		if u, err := url.Parse(a.URL); err != nil {
			v.failf("artifacts.url = %q: %v", a.URL, err)
		} else {
			v.enum("artifacts.url scheme", u.Scheme, "file", "s3", "gs")
		}
	}
	v.positive("artifacts.shard_target_bytes", a.ShardTargetBytes)
	v.positive("artifacts.blob_max_bytes", a.BlobMaxBytes)
	v.positive("artifacts.max_fragments_per_item", int64(a.MaxFragmentsPerItem))
	v.enum("artifacts.compression", a.Compression, "zstd", "none")
}

// validateRetention checks the garbage-collection windows.
func (c *Config) validateRetention(v *validator) {
	v.positive("retention.runs", int64(c.Retention.Runs))
	v.positive("retention.missing_runs", int64(c.Retention.MissingRuns))
}

// validateEnrich checks the cascade bounds. A reference depth of 0 is legal and disables the
// invalidation cascade — references still resolve and still guard their views, but a changed
// referent stops propagating. A cascade cap of 0 would mean an unbounded work set, which is not.
func (c *Config) validateEnrich(v *validator) {
	if c.Enrich.MaxReferenceDepth < 0 {
		v.failf("enrich.max_reference_depth = %d, want 0 or more", c.Enrich.MaxReferenceDepth)
	}
	v.positive("enrich.max_cascade_per_run", int64(c.Enrich.MaxCascadePerRun))
}

// validateState checks the driver and the field each driver needs to reach its store.
func (c *Config) validateState(v *validator) {
	v.enum("state.driver", c.State.Driver, "postgres", "sqlite")
	switch c.State.Driver {
	case "postgres":
		v.required("state.dsn_env", c.State.DSNEnv.Name())
	case "sqlite":
		v.required("state.path", c.State.Path)
	}
}

// validateModels checks both model roles, including the truncation relationship that
// makes an embedding request valid.
func (c *Config) validateModels(v *validator) {
	g := c.Models.Generator
	v.client("models.generator", g.ModelClient)
	if g.Temperature < 0 || g.Temperature > 2 {
		v.failf("models.generator.temperature = %v, want between 0 and 2", g.Temperature)
	}
	v.positive("models.generator.max_output_tokens", int64(g.MaxOutputTokens))
	v.positive("models.generator.max_input_chars", int64(g.MaxInputChars))
	v.nonNegative("models.generator.price_per_mtok_in", g.PricePerMTokIn)
	v.nonNegative("models.generator.price_per_mtok_out", g.PricePerMTokOut)

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

// validator accumulates problems so one load reports all of them.
type validator struct {
	problems []error
}

// failf records a problem.
func (v *validator) failf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Errorf(format, args...))
}

// wrap records err, if any, under a path prefix.
func (v *validator) wrap(path string, err error) {
	if err != nil {
		v.problems = append(v.problems, fmt.Errorf("%s: %w", path, err))
	}
}

// required reports whether a string setting is present, recording a problem when it is
// not. The boolean lets a caller skip follow-on checks that would be meaningless.
func (v *validator) required(path, got string) bool {
	if strings.TrimSpace(got) == "" {
		v.failf("%s is required", path)
		return false
	}
	return true
}

// enum checks a closed set of permitted values.
func (v *validator) enum(path, got string, allowed ...string) {
	if !slices.Contains(allowed, got) {
		v.failf("%s = %q, want one of %s", path, got, strings.Join(allowed, ", "))
	}
}

// positive checks that a numeric limit is greater than zero.
func (v *validator) positive(path string, got int64) {
	if got <= 0 {
		v.failf("%s = %d, want a value greater than 0", path, got)
	}
}

// nonNegative checks that a price or ratio is not negative.
func (v *validator) nonNegative(path string, got float64) {
	if got < 0 {
		v.failf("%s = %v, want 0 or more", path, got)
	}
}

// httpURL checks that a setting is an absolute HTTP endpoint.
func (v *validator) httpURL(path, got string) {
	if !v.required(path, got) {
		return
	}
	u, err := url.Parse(got)
	if err != nil {
		v.failf("%s = %q: %v", path, got, err)
		return
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		v.failf("%s = %q, want an absolute http:// or https:// URL", path, got)
	}
}

// client checks the settings both model roles share. Driver existence is the model
// registry's business; here it only has to be named.
func (v *validator) client(path string, m ModelClient) {
	v.required(path+".driver", m.Driver)
	v.httpURL(path+".base_url", m.BaseURL)
	v.required(path+".model", m.Model)
	v.positive(path+".concurrency", int64(m.Concurrency))
	v.positive(path+".timeout", int64(m.Timeout))
}

// unique records a problem for every repeated name in a list.
func (v *validator) unique(path string, names []string) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			v.failf("%s contains more than one entry named %q", path, name)
		}
		seen[name] = true
	}
}

// err joins the accumulated problems, or returns nil when there are none.
func (v *validator) err() error {
	return errors.Join(v.problems...)
}
