// The consumer side: run resolution, integrity checks and record streaming.
//
// Everything a reader does enforces the consumer obligations from
// docs/artifact-envelope.md. A run directory without _COMMIT does not exist. An unknown
// schema_version is rejected before any field is interpreted. A shard whose SHA-256 does
// not match its manifest entry fails the read rather than yielding records that were
// partly right.
//
// "latest" resolves by taking the largest run identifier that carries a _COMMIT marker,
// which works because run IDs are ULIDs and so sort chronologically. That is a listing
// plus one existence check, with no reliance on modification times that object stores do
// not expose meaningfully.
//
// Verification happens per shard: the compressed object is read into memory, hashed, and
// only then decoded. The memory cost is one compressed shard at a time — tens of MiB at
// the default target — and the alternative, hashing while streaming, would hand records to
// the caller before knowing whether they were trustworthy.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gocloud.dev/blob"
)

// LatestRun asks OpenRun to resolve the newest committed run.
const LatestRun = "latest"

// Run is one committed run, open for reading.
type Run struct {
	// Manifest is the validated manifest of this run.
	Manifest *Manifest

	store  *Store
	prefix string
}

// ListRuns returns the identifiers of every committed run for one source and datatype, in
// chronological order. Directories that are not ULIDs, and runs that never committed, are
// skipped: a store may be shared, and an abandoned fetch leaves exactly that behind.
func (s *Store) ListRuns(ctx context.Context, source, datatype string) ([]string, error) {
	candidates, err := s.listRunDirs(ctx, source, datatype)
	if err != nil {
		return nil, err
	}
	runs := make([]string, 0, len(candidates))
	for _, runID := range candidates {
		committed, err := s.isCommitted(ctx, source, datatype, runID)
		if err != nil {
			return nil, err
		}
		if committed {
			runs = append(runs, runID)
		}
	}
	return runs, nil
}

// LatestRun returns the newest committed run identifier, or ErrNotFound when the datatype
// has never been fetched successfully.
func (s *Store) LatestRun(ctx context.Context, source, datatype string) (string, error) {
	candidates, err := s.listRunDirs(ctx, source, datatype)
	if err != nil {
		return "", err
	}
	// Newest first, so the usual case costs one existence check.
	for i := len(candidates) - 1; i >= 0; i-- {
		committed, err := s.isCommitted(ctx, source, datatype, candidates[i])
		if err != nil {
			return "", err
		}
		if committed {
			return candidates[i], nil
		}
	}
	return "", fmt.Errorf("no committed run for %s/%s: %w", source, datatype, ErrNotFound)
}

// listRunDirs returns the sorted run-shaped directory names under a datatype prefix,
// whether or not they committed.
func (s *Store) listRunDirs(ctx context.Context, source, datatype string) ([]string, error) {
	prefix := datatypePrefix(source, datatype)
	iter := s.bucket.List(&blob.ListOptions{Prefix: prefix, Delimiter: "/"})

	var runs []string
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing runs under %s: %w", prefix, err)
		}
		if !obj.IsDir {
			continue
		}
		runID := strings.TrimSuffix(strings.TrimPrefix(obj.Key, prefix), "/")
		if _, err := ParseRunID(runID); err != nil {
			continue // not a run directory; ignore rather than fail
		}
		runs = append(runs, runID)
	}
	slices.Sort(runs)
	return runs, nil
}

// isCommitted reports whether a run directory carries its _COMMIT marker.
func (s *Store) isCommitted(ctx context.Context, source, datatype, runID string) (bool, error) {
	key := runPrefix(source, datatype, runID) + CommitName
	exists, err := s.bucket.Exists(ctx, key)
	if err != nil {
		return false, fmt.Errorf("checking %s: %w", key, err)
	}
	return exists, nil
}

// OpenRun opens a committed run. An empty runID, or LatestRun, resolves to the newest one.
// An uncommitted or unknown run is ErrNotFound.
func (s *Store) OpenRun(ctx context.Context, source, datatype, runID string) (*Run, error) {
	if runID == "" || runID == LatestRun {
		resolved, err := s.LatestRun(ctx, source, datatype)
		if err != nil {
			return nil, err
		}
		runID = resolved
	} else if _, err := ParseRunID(runID); err != nil {
		return nil, err
	}

	committed, err := s.isCommitted(ctx, source, datatype, runID)
	if err != nil {
		return nil, err
	}
	if !committed {
		return nil, fmt.Errorf("run %s has no %s marker: %w", runID, CommitName, ErrNotFound)
	}

	prefix := runPrefix(source, datatype, runID)
	body, err := s.read(ctx, prefix+ManifestName)
	if err != nil {
		return nil, err
	}
	m, err := parseManifest(body)
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", runID, err)
	}
	if m.RunID != runID || m.Source != source || m.Datatype != datatype {
		return nil, fmt.Errorf("run %s: manifest describes %s %s/%s", runID, m.RunID, m.Source, m.Datatype)
	}
	return &Run{Manifest: m, store: s, prefix: prefix}, nil
}

// parseManifest decodes and validates a manifest, checking the schema version before
// anything else is interpreted.
func parseManifest(body []byte) (*Manifest, error) {
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", ManifestName, err)
	}
	if err := checkSchema(ManifestName, probe.SchemaVersion); err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", ManifestName, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	for _, shard := range m.RecordShards {
		if err := checkShardPath(shard.Path); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// Records streams every record of the run in shard order, calling visit for each. An error
// from visit stops the walk and is returned unchanged, so a caller can use its own
// sentinel to stop early.
func (r *Run) Records(ctx context.Context, visit func(*Record) error) error {
	for _, shard := range r.Manifest.RecordShards {
		if err := r.readShard(ctx, shard, visit); err != nil {
			return err
		}
	}
	return nil
}

// readShard verifies one shard's digest, then decodes and validates its records.
func (r *Run) readShard(ctx context.Context, shard Shard, visit func(*Record) error) error {
	stored, err := r.store.read(ctx, r.prefix+shard.Path)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(stored); hex.EncodeToString(sum[:]) != shard.SHA256 {
		return fmt.Errorf("shard %s%s: sha256 does not match the manifest", r.prefix, shard.Path)
	}
	reader, err := newShardReader(shard.Path, stored)
	if err != nil {
		return err
	}
	// The shard reader wraps an in-memory buffer, so closing it only releases the
	// decompressor; there is nothing a failure could tell the caller.
	defer func() { _ = reader.Close() }()

	count := 0
	dec := json.NewDecoder(reader)
	for {
		var rec Record
		if err := dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decoding record %d in %s: %w", count, shard.Path, err)
		}
		if err := rec.Validate(); err != nil {
			return fmt.Errorf("record %d in %s: %w", count, shard.Path, err)
		}
		count++
		if err := visit(&rec); err != nil {
			return err
		}
	}
	if count != shard.Records {
		return fmt.Errorf("shard %s: %d records, manifest claims %d", shard.Path, count, shard.Records)
	}
	return nil
}
