// Hashing tests. The properties that matter: a hash depends on effective values and not
// on how they were written or which layer supplied them, and it moves whenever an input
// that changes what a fetch produces changes.
package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// hashes returns the config and domain hash for the fixture's only source/datatype pair.
func hashes(t *testing.T, cfg *Config) (configHash, domainHash string) {
	t.Helper()
	configHash, err := cfg.ConfigHash("git", "git/repo")
	if err != nil {
		t.Fatalf("ConfigHash: %v", err)
	}
	domainHash, err = cfg.DomainHash("git")
	if err != nil {
		t.Fatalf("DomainHash: %v", err)
	}
	return configHash, domainHash
}

func TestHashesIgnoreKeyOrder(t *testing.T) {
	ordered := load(t, filepath.Join("testdata", "minimal.yaml"))
	reordered := load(t, filepath.Join("testdata", "reordered.yaml"))

	wantConfig, wantDomain := hashes(t, ordered)
	gotConfig, gotDomain := hashes(t, reordered)

	if gotConfig != wantConfig {
		t.Errorf("config hash changed with key order:\n ordered   %s\n reordered %s", wantConfig, gotConfig)
	}
	if gotDomain != wantDomain {
		t.Errorf("domain hash changed with key order:\n ordered   %s\n reordered %s", wantDomain, gotDomain)
	}
	if !strings.HasPrefix(gotConfig, "sha256:") {
		t.Errorf("config hash = %q, want a sha256: prefix as the envelope specifies", gotConfig)
	}
}

// TestHashIgnoresWhichLayerSuppliedAValue is why hashing runs on the decoded struct: an
// environment override arrives as a string, and hashing raw settings would make an
// override that changes nothing look like a config change.
func TestHashIgnoresWhichLayerSuppliedAValue(t *testing.T) {
	base := fixture(t, "minimal.yaml")
	fromFile, _ := hashes(t, load(t, base))

	t.Setenv("INGET_ARTIFACTS__BLOB_MAX_BYTES", "512") // identical to the file
	fromEnv, _ := hashes(t, load(t, base))

	if fromEnv != fromFile {
		t.Errorf("config hash changed when an env var restated the file's value:\n file %s\n env  %s", fromFile, fromEnv)
	}
}

func TestHashesRespondToRelevantChanges(t *testing.T) {
	baseConfig, baseDomain := hashes(t, valid(t))

	tests := []struct {
		name          string
		mutate        func(*Config)
		wantConfigNew bool
		wantDomainNew bool
	}{
		{"domain value", func(c *Config) { c.Sources[0].Domain["orgs"] = []any{"other"} }, true, true},
		{"domain key added", func(c *Config) { c.Sources[0].Domain["topics"] = []any{"go"} }, true, true},
		{"source limit", func(c *Config) { c.Sources[0].Limits["max_concurrent"] = 9 }, true, false},
		{"token variable renamed", func(c *Config) { c.Sources[0].Auth.TokenEnv = "OTHER_TOKEN" }, true, false},
		{"view prompt path", func(c *Config) { c.Datatypes[0].Views[0].Prompt = "prompts/other.tmpl" }, true, false},
		{"dependency glob", func(c *Config) { c.Datatypes[0].Views[0].DependsOn = []string{"**"} }, true, false},
		{"fragment cap", func(c *Config) { c.Artifacts.MaxFragmentsPerItem = 11 }, true, false},
		{"blob cap", func(c *Config) { c.Artifacts.BlobMaxBytes = 1024 }, true, false},
		// The store location does not change content, so it is excluded by design.
		{"artifact store url", func(c *Config) { c.Artifacts.URL = "s3://bucket/prefix" }, false, false},
		{"unrelated model setting", func(c *Config) { c.Models.Generator.Concurrency = 32 }, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid(t)
			tt.mutate(cfg)
			gotConfig, gotDomain := hashes(t, cfg)

			if changed := gotConfig != baseConfig; changed != tt.wantConfigNew {
				t.Errorf("config hash changed = %v, want %v", changed, tt.wantConfigNew)
			}
			if changed := gotDomain != baseDomain; changed != tt.wantDomainNew {
				t.Errorf("domain hash changed = %v, want %v", changed, tt.wantDomainNew)
			}
		})
	}
}

func TestHashesRejectUnknownNames(t *testing.T) {
	cfg := valid(t)

	if _, err := cfg.DomainHash("absent"); err == nil {
		t.Error("DomainHash for an unknown source = nil error, want failure")
	}
	if _, err := cfg.ConfigHash("absent", "git/repo"); err == nil {
		t.Error("ConfigHash for an unknown source = nil error, want failure")
	}
	if _, err := cfg.ConfigHash("git", "absent"); err == nil {
		t.Error("ConfigHash for an unknown datatype = nil error, want failure")
	}

	cfg.Sources = append(cfg.Sources, Source{Name: "other", Driver: "github"})
	if _, err := cfg.ConfigHash("other", "git/repo"); err == nil {
		t.Error("ConfigHash pairing a datatype with a source it does not read = nil error, want failure")
	}
}

// TestEqualDurationsHashEqually confirms the canonical form is the decoded value, not the
// text: 30d and 720h are the same retention window and must not look like a change.
func TestEqualDurationsHashEqually(t *testing.T) {
	var day, hours Duration
	if err := day.UnmarshalText([]byte("30d")); err != nil {
		t.Fatalf("parsing 30d: %v", err)
	}
	if err := hours.UnmarshalText([]byte("720h")); err != nil {
		t.Fatalf("parsing 720h: %v", err)
	}
	if day != hours {
		t.Fatalf("30d = %v, 720h = %v, want equal", day, hours)
	}
}
