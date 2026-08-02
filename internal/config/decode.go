// Decoding of the driver-specific blocks this package deliberately leaves raw.
//
// A source's `domain` and `limits` keys differ per driver — GitHub enumerates orgs and
// repositories, Monday enumerates workspaces and boards — so the schema carries them as
// map[string]any and the connector that owns the driver decodes them through DecodeInto.
// That keeps this package from growing a case per source while still giving the connector
// the same guarantees Load gives the rest of the file: the same decode hooks, and an
// unknown key reported as an error rather than silently ignored.
package config

import (
	"fmt"

	"github.com/go-viper/mapstructure/v2"
)

// DecodeInto decodes one raw configuration block into dest, which must be a pointer to a
// struct carrying mapstructure tags.
//
// Decoding is strict. A misspelled `include_forks` in a source's domain block is the same
// class of mistake as a misspelled top-level key, and a config-driven tool can least
// afford a setting that appears to be set and does nothing.
func DecodeInto(block map[string]any, dest any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:      dest,
		ErrorUnused: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.TextUnmarshallerHookFunc(),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return fmt.Errorf("building decoder: %w", err)
	}
	if err := decoder.Decode(block); err != nil {
		return fmt.Errorf("decoding configuration block: %w", err)
	}
	return nil
}
