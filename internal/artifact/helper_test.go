// Shared test fixtures: a filesystem-backed store and the records written into it.
package artifact

import (
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// testSource and testDatatype are the names every test writes under. The datatype
// deliberately contains a slash so key flattening is exercised throughout.
const (
	testSource   = "github"
	testDatatype = "github/repo"
	testProducer = "inget-fetch/test"
)

// fixedTime keeps timestamps comparable across a round trip: whole seconds in UTC, which
// is exactly what RFC 3339 carries.
var fixedTime = time.Date(2026, 7, 31, 20, 39, 52, 0, time.UTC)

// newStore opens a store in a temporary directory. shardTarget of zero takes the default.
func newStore(t *testing.T, shardTarget int64) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(t.Context(), Options{
		URL:              fileURL(dir),
		ShardTargetBytes: shardTarget,
		BlobMaxBytes:     4096,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

// fileURL renders a directory as a file:// URL with any path escaping applied.
func fileURL(dir string) string {
	u := url.URL{Scheme: "file", Path: dir}
	return u.String()
}

// storeDir recovers the filesystem directory behind a store URL, so a test can corrupt or
// remove an object behind the abstraction's back.
func storeDir(t *testing.T, store *Store) string {
	t.Helper()
	u, err := url.Parse(store.opts.URL)
	if err != nil {
		t.Fatalf("parsing store URL: %v", err)
	}
	return u.Path
}

// objectPath returns the filesystem path of a key within a store.
func objectPath(t *testing.T, store *Store, key string) string {
	t.Helper()
	return filepath.Join(storeDir(t, store), filepath.FromSlash(key))
}

// testRecord builds a record with deterministic content derived from i.
func testRecord(i int) *Record {
	return &Record{
		ItemID:      itemID(i),
		Fingerprint: fingerprint(i),
		FetchedAt:   fixedTime,
		Metadata: map[string]string{
			"name":     itemID(i),
			"language": "Go",
		},
		Fragments: []Fragment{{
			Key:         "README.md",
			Fingerprint: fingerprint(i),
			Bytes:       int64(64 + i),
			Tier:        0,
			MIME:        "text/markdown",
			Meta:        map[string]string{"note": "fixture"},
		}},
		FragmentCount: 1,
	}
}

// itemID and fingerprint keep fixture identities stable and readable in failure output.
func itemID(i int) string      { return "maxwellcudlitz/repo-" + strconv.Itoa(i) }
func fingerprint(i int) string { return "fp-" + strconv.Itoa(i) }

// writeRun writes n records as one committed run and returns its manifest.
func writeRun(t *testing.T, store *Store, n int, scope Scope) *Manifest {
	t.Helper()
	ctx := t.Context()
	w, err := store.NewWriter(ctx, RunInfo{
		Source:     testSource,
		Datatype:   testDatatype,
		Scope:      scope,
		Producer:   testProducer,
		DomainHash: "sha256:domain",
		ConfigHash: "sha256:config",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := range n {
		if err := w.Write(ctx, testRecord(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	m, err := w.Commit(ctx, CommitInfo{BlobsWritten: 1, BlobsReused: 2})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return m
}
