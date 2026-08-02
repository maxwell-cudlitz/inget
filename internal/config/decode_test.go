// Tests for DecodeInto, the decoder connectors use on their raw config blocks.
//
// Strictness is the property under test. A driver block is the one part of the file this package
// cannot validate, so if an unknown key were tolerated there, a misspelled setting would be
// undetectable anywhere.
package config

import (
	"testing"
	"time"
)

// block is a representative driver configuration: strings, a list, a number, a bool, a pointer whose
// zero value is meaningful, and a duration.
type block struct {
	Orgs     []string      `mapstructure:"orgs"`
	Rate     float64       `mapstructure:"rate"`
	MaxBytes int64         `mapstructure:"max_bytes"`
	Enabled  bool          `mapstructure:"enabled"`
	Optional *bool         `mapstructure:"optional"`
	Timeout  time.Duration `mapstructure:"timeout"`
}

func TestDecodeIntoDecodesEveryScalarKind(t *testing.T) {
	var got block
	err := DecodeInto(map[string]any{
		"orgs":      []string{"acme", "globex"},
		"rate":      10,
		"max_bytes": 33554432,
		"enabled":   true,
		"optional":  false,
		"timeout":   "30s",
	}, &got)
	if err != nil {
		t.Fatalf("DecodeInto() = %v", err)
	}
	if len(got.Orgs) != 2 || got.Orgs[0] != "acme" {
		t.Errorf("orgs = %v, want the two configured values", got.Orgs)
	}
	if got.Rate != 10 {
		t.Errorf("rate = %v, want an integer literal widened to 10", got.Rate)
	}
	if got.MaxBytes != 33554432 {
		t.Errorf("max_bytes = %d, want 33554432", got.MaxBytes)
	}
	if !got.Enabled {
		t.Error("enabled = false, want true")
	}
	if got.Optional == nil || *got.Optional {
		t.Errorf("optional = %v, want an explicit false rather than absent", got.Optional)
	}
	if got.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", got.Timeout)
	}
}

// An absent pointer field stays nil, which is what lets a driver distinguish "not set" from
// "explicitly off" and apply a default of true.
func TestDecodeIntoLeavesAbsentPointersNil(t *testing.T) {
	var got block
	if err := DecodeInto(map[string]any{"orgs": []string{"acme"}}, &got); err != nil {
		t.Fatalf("DecodeInto() = %v", err)
	}
	if got.Optional != nil {
		t.Errorf("optional = %v, want nil when the key is absent", got.Optional)
	}
}

func TestDecodeIntoRejectsUnknownKeys(t *testing.T) {
	var got block
	err := DecodeInto(map[string]any{"orgs": []string{"acme"}, "orgz": []string{"typo"}}, &got)
	if err == nil {
		t.Fatal("DecodeInto() with an unknown key = nil error, want failure")
	}
}

func TestDecodeIntoRejectsWrongTypes(t *testing.T) {
	var got block
	if err := DecodeInto(map[string]any{"orgs": "acme", "rate": "fast"}, &got); err == nil {
		t.Fatal("DecodeInto() with an unparseable number = nil error, want failure")
	}
}

func TestDecodeIntoAcceptsAnEmptyBlock(t *testing.T) {
	var got block
	if err := DecodeInto(nil, &got); err != nil {
		t.Fatalf("DecodeInto(nil) = %v, want the zero value and no error", err)
	}
	if len(got.Orgs) != 0 || got.Enabled {
		t.Errorf("decoded %+v from nil, want the zero value", got)
	}
}

func TestDecodeIntoRequiresAPointer(t *testing.T) {
	if err := DecodeInto(map[string]any{"orgs": []string{"acme"}}, block{}); err == nil {
		t.Fatal("DecodeInto() into a non-pointer = nil error, want failure")
	}
}
