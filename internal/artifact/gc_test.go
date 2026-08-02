// Tests for the collection primitives: run enumeration, run deletion, blob enumeration and
// blob deletion.
//
// They write through the store's own API where they can and through the bucket directly where
// the fixture is something the API cannot produce — a crashed fetch's uncommitted directory,
// and a foreign object under the blob prefix.
package artifact

import (
	"errors"
	"testing"
)

// writeObject puts one object at a raw key, for fixtures the public API cannot create.
func writeObject(t *testing.T, store *Store, key, body string) {
	t.Helper()
	if err := store.bucket.WriteAll(t.Context(), key, []byte(body), nil); err != nil {
		t.Fatalf("writing %s: %v", key, err)
	}
}

// abandonedRun leaves a run directory holding a manifest and no _COMMIT, which is what a
// fetch killed between the two writes leaves behind.
func abandonedRun(t *testing.T, store *Store) string {
	t.Helper()
	runID, err := NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	writeObject(t, store, runPrefix(testSource, testDatatype, runID)+ManifestName, "{}")
	return runID
}

func TestRunDirsIncludeUncommittedRuns(t *testing.T) {
	store := newStore(t, 0)
	committed := writeRun(t, store, 2, ScopeFull).RunID
	abandoned := abandonedRun(t, store)

	dirs, err := store.RunDirs(t.Context(), testSource, testDatatype)
	if err != nil {
		t.Fatalf("RunDirs: %v", err)
	}
	if len(dirs) != 2 {
		t.Fatalf("RunDirs returned %d directories, want 2: %+v", len(dirs), dirs)
	}
	found := map[string]RunDir{}
	for _, d := range dirs {
		found[d.RunID] = d
		if d.CreatedAt.IsZero() {
			t.Errorf("run %s has no creation time; it is decoded from the ULID", d.RunID)
		}
	}
	if !found[committed].Committed {
		t.Errorf("run %s is reported uncommitted", committed)
	}
	if found[abandoned].Committed {
		t.Errorf("run %s is reported committed without a %s marker", abandoned, CommitName)
	}
	// Chronological order is what the retention pass walks.
	if dirs[0].RunID > dirs[1].RunID {
		t.Errorf("RunDirs returned %s before %s; want chronological order", dirs[0].RunID, dirs[1].RunID)
	}
}

func TestDeleteRunRemovesEveryObject(t *testing.T) {
	store := newStore(t, 0)
	first := writeRun(t, store, 2, ScopeFull).RunID
	second := writeRun(t, store, 2, ScopeFull).RunID
	ctx := t.Context()

	deleted, err := store.DeleteRun(ctx, testSource, testDatatype, first)
	if err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	// One shard, one manifest, one marker.
	if deleted != 3 {
		t.Errorf("DeleteRun deleted %d objects, want 3", deleted)
	}
	if _, err := store.OpenRun(ctx, testSource, testDatatype, first); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenRun after deletion: %v, want ErrNotFound", err)
	}

	// The run that was left alone must still be readable, and still be latest.
	latest, err := store.LatestRun(ctx, testSource, testDatatype)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if latest != second {
		t.Errorf("LatestRun = %s, want the surviving run %s", latest, second)
	}

	// Deleting the same run again is a no-op rather than an error, so a retried collection
	// after a partial failure completes.
	if _, err := store.DeleteRun(ctx, testSource, testDatatype, first); err != nil {
		t.Errorf("second DeleteRun: %v", err)
	}
}

func TestDeleteRunRejectsANonRunDirectory(t *testing.T) {
	store := newStore(t, 0)
	if _, err := store.DeleteRun(t.Context(), testSource, testDatatype, "../../blobs"); err == nil {
		t.Error("DeleteRun with a non-ULID run id: want an error")
	}
}

func TestBlobsEnumeratesDigestsAndSkipsForeignObjects(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	var want []string
	for _, content := range []string{"alpha", "beta"} {
		digest, _, err := store.PutBlob(ctx, []byte(content))
		if err != nil {
			t.Fatalf("PutBlob: %v", err)
		}
		want = append(want, digest)
	}
	// A store may be shared; an object under the blob prefix that is not content-addressed
	// is not ours to collect.
	writeObject(t, store, blobsPrefix+"zz/zz/index.txt", "not a digest")

	var got []string
	if err := store.Blobs(ctx, func(digest string) error {
		got = append(got, digest)
		return nil
	}); err != nil {
		t.Fatalf("Blobs: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Blobs visited %v, want exactly %v", got, want)
	}
	for _, digest := range want {
		if !contains(got, digest) {
			t.Errorf("Blobs did not visit %s", digest)
		}
	}
}

func TestDeleteBlobIsIdempotent(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()
	digest, _, err := store.PutBlob(ctx, []byte("collectable"))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}

	for range 2 {
		if err := store.DeleteBlob(ctx, digest); err != nil {
			t.Fatalf("DeleteBlob: %v", err)
		}
	}
	exists, err := store.HasBlob(ctx, digest)
	if err != nil {
		t.Fatalf("HasBlob: %v", err)
	}
	if exists {
		t.Error("HasBlob is true after DeleteBlob")
	}
	if err := store.DeleteBlob(ctx, "not-a-digest"); err == nil {
		t.Error("DeleteBlob with a malformed digest: want an error")
	}
}

func TestRunBlobRefsCollectsEveryReferencedDigest(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	w, err := store.NewWriter(ctx, RunInfo{
		Source: testSource, Datatype: testDatatype, Scope: ScopeFull, Producer: testProducer,
		DomainHash: "sha256:domain", ConfigHash: "sha256:config",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Two items sharing one blob, plus a truncated fragment with none: the digest set is
	// smaller than the fragment count in both directions that matter.
	shared, _, err := store.PutBlob(ctx, []byte("shared content"))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	for i := range 2 {
		rec := testRecord(i)
		rec.Fragments[0].Blob = shared
		rec.Fragments = append(rec.Fragments, Fragment{
			Key: "huge.bin", Fingerprint: "fp-huge", Tier: 4, Truncated: true,
		})
		rec.FragmentCount = 2
		if err := w.Write(ctx, rec); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	m, err := w.Commit(ctx, CommitInfo{})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	run, err := store.OpenRun(ctx, testSource, testDatatype, m.RunID)
	if err != nil {
		t.Fatalf("OpenRun: %v", err)
	}
	refs, err := run.BlobRefs(ctx)
	if err != nil {
		t.Fatalf("BlobRefs: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("BlobRefs = %v, want just the shared digest", refs)
	}
	if _, ok := refs[shared]; !ok {
		t.Errorf("BlobRefs is missing %s", shared)
	}
}

// contains reports whether a slice holds a value.
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
