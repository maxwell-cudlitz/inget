// The consumer obligations from docs/artifact-envelope.md, enforced as code.
//
// A malformed envelope must fail loudly at the boundary rather than half-way through a
// run that has already spent money. Validation therefore happens once, on read, before
// any record reaches the pipeline — and again on write, so a producer bug cannot commit
// a run that no consumer will accept.
//
// Every problem is reported, not just the first, because an operator debugging a fetch
// wants the whole list.
package artifact

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedSchema reports an envelope this build cannot interpret. It is returned
// before any other validation, since unknown fields make every other check meaningless.
var ErrUnsupportedSchema = errors.New("unsupported schema_version")

// checkSchema returns ErrUnsupportedSchema unless version is the one this build speaks.
func checkSchema(what string, version int) error {
	if version != SchemaVersion {
		return fmt.Errorf("%s: %w %d, want %d", what, ErrUnsupportedSchema, version, SchemaVersion)
	}
	return nil
}

// Validate reports every problem in the manifest as a joined error.
func (m *Manifest) Validate() error {
	if err := checkSchema("manifest", m.SchemaVersion); err != nil {
		return err
	}
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if _, err := ParseRunID(m.RunID); err != nil {
		fail("manifest.run_id: %w", err)
	}
	if m.CreatedAt.IsZero() {
		fail("manifest.created_at is required")
	}
	for field, value := range map[string]string{
		"producer":    m.Producer,
		"source":      m.Source,
		"datatype":    m.Datatype,
		"domain_hash": m.DomainHash,
		"config_hash": m.ConfigHash,
	} {
		if strings.TrimSpace(value) == "" {
			fail("manifest.%s is required", field)
		}
	}
	switch m.Scope {
	case ScopeFull:
	case ScopePartial:
		// Load-bearing: a partial run says nothing about what it did not carry, so a
		// tombstone in one would delete an item that still exists at the source.
		if len(m.Tombstones) > 0 {
			fail("manifest.tombstones: %d present on a partial run, which must carry none", len(m.Tombstones))
		}
	default:
		fail("manifest.scope = %q, want %q or %q", m.Scope, ScopeFull, ScopePartial)
	}
	for i, s := range m.RecordShards {
		for _, err := range s.problems() {
			fail("manifest.record_shards[%d].%w", i, err)
		}
	}
	return errors.Join(problems...)
}

// problems returns everything wrong with one shard entry, each phrased as a field name
// followed by the complaint so the caller can prefix an index.
func (s Shard) problems() []error {
	var out []error
	if s.Path == "" {
		out = append(out, errors.New("path is required"))
	}
	if s.Records <= 0 {
		out = append(out, fmt.Errorf("records = %d, want 1 or more", s.Records))
	}
	if s.BytesCompressed <= 0 {
		out = append(out, fmt.Errorf("bytes_compressed = %d, want 1 or more", s.BytesCompressed))
	}
	if s.BytesUncompressed <= 0 {
		out = append(out, fmt.Errorf("bytes_uncompressed = %d, want 1 or more", s.BytesUncompressed))
	}
	if err := checkDigest(s.SHA256); err != nil {
		out = append(out, fmt.Errorf("sha256: %w", err))
	}
	return out
}

// Validate reports every problem in the record as a joined error. Fingerprints are not
// checked for content: empty means "unknown", which the consumer handles by treating the
// item as changed rather than by rejecting the record.
func (r *Record) Validate() error {
	if err := checkSchema("record", r.SchemaVersion); err != nil {
		return err
	}
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if r.Datatype == "" {
		fail("record.datatype is required")
	}
	if r.ItemID == "" {
		fail("record.item_id is required")
	}
	if r.FetchedAt.IsZero() {
		fail("record.fetched_at is required")
	}
	if r.FragmentCount < len(r.Fragments) {
		fail("record.fragment_count = %d, fewer than the %d fragments carried", r.FragmentCount, len(r.Fragments))
	}
	seen := make(map[string]struct{}, len(r.Fragments))
	for i, f := range r.Fragments {
		if f.Key == "" {
			fail("record.fragments[%d].key is required", i)
			continue
		}
		// Keys address fragments within an item all the way through the cascade, so a
		// duplicate would make derivations and view scoping ambiguous.
		if _, dup := seen[f.Key]; dup {
			fail("record.fragments[%d].key = %q is a duplicate", i, f.Key)
		}
		seen[f.Key] = struct{}{}
		if f.Blob != "" {
			if err := checkDigest(f.Blob); err != nil {
				fail("record.fragments[%d].blob: %w", i, err)
			}
		}
		if f.Bytes < 0 {
			fail("record.fragments[%d].bytes = %d, want 0 or more", i, f.Bytes)
		}
	}
	return errors.Join(problems...)
}
