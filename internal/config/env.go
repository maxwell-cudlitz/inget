// Environment variable naming and the set of keys that can be overridden.
//
// A dotted config key maps to INGET_ plus its uppercased path with `.` replaced by `__`,
// so models.generator.model is INGET_MODELS__GENERATOR__MODEL. The names are computed
// here and passed to viper's two-argument BindEnv rather than relying on its key
// replacer, so the mapping is explicit and testable in one place.
//
// Overridable keys are derived from the schema by walking a zero Config: every scalar
// field is registered, and nothing inside a list is, because viper cannot address list
// elements. That limitation is documented rather than worked around (see the design's
// Configuration section) — list-valued settings are overridden through
// config.local.yaml.
package config

import (
	"reflect"
	"sort"
	"strings"
)

const (
	// EnvPrefix precedes every configuration environment variable.
	EnvPrefix = "INGET"
	// EnvNestSeparator expresses one level of key nesting.
	EnvNestSeparator = "__"
)

// EnvName returns the environment variable that overrides a dotted config key.
func EnvName(key string) string {
	return EnvPrefix + "_" + strings.ToUpper(strings.ReplaceAll(key, ".", EnvNestSeparator))
}

// EnvKeys returns every env-overridable dotted key, sorted, derived from the schema
// itself so that declaring a field is all it takes to make it overridable.
func EnvKeys() []string {
	leaves := flatten("", reflect.ValueOf(Config{}))
	keys := make([]string, 0, len(leaves))
	for _, l := range leaves {
		keys = append(keys, l.path)
	}
	sort.Strings(keys)
	return keys
}
