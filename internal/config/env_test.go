// Tests for the two small mechanisms the loader leans on: day-aware duration parsing and
// the derivation of environment variable names from schema keys.
package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDurationUnmarshalText(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"empty is zero", "", 0, false},
		{"seconds", "120s", 2 * time.Minute, false},
		{"minutes and seconds", "1m30s", 90 * time.Second, false},
		{"days", "30d", 30 * Day, false},
		{"fractional days", "0.5d", 12 * time.Hour, false},
		{"days and hours", "1d12h", 36 * time.Hour, false},
		{"padded", "  7d  ", 7 * Day, false},
		{"zero", "0", 0, false},
		{"unknown unit", "5x", 0, true},
		{"words", "thirty days", 0, true},
		{"day without count", "d", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Duration
			err := got.UnmarshalText([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalText(%q) = %v, want error", tt.input, got.Duration())
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalText(%q) returned error: %v", tt.input, err)
			}
			if got.Duration() != tt.want {
				t.Errorf("UnmarshalText(%q) = %v, want %v", tt.input, got.Duration(), tt.want)
			}
		})
	}
}

func TestDurationRoundTrip(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("90m")); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	text, err := d.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Duration
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText(%q): %v", text, err)
	}
	if back != d {
		t.Errorf("round trip of %v produced %v", d, back)
	}
}

func TestEnvName(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"version", "INGET_VERSION"},
		{"log.level", "INGET_LOG__LEVEL"},
		{"models.generator.model", "INGET_MODELS__GENERATOR__MODEL"},
		{"artifacts.shard_target_bytes", "INGET_ARTIFACTS__SHARD_TARGET_BYTES"},
	}
	for _, tt := range tests {
		if got := EnvName(tt.key); got != tt.want {
			t.Errorf("EnvName(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

// TestEnvKeysCoverScalarsAndSkipLists asserts both halves of the documented contract:
// every scalar is overridable, including those inside embedded blocks, and nothing inside
// a list is.
func TestEnvKeysCoverScalarsAndSkipLists(t *testing.T) {
	keys := EnvKeys()

	for _, want := range []string{
		"version",
		"log.level",
		"artifacts.url",
		"retention.runs",
		"enrich.max_cascade_per_run",
		"state.dsn_env",
		"models.generator.model",  // squashed from ModelClient
		"models.generator.seed",   // declared on Generator
		"models.embedder.timeout", // squashed from ModelClient
		"models.embedder.batch_size",
	} {
		if !slices.Contains(keys, want) {
			t.Errorf("EnvKeys() is missing %q", want)
		}
	}

	for _, key := range keys {
		for _, list := range []string{"sources", "destinations", "datatypes"} {
			if strings.HasPrefix(key, list) {
				t.Errorf("EnvKeys() contains %q; list elements are not env-addressable", key)
			}
		}
		if strings.Contains(key, "modelclient") {
			t.Errorf("EnvKeys() contains %q; embedded blocks must be squashed", key)
		}
	}
}
