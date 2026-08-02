// Fakes and fixtures for the orchestrator tests: a scripted connector, a sqlite state store and a
// file:// artifact store.
//
// The connector counts what it was asked for, because the assertions that matter here are about work
// not done. "A second run performs no fetch" is only worth asserting against the thing that would
// have been called.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/source"
	"github.com/maxwellcudlitz/inget/internal/state"
)

const (
	testSource   = "github"
	testDatatype = "github/repo"
	testProducer = "inget-fetch/test"
	testConfig   = "sha256:config"
	testDomain   = "sha256:domain"
)

// fakeItem is one item the connector will answer with.
type fakeItem struct {
	id          string
	fingerprint string
	fragments   map[string]string // key to content
	fail        bool              // Fetch returns an error for this item
	warnings    []string
}

// fakeConnector serves a scripted set of items and records what was asked of it.
type fakeConnector struct {
	items []fakeItem

	mu      sync.Mutex
	listed  int
	fetched []string
}

func (c *fakeConnector) Name() string        { return testSource }
func (c *fakeConnector) Datatypes() []string { return []string{testDatatype} }
func (c *fakeConnector) Close() error        { return nil }

// List yields every scripted item, honouring Only and Limit the way a real connector does.
func (c *fakeConnector) List(_ context.Context, _ string, q source.ListQuery, yield func(source.Ref) error) error {
	c.mu.Lock()
	c.listed++
	c.mu.Unlock()

	only := map[string]bool{}
	for _, id := range q.Only {
		only[id] = true
	}
	count := 0
	for _, item := range c.items {
		if len(only) > 0 && !only[item.id] {
			continue
		}
		if err := yield(source.Ref{ID: item.id, Fingerprint: item.fingerprint}); err != nil {
			return err
		}
		count++
		if q.Limit > 0 && count >= q.Limit {
			return source.ErrStopList
		}
	}
	return nil
}

// Fetch answers with the scripted fragments.
func (c *fakeConnector) Fetch(_ context.Context, _ string, ref source.Ref) (source.Result, error) {
	c.mu.Lock()
	c.fetched = append(c.fetched, ref.ID)
	c.mu.Unlock()

	for _, item := range c.items {
		if item.id != ref.ID {
			continue
		}
		if item.fail {
			return source.Result{}, errors.New("scripted fetch failure")
		}
		result := source.Result{
			Item: source.Item{
				ID:          item.id,
				Fingerprint: item.fingerprint,
				Metadata:    map[string]string{"full_name": item.id},
			},
			Warnings: item.warnings,
		}
		for _, key := range sortedKeys(item.fragments) {
			content := item.fragments[key]
			result.Fragments = append(result.Fragments, source.Fragment{
				Key:         key,
				Fingerprint: contentHash(content),
				Content:     []byte(content),
				Bytes:       int64(len(content)),
				Tier:        3,
			})
		}
		return result, nil
	}
	return source.Result{}, fmt.Errorf("no scripted item %q", ref.ID)
}

// fetchCount reports how many items were fetched.
func (c *fakeConnector) fetchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.fetched)
}

// reset zeroes the counters between phases.
func (c *fakeConnector) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listed = 0
	c.fetched = nil
}

// harness is one datatype wired to real stores and the fake connector.
type harness struct {
	t     *testing.T
	ctx   context.Context
	conn  *fakeConnector
	state state.Store
	arts  *artifact.Store
	dir   string
}

// newHarness builds the harness with the default blob size cap.
func newHarness(t *testing.T, items ...fakeItem) *harness {
	t.Helper()
	return newHarnessWithBlobCap(t, 1<<20, items...)
}

// newHarnessWithBlobCap builds the harness with an explicit artifacts.blob_max_bytes, so a test can
// make ordinary content exceed it. State is sqlite in a temp directory and artifacts are file://, so
// nothing here needs a network or a credential.
func newHarnessWithBlobCap(t *testing.T, blobMax int64, items ...fakeItem) *harness {
	t.Helper()
	dir := t.TempDir()
	ctx := t.Context()

	store, err := state.Open(ctx, state.Options{Driver: state.DriverSQLite, Path: filepath.Join(dir, "state.db")})
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating state: %v", err)
	}

	artDir := filepath.Join(dir, "artifacts")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}
	arts, err := artifact.Open(ctx, artifact.Options{
		URL:              "file://" + artDir,
		ShardTargetBytes: 1 << 20,
		BlobMaxBytes:     blobMax,
		Compression:      artifact.CompressionNone,
	})
	if err != nil {
		t.Fatalf("opening artifacts: %v", err)
	}
	t.Cleanup(func() { _ = arts.Close() })

	return &harness{t: t, ctx: ctx, conn: &fakeConnector{items: items}, state: store, arts: arts, dir: artDir}
}

// deps builds the dependency set.
func (h *harness) deps() Deps {
	return Deps{State: h.state, Artifacts: h.arts, Connector: h.conn}
}

// config builds a full-scope run configuration.
func (h *harness) config() RunConfig {
	return RunConfig{
		Source:      testSource,
		Datatype:    testDatatype,
		Scope:       artifact.ScopeFull,
		Concurrency: 4,
		DomainHash:  testDomain,
		ConfigHash:  testConfig,
		Producer:    testProducer,
	}
}

// run executes a fetch with the given configuration adjustments applied.
func (h *harness) run(adjust ...func(*RunConfig)) *Report {
	h.t.Helper()
	rc := h.config()
	for _, fn := range adjust {
		fn(&rc)
	}
	report, err := Run(h.ctx, h.deps(), rc)
	if err != nil {
		h.t.Fatalf("Run() = %v", err)
	}
	return report
}

// checkpoint records an item in state the way a successful enrichment run leaves it, which is what
// lets the level-0 and level-1 guards skip it next time. It writes the guards directly rather than
// through CheckpointItem, because that method also completes a work row and there is no pipeline run
// here to own one.
func (h *harness) checkpoint(item fakeItem, blobs map[string]string) {
	h.t.Helper()
	err := h.state.PutItem(h.ctx, testDatatype, state.Item{
		ID: item.id, Source: testSource, Fingerprint: item.fingerprint,
		Metadata: map[string]string{"full_name": item.id},
	})
	if err != nil {
		h.t.Fatalf("recording item %s: %v", item.id, err)
	}

	fragments := make([]state.FragmentState, 0, len(item.fragments))
	for _, key := range sortedKeys(item.fragments) {
		content := item.fragments[key]
		fragments = append(fragments, state.FragmentState{
			Key:         key,
			Fingerprint: contentHash(content),
			BlobRef:     blobs[key],
			SizeBytes:   int64(len(content)),
			Tier:        3,
		})
	}
	if err := h.state.PutFragments(h.ctx, testDatatype, item.id, fragments); err != nil {
		h.t.Fatalf("recording fragments of %s: %v", item.id, err)
	}
}

// blobDigests returns the blob digest of each of an item's fragments, as the artifact store would
// compute it.
func blobDigests(item fakeItem) map[string]string {
	out := make(map[string]string, len(item.fragments))
	for key, content := range item.fragments {
		out[key] = contentHash(content)
	}
	return out
}

// latestManifest reads back the newest committed run for the test datatype.
func (h *harness) latestManifest() *artifact.Manifest {
	h.t.Helper()
	run, err := h.arts.OpenRun(h.ctx, testSource, testDatatype, artifact.LatestRun)
	if err != nil {
		h.t.Fatalf("opening the latest run: %v", err)
	}
	return run.Manifest
}

// records reads back every record of the newest committed run, keyed by item ID.
func (h *harness) records() map[string]*artifact.Record {
	h.t.Helper()
	run, err := h.arts.OpenRun(h.ctx, testSource, testDatatype, artifact.LatestRun)
	if err != nil {
		h.t.Fatalf("opening the latest run: %v", err)
	}
	out := map[string]*artifact.Record{}
	if err := run.Records(h.ctx, func(rec *artifact.Record) error {
		copied := *rec
		out[rec.ItemID] = &copied
		return nil
	}); err != nil {
		h.t.Fatalf("reading records: %v", err)
	}
	return out
}

// item builds a scripted item with two fragments.
func item(id, version string, keys ...string) fakeItem {
	if len(keys) == 0 {
		keys = []string{"README.md", "main.go"}
	}
	fragments := make(map[string]string, len(keys))
	for _, key := range keys {
		fragments[key] = fmt.Sprintf("// %s at %s\n", key, version)
	}
	return fakeItem{id: id, fingerprint: id + ":" + version, fragments: fragments}
}

func contentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
