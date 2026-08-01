// The producer side: JSONL shard rolling and the commit protocol.
//
// Order is the whole point (D5). Blobs first, then record shards, then manifest.json,
// then a zero-byte _COMMIT written last. A reader ignores any run directory without
// _COMMIT, so a fetch that crashes at any point leaves objects that are inert rather than
// a run that is half visible. Nothing is ever mutated; a failed attempt is abandoned and a
// new run gets a new identifier.
//
// Shards roll at Options.ShardTargetBytes of uncompressed JSON. Object storage penalizes
// many small objects — per-request latency and cost dominate and listing becomes O(n) —
// while a single shard should still be cheap to re-read, so the default 128 MiB sits
// inside the band AWS's own compaction guidance recommends.
package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RunInfo identifies the run a Writer produces. Everything here lands in the manifest
// unchanged; the hashes are computed by internal/config and carried opaquely.
type RunInfo struct {
	RunID      string // generated when empty
	Source     string
	Datatype   string
	Scope      Scope
	Producer   string // name/version of the writing binary
	DomainHash string
	ConfigHash string
}

// CommitInfo carries the totals only the producer can know, because they describe work
// that never reached the Writer: items skipped as unchanged, blobs deduplicated against
// the store, items found missing, and problems worth reporting.
type CommitInfo struct {
	ItemsSkippedUnchanged int
	BlobsWritten          int
	BlobsReused           int
	Tombstones            []string
	Truncated             bool
	Warnings              []string
}

// Writer accumulates records into shards and commits them as one run. It is safe for
// concurrent use, so a worker pool can write items as it finishes them.
type Writer struct {
	store  *Store
	info   RunInfo
	prefix string

	mu        sync.Mutex
	shard     *shardWriter // nil until the first record and after each roll
	shards    []Shard
	counts    Counts
	committed bool
}

// NewWriter starts a run. Nothing becomes visible to a reader until Commit succeeds.
func (s *Store) NewWriter(ctx context.Context, info RunInfo) (*Writer, error) {
	if info.Source == "" || info.Datatype == "" || info.Producer == "" {
		return nil, errors.New("run info requires source, datatype and producer")
	}
	if info.RunID == "" {
		runID, err := NewRunID()
		if err != nil {
			return nil, err
		}
		info.RunID = runID
	} else if _, err := ParseRunID(info.RunID); err != nil {
		return nil, err
	}
	return &Writer{
		store:  s,
		info:   info,
		prefix: runPrefix(info.Source, info.Datatype, info.RunID),
		shards: []Shard{},
	}, nil
}

// RunID returns the identifier of the run being written.
func (w *Writer) RunID() string { return w.info.RunID }

// Write appends one record. The schema version and datatype are stamped from the run so a
// shard is self-describing and cannot disagree with its manifest, and fragment_count is
// filled in from the fragments carried when the producer left it unset — the honest value
// when nothing was capped.
//
// Required arrays and maps are normalized to empty rather than nil so that they encode as
// [] and {}, which is what the specification's field table promises a consumer.
func (w *Writer) Write(ctx context.Context, rec *Record) error {
	out := *rec
	out.SchemaVersion = SchemaVersion
	if out.Datatype == "" {
		out.Datatype = w.info.Datatype
	}
	if out.FragmentCount == 0 {
		out.FragmentCount = len(out.Fragments)
	}
	if out.Fragments == nil {
		out.Fragments = []Fragment{}
	}
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	if out.Datatype != w.info.Datatype {
		return fmt.Errorf("record %s: datatype %q does not match run datatype %q", out.ItemID, out.Datatype, w.info.Datatype)
	}
	if err := out.Validate(); err != nil {
		return fmt.Errorf("record %s: %w", out.ItemID, err)
	}
	line, err := json.Marshal(&out)
	if err != nil {
		return fmt.Errorf("encoding record %s: %w", out.ItemID, err)
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return errors.New("write after commit")
	}
	if w.shard == nil {
		if w.shard, err = w.store.newShard(ctx, w.prefix, len(w.shards)); err != nil {
			return err
		}
	}
	if err := w.shard.write(line); err != nil {
		return err
	}
	w.counts.Items++
	w.counts.Fragments += len(out.Fragments)

	if w.shard.uncompressed >= w.store.opts.ShardTargetBytes {
		return w.rollLocked()
	}
	return nil
}

// rollLocked finishes the open shard and records it in the manifest. The caller holds mu.
// The shard is detached first, so a failure to flush cannot leave it behind for Close to
// finish a second time.
func (w *Writer) rollLocked() error {
	shard := w.shard
	w.shard = nil
	finished, err := shard.finish()
	if err != nil {
		return err
	}
	w.shards = append(w.shards, finished)
	return nil
}

// Commit finishes the open shard, writes the manifest, and publishes the run with the
// _COMMIT marker. The returned manifest is what a reader will see.
func (w *Writer) Commit(ctx context.Context, info CommitInfo) (*Manifest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return nil, errors.New("run already committed")
	}
	if w.shard != nil {
		if err := w.rollLocked(); err != nil {
			return nil, err
		}
	}

	counts := w.counts
	counts.ItemsSkippedUnchanged = info.ItemsSkippedUnchanged
	counts.BlobsWritten = info.BlobsWritten
	counts.BlobsReused = info.BlobsReused

	m := &Manifest{
		SchemaVersion: SchemaVersion,
		RunID:         w.info.RunID,
		// Truncated to the second so the field renders as the plain RFC 3339 timestamp
		// the specification shows rather than with a nanosecond fraction.
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
		Producer:     w.info.Producer,
		Source:       w.info.Source,
		Datatype:     w.info.Datatype,
		Scope:        w.info.Scope,
		DomainHash:   w.info.DomainHash,
		ConfigHash:   w.info.ConfigHash,
		RecordShards: w.shards,
		Counts:       counts,
		Tombstones:   nonNil(info.Tombstones),
		Truncated:    info.Truncated,
		Warnings:     nonNil(info.Warnings),
	}
	// Validating before writing keeps a producer bug from committing a run that every
	// consumer would then reject.
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("refusing to commit run %s: %w", w.info.RunID, err)
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	if err := w.store.bucket.WriteAll(ctx, w.prefix+ManifestName, body, nil); err != nil {
		return nil, fmt.Errorf("writing %s: %w", w.prefix+ManifestName, err)
	}
	if err := w.store.bucket.WriteAll(ctx, w.prefix+CommitName, nil, nil); err != nil {
		return nil, fmt.Errorf("writing %s: %w", w.prefix+CommitName, err)
	}
	w.committed = true
	return m, nil
}

// Close abandons an uncommitted run, releasing the open shard's resources. It is a no-op
// after Commit. Objects already written stay behind for inget state gc; without a _COMMIT
// marker no reader will see them.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.shard == nil {
		return nil
	}
	shard := w.shard
	w.shard = nil
	if _, err := shard.finish(); err != nil {
		return err
	}
	return nil
}

// nonNil returns an empty slice for nil, so required manifest arrays encode as [] rather
// than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
