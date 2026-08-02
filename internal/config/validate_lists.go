// Validation of the list-valued blocks: destinations, sources and datatypes.
//
// This is where referential integrity lives. A datatype names a source and one or more
// destinations; if either name does not resolve, the run would fail deep inside the
// pipeline with a nil lookup instead of at startup with a readable message. The vector
// width check belongs here too: pgvector can only index 2,000 dimensions as `vector` and
// 4,000 as `halfvec` (D8), and the effective width comes from the embedder, so neither
// block can catch the mismatch alone.
package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// maxIndexDims is the largest vector pgvector can build an HNSW index over, per storage
// type.
var maxIndexDims = map[string]int{"halfvec": 4000, "vector": 2000}

// validateDestinations checks each vector sink and its binding to the embedder width.
func (c *Config) validateDestinations(v *validator) {
	if len(c.Destinations) == 0 {
		v.failf("destinations is required: at least one vector sink must be configured")
	}
	names := make([]string, 0, len(c.Destinations))
	for i, d := range c.Destinations {
		path := listPath("destinations", i, d.Name)
		names = append(names, d.Name)
		v.required(path+".name", d.Name)
		v.required(path+".driver", d.Driver)
		v.required(path+".dsn_env", d.DSNEnv.Name())
		v.required(path+".table", d.Table)
		v.enum(path+".storage", d.Storage, "halfvec", "vector")
		v.enum(path+".granularity", d.Granularity, "item", "fragment")
		v.positive(path+".batch_size", int64(d.BatchSize))
		v.positive(path+".hnsw.m", int64(d.HNSW.M))
		v.positive(path+".hnsw.ef_construction", int64(d.HNSW.EFConstruction))
		v.positive(path+".ef_search", int64(d.EFSearch))

		if limit, ok := maxIndexDims[d.Storage]; ok {
			if dims := c.Models.Embedder.EffectiveDims(); dims > limit {
				v.failf("%s.storage = %q indexes at most %d dimensions, but models.embedder yields %d; "+
					"lower models.embedder.truncate_dims or change the storage type",
					path, d.Storage, limit, dims)
			}
		}
	}
	v.unique("destinations", names)
}

// validateSources checks each connector instance. Domain and limits keys are the driver's
// vocabulary, so only their presence is checked here.
func (c *Config) validateSources(v *validator) {
	if len(c.Sources) == 0 {
		v.failf("sources is required: at least one source must be configured")
	}
	names := make([]string, 0, len(c.Sources))
	for i, s := range c.Sources {
		path := listPath("sources", i, s.Name)
		names = append(names, s.Name)
		v.required(path+".name", s.Name)
		v.required(path+".driver", s.Driver)
		if s.Auth.APIURL != "" {
			v.httpURL(path+".auth.api_url", s.Auth.APIURL)
		}
		if len(s.Domain) == 0 {
			v.failf("%s.domain is required: a source with no domain enumerates nothing", path)
		}
	}
	v.unique("sources", names)
}

// validateDatatypes checks each datatype, its views and its references.
func (c *Config) validateDatatypes(v *validator) {
	if len(c.Datatypes) == 0 {
		v.failf("datatypes is required: at least one datatype must be configured")
	}
	names := make([]string, 0, len(c.Datatypes))
	for i, d := range c.Datatypes {
		path := listPath("datatypes", i, d.Name)
		names = append(names, d.Name)
		v.required(path+".name", d.Name)
		v.required(path+".enricher", d.Enricher)

		if v.required(path+".source", d.Source) {
			if _, ok := c.Source(d.Source); !ok {
				v.failf("%s.source = %q, which no source declares", path, d.Source)
			}
		}
		if len(d.Destinations) == 0 {
			v.failf("%s.destinations is required", path)
		}
		for j, name := range d.Destinations {
			if _, ok := c.Destination(name); !ok {
				v.failf("%s.destinations[%d] = %q, which no destination declares", path, j, name)
			}
		}

		if d.FragmentEnricher.Enabled {
			v.required(path+".fragment_enricher.prompt", d.FragmentEnricher.Prompt)
			v.positive(path+".fragment_enricher.max_input_chars", int64(d.FragmentEnricher.MaxInputChars))
		}
		v.enum(path+".compose.order", d.Compose.Order, "tier", "path")
		v.positive(path+".compose.max_chars", int64(d.Compose.MaxChars))
		if drift := d.Drift(); drift < 0 || drift > 1 {
			v.failf("%s.drift_threshold = %v, want between 0 and 1", path, drift)
		}
		for key := range d.MetadataFields {
			v.required(path+".metadata_fields key", key)
		}

		c.validateViews(v, path, d.Views)
		c.validateReferences(v, path, d.References)
	}
	v.unique("datatypes", names)
}

// validateViews checks that every view is named, prompted and scoped by valid globs.
// Wrong glob syntax means wrong invalidation in both directions (D3), so patterns are
// checked with the same matcher the delta engine uses.
func (c *Config) validateViews(v *validator, parent string, views []View) {
	if len(views) == 0 {
		v.failf("%s.views is required: a datatype with no views produces no vectors", parent)
	}
	names := make([]string, 0, len(views))
	for i, view := range views {
		path := listPath(parent+".views", i, view.Name)
		names = append(names, view.Name)
		v.required(path+".name", view.Name)
		v.required(path+".prompt", view.Prompt)
		if len(view.DependsOn) == 0 {
			v.failf("%s.depends_on is required: a view with no patterns never regenerates", path)
			continue
		}
		for j, pattern := range view.DependsOn {
			if !doublestar.ValidatePattern(pattern) {
				v.failf("%s.depends_on[%d] = %q is not a valid glob pattern", path, j, pattern)
			}
		}
	}
	v.unique(parent+".views", names)
}

// validateReferences checks reference wiring, including that an inget reference points at
// a datatype this configuration actually defines.
func (c *Config) validateReferences(v *validator, parent string, refs []Reference) {
	names := make([]string, 0, len(refs))
	for i, r := range refs {
		path := listPath(parent+".references", i, r.Name)
		names = append(names, r.Name)
		v.required(path+".name", r.Name)
		v.enum(path+".inject_as", r.InjectAs, "fragment", "metadata")
		if len(r.Fields) == 0 {
			v.failf("%s.fields is required: a reference that pulls no fields is a no-op", path)
		}
		c.validateReferenceKey(v, path, r)
		if !v.required(path+".resolver", r.Resolver) {
			continue
		}
		switch r.Resolver {
		case "inget":
			if v.required(path+".datatype", r.Datatype) {
				if _, ok := c.Datatype(r.Datatype); !ok {
					v.failf("%s.datatype = %q, which no datatype declares", path, r.Datatype)
				}
			}
		case "http":
			v.required(path+".endpoint", r.Endpoint)
		}
	}
	v.unique(parent+".references", names)
}

// validateReferenceKey checks how a reference finds its key. The glob is checked with the
// same matcher the delta engine uses, and the regexp both compiles and is required to have
// exactly one capture group: a pattern with none extracts nothing and a pattern with two is
// a reference whose key would depend on which group the code happened to read.
func (c *Config) validateReferenceKey(v *validator, path string, r Reference) {
	if v.required(path+".key_from", r.KeyFrom) {
		if glob, ok := strings.CutPrefix(r.KeyFrom, "metadata:"); ok {
			v.required(path+".key_from metadata field", glob)
		} else if !doublestar.ValidatePattern(r.KeyFrom) {
			v.failf("%s.key_from = %q is not a valid glob pattern or a metadata: reference",
				path, r.KeyFrom)
		}
	}
	if r.KeyRegex == "" {
		return
	}
	re, err := regexp.Compile(r.KeyRegex)
	if err != nil {
		v.failf("%s.key_regex = %q: %v", path, r.KeyRegex, err)
		return
	}
	if n := re.NumSubexp(); n != 1 {
		v.failf("%s.key_regex = %q has %d capture groups, want exactly 1", path, r.KeyRegex, n)
	}
}

// listPath renders a list element's path, preferring its name over its index because a
// name is what an operator searches the file for.
func listPath(block string, index int, name string) string {
	if name == "" {
		return fmt.Sprintf("%s[%d]", block, index)
	}
	return fmt.Sprintf("%s[%s]", block, name)
}
