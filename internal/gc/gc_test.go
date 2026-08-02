// Tests for the three-phase collection.
//
// The artifact store is real — a file:// bucket in a temporary directory — because content
// addressing, run listing and the commit marker are the behaviour under test and a fake of them
// would only assert that the fake works. State is faked: what collection needs from it is two
// reads and a lock, and a database would add setup without adding coverage.
package gc

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
)

const (
	testSource   = "github"
	testDatatype = "github/repo"
	testProducer = "inget-fetch/test"
)

// fakeState is the State surface, with the lock outcome and the two reads controllable.
type fakeState struct {
	live        map[string]struct{}
	collected   int
	lockedKeys  map[string]bool
	takenLocks  []string
	missingRuns int
}

var errLocked = errors.New("lock is held by another process")

func (f *fakeState) Lock(_ context.Context, key string) (func() error, error) {
	if f.lockedKeys[key] {
		return nil, errLocked
	}
	f.takenLocks = append(f.takenLocks, key)
	return func() error { return nil }, nil
}

func (f *fakeState) LiveBlobRefs(context.Context) (map[string]struct{}, error) {
	if f.live == nil {
		return map[string]struct{}{}, nil
	}
	return f.live, nil
}

func (f *fakeState) CollectDerivations(_ context.Context, missingRuns int) (int, error) {
	f.missingRuns = missingRuns
	return f.collected, nil
}

// newStore opens a file-backed artifact store in a temporary directory.
func newStore(t *testing.T) *artifact.Store {
	t.Helper()
	u := url.URL{Scheme: "file", Path: t.TempDir()}
	store, err := artifact.Open(t.Context(), artifact.Options{URL: u.String()})
	if err != nil {
		t.Fatalf("opening the artifact store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("closing the artifact store: %v", err)
		}
	})
	return store
}

// runIDAt returns a run identifier whose encoded timestamp is at, which is how a test ages a
// run without waiting for one.
func runIDAt(t *testing.T, at time.Time) string {
	t.Helper()
	id, err := ulid.New(ulid.Timestamp(at), rand.Reader)
	if err != nil {
		t.Fatalf("building a run id: %v", err)
	}
	return id.String()
}

// writeRun commits one run holding one item whose only fragment points at blob, and returns its
// identifier. An empty blob writes a truncated fragment instead.
func writeRun(t *testing.T, store *artifact.Store, runID, blob string) string {
	t.Helper()
	ctx := t.Context()
	w, err := store.NewWriter(ctx, artifact.RunInfo{
		RunID: runID, Source: testSource, Datatype: testDatatype, Scope: artifact.ScopeFull,
		Producer: testProducer, DomainHash: "sha256:domain", ConfigHash: "sha256:config",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	rec := &artifact.Record{
		ItemID:      "acme/thing",
		Fingerprint: "fp-1",
		FetchedAt:   time.Now().UTC(),
		Fragments: []artifact.Fragment{{
			Key: "README.md", Fingerprint: "fp-readme", Blob: blob, Bytes: 12,
		}},
		FragmentCount: 1,
	}
	if err := w.Write(ctx, rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m, err := w.Commit(ctx, artifact.CommitInfo{})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return m.RunID
}

// putBlob stores content and returns its digest.
func putBlob(t *testing.T, store *artifact.Store, content string) string {
	t.Helper()
	digest, _, err := store.PutBlob(t.Context(), []byte(content))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	return digest
}

// options returns a collection request over the fixture datatype with a 30-day window.
func options(now time.Time) Options {
	return Options{
		Datatypes:    []Datatype{{Source: testSource, Name: testDatatype}},
		RunRetention: 30 * 24 * time.Hour,
		MissingRuns:  3,
		Now:          now,
	}
}

func TestCollectRetiresOldRunsButKeepsTheLatestCommitted(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()

	// Two runs well outside the window and one inside it. The older of the two outside is
	// collectable; the newer one is not, because it is the latest committed run.
	ancient := writeRun(t, store, runIDAt(t, now.Add(-200*24*time.Hour)), putBlob(t, store, "ancient"))
	old := writeRun(t, store, runIDAt(t, now.Add(-100*24*time.Hour)), putBlob(t, store, "old"))

	report, err := Collect(t.Context(), store, &fakeState{}, options(now))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if report.RunsDeleted != 1 || report.RunsRetained != 1 {
		t.Errorf("runs deleted/retained = %d/%d, want 1/1", report.RunsDeleted, report.RunsRetained)
	}
	if _, err := store.OpenRun(t.Context(), testSource, testDatatype, ancient); !errors.Is(err, artifact.ErrNotFound) {
		t.Errorf("the oldest run is still readable: %v", err)
	}
	if _, err := store.OpenRun(t.Context(), testSource, testDatatype, old); err != nil {
		t.Errorf("the latest committed run was collected: %v", err)
	}
}

func TestCollectKeepsBlobsOfRetainedRunsAndOfLiveState(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()

	referenced := putBlob(t, store, "referenced by the retained run")
	inState := putBlob(t, store, "referenced by a live fragment row")
	orphan := putBlob(t, store, "referenced by nothing")
	writeRun(t, store, runIDAt(t, now.Add(-time.Hour)), referenced)

	st := &fakeState{live: map[string]struct{}{inState: {}}}
	report, err := Collect(t.Context(), store, st, options(now))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if report.BlobsDeleted != 1 || report.BlobsRetained != 2 {
		t.Errorf("blobs deleted/retained = %d/%d, want 1/2", report.BlobsDeleted, report.BlobsRetained)
	}
	for digest, wantKept := range map[string]bool{referenced: true, inState: true, orphan: false} {
		exists, err := store.HasBlob(t.Context(), digest)
		if err != nil {
			t.Fatalf("HasBlob: %v", err)
		}
		if exists != wantKept {
			t.Errorf("blob %s present = %v, want %v", digest, exists, wantKept)
		}
	}
}

func TestCollectPassesTheMissingRunsWindowToState(t *testing.T) {
	store := newStore(t)
	st := &fakeState{collected: 7}

	report, err := Collect(t.Context(), store, st, options(time.Now().UTC()))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if st.missingRuns != 3 {
		t.Errorf("CollectDerivations called with missing_runs %d, want 3", st.missingRuns)
	}
	if report.DerivationsDeleted != 7 {
		t.Errorf("derivations deleted = %d, want the 7 state reported", report.DerivationsDeleted)
	}
}

func TestDryRunDeletesNothing(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()
	ancient := writeRun(t, store, runIDAt(t, now.Add(-200*24*time.Hour)), putBlob(t, store, "ancient"))
	writeRun(t, store, runIDAt(t, now.Add(-time.Hour)), putBlob(t, store, "current"))
	orphan := putBlob(t, store, "orphan")

	o := options(now)
	o.DryRun = true
	st := &fakeState{collected: 9}
	report, err := Collect(t.Context(), store, st, o)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// Two blobs, not one: the preview is internally consistent, so the run it would retire
	// stops protecting its blob in the same preview.
	if report.RunsDeleted != 1 || report.BlobsDeleted != 2 {
		t.Errorf("dry run reported %d runs and %d blobs, want 1 and 2",
			report.RunsDeleted, report.BlobsDeleted)
	}
	if report.ObjectsDeleted != 0 || report.DerivationsDeleted != 0 {
		t.Errorf("dry run deleted %d objects and %d derivations, want none",
			report.ObjectsDeleted, report.DerivationsDeleted)
	}
	if st.missingRuns != 0 {
		t.Error("dry run called CollectDerivations, which deletes rows")
	}
	if _, err := store.OpenRun(t.Context(), testSource, testDatatype, ancient); err != nil {
		t.Errorf("dry run removed a run: %v", err)
	}
	exists, err := store.HasBlob(t.Context(), orphan)
	if err != nil {
		t.Fatalf("HasBlob: %v", err)
	}
	if !exists {
		t.Error("dry run removed a blob")
	}
}

func TestCollectTakesBothLocksAndAbortsWhenOneIsHeld(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()

	st := &fakeState{}
	if _, err := Collect(t.Context(), store, st, options(now)); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []string{testDatatype, "fetch:" + testDatatype}
	if len(st.takenLocks) != len(want) {
		t.Fatalf("locks taken = %v, want %v", st.takenLocks, want)
	}
	for i, key := range want {
		if st.takenLocks[i] != key {
			t.Errorf("lock %d = %q, want %q", i, st.takenLocks[i], key)
		}
	}

	// A fetch in progress holds "fetch:<datatype>", and its uncommitted blobs are exactly what
	// collection must not touch.
	held := &fakeState{lockedKeys: map[string]bool{"fetch:" + testDatatype: true}}
	if _, err := Collect(t.Context(), store, held, options(now)); !errors.Is(err, errLocked) {
		t.Errorf("Collect with a held fetch lock: %v, want the lock error", err)
	}
}

func TestOptionsAreValidated(t *testing.T) {
	store := newStore(t)
	base := options(time.Now().UTC())

	for _, tc := range []struct {
		name    string
		mutuate func(o *Options)
	}{
		{"no datatypes", func(o *Options) { o.Datatypes = nil }},
		{"no run window", func(o *Options) { o.RunRetention = 0 }},
		{"no missing-runs window", func(o *Options) { o.MissingRuns = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mutuate(&o)
			if _, err := Collect(t.Context(), store, &fakeState{}, o); err == nil {
				t.Error("Collect: want an error")
			}
		})
	}
}
