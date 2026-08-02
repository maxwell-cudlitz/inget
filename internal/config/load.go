// Loading and layering. Precedence, highest wins:
//
//	environment variables -> config.local.yaml -> config.yaml
//
// The local override is looked for beside the base file, so `--config /etc/inget/x.yaml`
// picks up /etc/inget/config.local.yaml. Decoding is strict: an unknown key is an error
// rather than a setting that silently does nothing, which is the failure mode a
// config-driven tool can least afford.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

const (
	// DefaultPath is the base configuration file, relative to the working directory.
	DefaultPath = "config.yaml"
	// LocalName is the optional override layer, resolved beside the base file.
	LocalName = "config.local.yaml"
)

// Load reads, layers, decodes, validates and normalizes the configuration at path, then
// snapshots the secrets it names. The base file must exist; the local override need not.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath
	}
	v, err := newViper(path, true)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := v.Unmarshal(&cfg, decoderOptions()...); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	normalize(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration in %s: %w", path, err)
	}
	cfg.resolveSecrets()
	return &cfg, nil
}

// newViper builds a viper instance with the environment bindings registered and the
// configuration layers read. When required is false a missing base file is tolerated and
// the result carries environment values only.
func newViper(path string, required bool) (*viper.Viper, error) {
	v := viper.New()
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", EnvNestSeparator))
	v.AutomaticEnv()

	// AutomaticEnv only consults keys viper already knows, so every scalar in the schema
	// is bound explicitly. Keys absent from the file are then still overridable.
	for _, key := range EnvKeys() {
		if err := v.BindEnv(key, EnvName(key)); err != nil {
			return nil, fmt.Errorf("binding %s: %w", EnvName(key), err)
		}
	}

	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		if required || !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}

	local := filepath.Join(filepath.Dir(path), LocalName)
	v.SetConfigFile(local)
	if err := v.MergeInConfig(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("merging %s: %w", local, err)
	}
	return v, nil
}

// decoderOptions configures mapstructure: day-aware durations and any other
// TextUnmarshaler, comma-separated lists from environment values, and strict handling of
// keys the schema does not define.
func decoderOptions() []viper.DecoderConfigOption {
	return []viper.DecoderConfigOption{
		viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
			mapstructure.TextUnmarshallerHookFunc(),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		)),
		func(dc *mapstructure.DecoderConfig) { dc.ErrorUnused = true },
	}
}

// normalize applies the documented defaults that no configuration layer can supply.
// Viper cannot address list elements, so a datatype omitting `compose` has nothing to
// inherit from; the default is filled in here instead, before hashing, so a hash always
// reflects effective behavior. The eval block is defaulted here too, for a different
// reason: its numbers are decided by D14 rather than by a deployment, so a configuration
// that says nothing about quality still gates on the design's thresholds.
func normalize(c *Config) {
	normalizeEval(&c.Eval)
	for i := range c.Datatypes {
		d := &c.Datatypes[i]
		if d.Compose.Order == "" {
			d.Compose.Order = DefaultComposeOrder
		}
		if d.Compose.MaxChars == 0 {
			d.Compose.MaxChars = DefaultComposeMaxChars
		}
	}
}

// normalizeEval fills every unset eval setting with its documented default.
func normalizeEval(e *Eval) {
	if e.SampleSize == 0 {
		e.SampleSize = DefaultEvalSampleSize
	}
	for field, value := range map[*float64]float64{
		&e.Thresholds.SelfRetrieval:       DefaultSelfRetrieval,
		&e.Thresholds.Distinctiveness:     DefaultDistinctiveness,
		&e.Thresholds.ViewCoverage:        DefaultViewCoverage,
		&e.Thresholds.ViewDistinctiveness: DefaultViewDistinctiveness,
		&e.Thresholds.MetadataTop3:        DefaultMetadataTop3,
	} {
		if *field == 0 {
			*field = value
		}
	}
}
