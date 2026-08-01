// The bridge from configuration to store options.
//
// This is the only place the envelope implementation knows about internal/config. Keeping
// it to one function means tests construct Options directly, without a configuration file,
// while every command derives them the same way rather than repeating the mapping.
package artifact

import "github.com/maxwellcudlitz/inget/internal/config"

// FromConfig converts the artifacts block into store options. Validation of the values
// themselves already happened in internal/config; Options.normalize repeats the defaulting
// so a directly constructed Options behaves identically.
func FromConfig(a config.Artifacts) Options {
	return Options{
		URL:              a.URL,
		ShardTargetBytes: a.ShardTargetBytes,
		BlobMaxBytes:     a.BlobMaxBytes,
		Compression:      Compression(a.Compression),
	}
}
