// The destructive side of the store: enumeration and deletion for `inget state gc`.
//
// Nothing else in this package deletes. It lives in its own file because the read and write
// paths are content-addressed and idempotent — a mistake there costs a redundant upload —
// while a mistake here costs data that only a re-fetch can rebuild.
//
// Two orderings matter. A run directory's _COMMIT marker is deleted first, mirroring the
// commit protocol: an interrupted deletion then leaves a directory every consumer already
// ignores, rather than a committed run with missing shards. And collection as a whole visits
// runs before blobs, because the retention predicate for a blob is "no retained run and no
// live fragment references it" — computing it against runs that are about to disappear would
// keep their blobs alive for one more cycle.
package artifact

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

// RunDir is one run-shaped directory in the store. Committed reports whether it carries its
// _COMMIT marker; CreatedAt is decoded from the ULID rather than read from object metadata,
// which object stores do not expose comparably.
type RunDir struct {
	RunID     string
	CreatedAt time.Time
	Committed bool
}

// RunDirs returns every run-shaped directory for one source and datatype in chronological
// order, committed or not. Uncommitted directories are included because they are exactly what
// a crashed fetch leaves behind, and collecting them is the only thing that ever will.
func (s *Store) RunDirs(ctx context.Context, source, datatype string) ([]RunDir, error) {
	names, err := s.listRunDirs(ctx, source, datatype)
	if err != nil {
		return nil, err
	}
	dirs := make([]RunDir, 0, len(names))
	for _, runID := range names {
		createdAt, err := ParseRunID(runID)
		if err != nil {
			continue // listRunDirs already filtered these; belt and braces
		}
		committed, err := s.isCommitted(ctx, source, datatype, runID)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, RunDir{RunID: runID, CreatedAt: createdAt, Committed: committed})
	}
	return dirs, nil
}

// DeleteRun removes every object of one run directory and reports how many it deleted. A run
// that is already gone deletes nothing and is not an error, so a retried collection is safe.
func (s *Store) DeleteRun(ctx context.Context, source, datatype, runID string) (int, error) {
	if _, err := ParseRunID(runID); err != nil {
		return 0, err
	}
	prefix := runPrefix(source, datatype, runID)
	keys, err := s.keysUnder(ctx, prefix)
	if err != nil {
		return 0, err
	}

	// The marker first, so an interrupted deletion leaves an invisible directory rather than
	// a committed run whose shards have started disappearing.
	commit := prefix + CommitName
	if err := s.deleteKey(ctx, commit); err != nil {
		return 0, err
	}
	deleted := 0
	for _, key := range keys {
		if key != commit {
			if err := s.deleteKey(ctx, key); err != nil {
				return deleted, err
			}
		}
		deleted++
	}
	return deleted, nil
}

// Blobs calls visit once per stored blob digest, in key order. An object under the blob
// prefix whose name is not a digest is skipped: the store may be shared, and this is the one
// place that would otherwise delete something it does not understand.
func (s *Store) Blobs(ctx context.Context, visit func(digest string) error) error {
	iter := s.bucket.List(&blob.ListOptions{Prefix: blobsPrefix})
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("listing blobs: %w", err)
		}
		if obj.IsDir {
			continue
		}
		digest := obj.Key[strings.LastIndex(obj.Key, "/")+1:]
		if checkDigest(digest) != nil {
			continue
		}
		if err := visit(digest); err != nil {
			return err
		}
	}
}

// DeleteBlob removes one blob. A digest that is already absent is not an error.
func (s *Store) DeleteBlob(ctx context.Context, digest string) error {
	if err := checkDigest(digest); err != nil {
		return err
	}
	return s.deleteKey(ctx, blobKey(digest))
}

// BlobRefs returns every blob digest this run's records reference. It streams the shards, so
// the cost is one pass over the run and the memory is one digest set.
func (r *Run) BlobRefs(ctx context.Context) (map[string]struct{}, error) {
	refs := map[string]struct{}{}
	err := r.Records(ctx, func(rec *Record) error {
		for _, frag := range rec.Fragments {
			if frag.Blob != "" {
				refs[frag.Blob] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

// keysUnder lists the object keys under a prefix, sorted so deletion order is deterministic.
func (s *Store) keysUnder(ctx context.Context, prefix string) ([]string, error) {
	iter := s.bucket.List(&blob.ListOptions{Prefix: prefix})
	var keys []string
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", prefix, err)
		}
		if !obj.IsDir {
			keys = append(keys, obj.Key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// deleteKey removes one object, treating an already-absent key as done.
func (s *Store) deleteKey(ctx context.Context, key string) error {
	err := s.bucket.Delete(ctx, key)
	if err == nil || gcerrors.Code(err) == gcerrors.NotFound {
		return nil
	}
	return fmt.Errorf("deleting %s: %w", key, err)
}
