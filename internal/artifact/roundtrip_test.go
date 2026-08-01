// Round-trip and integrity tests: the acceptance criteria for the envelope.
//
// These assert behavior a consumer depends on, not implementation: records come back
// exactly as written and in order, shards roll, an uncommitted run is invisible, a
// corrupted shard fails the read, and an unknown schema version is refused.
package artifact

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

// TestRoundTripAcrossShards writes 10k records with a small shard target, so the run spans
// many shards, and reads them back identically.
func TestRoundTripAcrossShards(t *testing.T) {
	const records = 10_000
	// 64 KiB rolls often enough to exercise multi-shard behavior without writing 128 MiB.
	store := newStore(t, 64<<10)
	ctx := t.Context()

	m := writeRun(t, store, records, ScopeFull)
	if len(m.RecordShards) < 2 {
		t.Fatalf("record_shards = %d, want at least 2 with a 64 KiB target", len(m.RecordShards))
	}
	if m.Counts.Items != records {
		t.Errorf("counts.items = %d, want %d", m.Counts.Items, records)
	}
	if m.Counts.Fragments != records {
		t.Errorf("counts.fragments = %d, want %d", m.Counts.Fragments, records)
	}

	total := 0
	for i, shard := range m.RecordShards {
		if want := shardName(i, CompressionZstd); shard.Path != want {
			t.Errorf("record_shards[%d].path = %q, want %q", i, shard.Path, want)
		}
		if shard.BytesCompressed >= shard.BytesUncompressed {
			t.Errorf("shard %s: compressed %d not smaller than uncompressed %d",
				shard.Path, shard.BytesCompressed, shard.BytesUncompressed)
		}
		total += shard.Records
	}
	if total != records {
		t.Errorf("shard record counts sum to %d, want %d", total, records)
	}

	run, err := store.OpenRun(ctx, testSource, testDatatype, LatestRun)
	if err != nil {
		t.Fatalf("OpenRun: %v", err)
	}
	read := 0
	if err := run.Records(ctx, func(got *Record) error {
		want := testRecord(read)
		want.SchemaVersion = SchemaVersion
		want.Datatype = testDatatype
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("record %d:\n got %+v\nwant %+v", read, got, want)
		}
		read++
		return nil
	}); err != nil {
		t.Fatalf("Records: %v", err)
	}
	if read != records {
		t.Errorf("read %d records, want %d", read, records)
	}
}

// TestEmptyRunCommits covers a run that found nothing: valid, committed, no shards. A
// scheduled full sync of an empty domain must still commit, because its tombstones are how
// the consumer learns everything was deleted.
func TestEmptyRunCommits(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	w, err := store.NewWriter(ctx, RunInfo{
		Source: testSource, Datatype: testDatatype, Scope: ScopeFull,
		Producer: testProducer, DomainHash: "sha256:domain", ConfigHash: "sha256:config",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	m, err := w.Commit(ctx, CommitInfo{Tombstones: []string{"maxwellcudlitz/retired"}})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(m.RecordShards) != 0 {
		t.Errorf("record_shards = %d, want 0", len(m.RecordShards))
	}
	if !m.ProcessTombstones() {
		t.Error("ProcessTombstones = false on an untruncated full run, want true")
	}

	run, err := store.OpenRun(ctx, testSource, testDatatype, "")
	if err != nil {
		t.Fatalf("OpenRun: %v", err)
	}
	if err := run.Records(ctx, func(*Record) error {
		t.Error("Records visited a record in an empty run")
		return nil
	}); err != nil {
		t.Fatalf("Records: %v", err)
	}
	if got := run.Manifest.Tombstones; len(got) != 1 || got[0] != "maxwellcudlitz/retired" {
		t.Errorf("tombstones = %v, want one entry", got)
	}
}

// TestUncommittedRunIsInvisible proves the commit marker is what makes a run exist. The
// newer run sorts higher and has a complete manifest, but without _COMMIT it is skipped in
// favour of the older committed one.
func TestUncommittedRunIsInvisible(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	committed := writeRun(t, store, 3, ScopeFull)
	abandoned := writeRun(t, store, 3, ScopeFull)
	marker := objectPath(t, store, runPrefix(testSource, testDatatype, abandoned.RunID)+CommitName)
	if err := os.Remove(marker); err != nil {
		t.Fatalf("removing commit marker: %v", err)
	}

	latest, err := store.LatestRun(ctx, testSource, testDatatype)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if latest != committed.RunID {
		t.Errorf("LatestRun = %s, want the committed run %s", latest, committed.RunID)
	}

	runs, err := store.ListRuns(ctx, testSource, testDatatype)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0] != committed.RunID {
		t.Errorf("ListRuns = %v, want only %s", runs, committed.RunID)
	}
	if _, err := store.OpenRun(ctx, testSource, testDatatype, abandoned.RunID); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenRun on an uncommitted run: %v, want ErrNotFound", err)
	}
}

// TestCorruptShardFailsRead proves shard digests are verified before records are parsed.
func TestCorruptShardFailsRead(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	m := writeRun(t, store, 10, ScopeFull)
	shard := objectPath(t, store, runPrefix(testSource, testDatatype, m.RunID)+m.RecordShards[0].Path)
	if err := os.WriteFile(shard, []byte("not the bytes the manifest describes"), 0o600); err != nil {
		t.Fatalf("corrupting shard: %v", err)
	}

	run, err := store.OpenRun(ctx, testSource, testDatatype, m.RunID)
	if err != nil {
		t.Fatalf("OpenRun: %v", err)
	}
	err = run.Records(ctx, func(*Record) error {
		t.Error("Records yielded a record from a corrupted shard")
		return nil
	})
	if err == nil {
		t.Fatal("Records succeeded on a corrupted shard, want a digest failure")
	}
}

// TestUnknownSchemaVersionRejected covers both places a version appears: the manifest,
// read before any field is interpreted, and a record inside a shard.
func TestUnknownSchemaVersionRejected(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()
	m := writeRun(t, store, 2, ScopeFull)

	manifest := objectPath(t, store, runPrefix(testSource, testDatatype, m.RunID)+ManifestName)
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decoding manifest: %v", err)
	}
	fields["schema_version"] = 99
	bumped, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encoding manifest: %v", err)
	}
	if err := os.WriteFile(manifest, bumped, 0o600); err != nil {
		t.Fatalf("rewriting manifest: %v", err)
	}

	if _, err := store.OpenRun(ctx, testSource, testDatatype, m.RunID); !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("OpenRun on schema_version 99: %v, want ErrUnsupportedSchema", err)
	}
	if err := (&Record{SchemaVersion: 99}).Validate(); !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("Record.Validate on schema_version 99: %v, want ErrUnsupportedSchema", err)
	}
}
