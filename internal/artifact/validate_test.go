// Validation tests: every consumer obligation that can be checked without a store.
package artifact

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// validRunID is the identifier from the specification's example manifest.
const validRunID = "01K1AB2CDEFGHJKMNPQRSTVWXY"

// validManifest returns a manifest that passes, for tests to break one field at a time.
func validManifest() *Manifest {
	return &Manifest{
		SchemaVersion: SchemaVersion,
		RunID:         validRunID,
		CreatedAt:     fixedTime,
		Producer:      testProducer,
		Source:        testSource,
		Datatype:      testDatatype,
		Scope:         ScopeFull,
		DomainHash:    "sha256:9f2c",
		ConfigHash:    "sha256:41ab",
		RecordShards: []Shard{{
			Path:              "records-00000.jsonl.zst",
			Records:           812,
			BytesCompressed:   9418233,
			BytesUncompressed: 133214887,
			SHA256:            strings.Repeat("3d1e", 16),
		}},
		Tombstones: []string{},
		Warnings:   []string{},
	}
}

func TestManifestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string // substring; empty means the manifest must pass
	}{
		{name: "valid full run"},
		{
			name:   "valid partial run",
			mutate: func(m *Manifest) { m.Scope = ScopePartial },
		},
		{
			name:    "unknown schema version",
			mutate:  func(m *Manifest) { m.SchemaVersion = 2 },
			wantErr: "unsupported schema_version",
		},
		{
			name:    "run id is not a ULID",
			mutate:  func(m *Manifest) { m.RunID = "run-1" },
			wantErr: "run_id",
		},
		{
			name:    "missing created_at",
			mutate:  func(m *Manifest) { m.CreatedAt = time.Time{} },
			wantErr: "created_at is required",
		},
		{
			name:    "missing producer",
			mutate:  func(m *Manifest) { m.Producer = "" },
			wantErr: "producer is required",
		},
		{
			name:    "blank source",
			mutate:  func(m *Manifest) { m.Source = "  " },
			wantErr: "source is required",
		},
		{
			name:    "missing config hash",
			mutate:  func(m *Manifest) { m.ConfigHash = "" },
			wantErr: "config_hash is required",
		},
		{
			name:    "unknown scope",
			mutate:  func(m *Manifest) { m.Scope = "incremental" },
			wantErr: "scope",
		},
		{
			// The load-bearing rule: a partial run says nothing about what it omitted,
			// so a tombstone in one would delete a live item.
			name: "tombstones on a partial run",
			mutate: func(m *Manifest) {
				m.Scope = ScopePartial
				m.Tombstones = []string{"maxwellcudlitz/live-repo"}
			},
			wantErr: "must carry none",
		},
		{
			name:    "shard without a path",
			mutate:  func(m *Manifest) { m.RecordShards[0].Path = "" },
			wantErr: "record_shards[0].path is required",
		},
		{
			name:    "shard with no records",
			mutate:  func(m *Manifest) { m.RecordShards[0].Records = 0 },
			wantErr: "record_shards[0].records",
		},
		{
			name:    "shard digest is not hex",
			mutate:  func(m *Manifest) { m.RecordShards[0].SHA256 = "zz" },
			wantErr: "record_shards[0].sha256",
		},
		{
			name:    "no shards is valid",
			mutate:  func(m *Manifest) { m.RecordShards = nil },
			wantErr: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validManifest()
			if tt.mutate != nil {
				tt.mutate(m)
			}
			assertError(t, "Manifest.Validate", m.Validate(), tt.wantErr)
		})
	}
}

// TestProcessTombstones covers the deletion gate: only a run that enumerated everything
// and finished doing so may imply absence.
func TestProcessTombstones(t *testing.T) {
	tests := []struct {
		scope     Scope
		truncated bool
		want      bool
	}{
		{ScopeFull, false, true},
		{ScopeFull, true, false},
		{ScopePartial, false, false},
		{ScopePartial, true, false},
	}
	for _, tt := range tests {
		m := &Manifest{Scope: tt.scope, Truncated: tt.truncated}
		if got := m.ProcessTombstones(); got != tt.want {
			t.Errorf("scope=%s truncated=%v: ProcessTombstones = %v, want %v",
				tt.scope, tt.truncated, got, tt.want)
		}
	}
}

// assertError reports whether err matches want, where an empty want means success.
func assertError(t *testing.T, what string, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Errorf("%s = %v, want no error", what, err)
	case want != "" && err == nil:
		t.Errorf("%s = nil, want an error mentioning %q", what, want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Errorf("%s = %v, want an error mentioning %q", what, err, want)
	}
	if want == "unsupported schema_version" && err != nil && !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("%s = %v, want it to wrap ErrUnsupportedSchema", what, err)
	}
}
