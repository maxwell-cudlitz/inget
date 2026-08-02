// Fixtures for the reindex tests.
//
// State is a real migrated sqlite store, because the pass is mostly an integration with it: work
// claiming, run resumption and view guards against the actual schema. The destination is a fake —
// what matters is which rows it was handed and whether it was pruned, not how pgvector stores
// them, which internal/destination's integration suite covers. The embedder is the hash-derived
// FakeEmbedder, whose texts-embedded counter is exactly the "did it re-embed" assertion.
package reindex

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/destination"
	"github.com/maxwellcudlitz/inget/internal/model"
	"github.com/maxwellcudlitz/inget/internal/state"
)

const (
	testDatatype = "github/repo"
	testSource   = "github"
	testDest     = "vectors"
	testDims     = 8
	oldModel     = "test/embedder-v1"
	newModel     = "test/embedder-v2"
)

// testViews is the view set every fixture item carries.
var testViews = []string{"role", "stack"}

// fakeDest records what it was asked to write, rebind and prune.
type fakeDest struct {
	mu       sync.Mutex
	rows     []destination.Row
	rebinds  int
	rebound  bool // what RebindModel reports
	pruned   []string
	prune    int // rows PruneStaleVectors reports deleting
	onWrite  func()
	bulkCall int
}

func (d *fakeDest) Migrate(context.Context) error                          { return nil }
func (d *fakeDest) Close() error                                           { return nil }
func (d *fakeDest) AssertModel(context.Context, string, int, string) error { return nil }

func (d *fakeDest) RebindModel(context.Context, string, int, string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rebinds++
	return d.rebound, nil
}

func (d *fakeDest) PruneStaleVectors(_ context.Context, datatype string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruned = append(d.pruned, datatype)
	return d.prune, nil
}

func (d *fakeDest) Upsert(ctx context.Context, rows []destination.Row) error {
	return d.BulkLoad(ctx, rows)
}

func (d *fakeDest) BulkLoad(_ context.Context, rows []destination.Row) error {
	d.mu.Lock()
	d.rows = append(d.rows, rows...)
	d.bulkCall++
	onWrite := d.onWrite
	d.mu.Unlock()
	if onWrite != nil {
		onWrite()
	}
	return nil
}

func (d *fakeDest) DeleteItem(context.Context, string, string) error { return nil }

func (d *fakeDest) Search(context.Context, destination.SearchQuery) ([]destination.SearchResult, error) {
	return nil, nil
}

// written returns a copy of the rows handed to the destination.
func (d *fakeDest) written() []destination.Row {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]destination.Row(nil), d.rows...)
}

// prunedDatatypes returns the datatypes the destination was pruned for.
func (d *fakeDest) prunedDatatypes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.pruned...)
}

var _ destination.Destination = (*fakeDest)(nil)

// harness is one test's dependencies plus the seeded item identifiers.
type harness struct {
	deps  Deps
	dest  *fakeDest
	emb   *model.FakeEmbedder
	store state.Store
	items []string
}

// newHarness migrates a sqlite state store, seeds items whose views were embedded by the previous
// model, and wires a fake destination and the new embedder.
func newHarness(t *testing.T, items int) harness {
	t.Helper()
	store := openState(t)
	ids := make([]string, 0, items)
	for i := range items {
		id := "acme/repo-" + strconv.Itoa(i)
		ids = append(ids, id)
		seedItem(t, store, id)
	}
	dest := &fakeDest{rebound: true}
	emb := model.NewFakeEmbedder(newModel, testDims)
	return harness{
		deps: Deps{
			State:        store,
			Embedder:     emb,
			Destinations: map[string]destination.Destination{testDest: dest},
		},
		dest: dest, emb: emb, store: store, items: ids,
	}
}

// options returns a full-scope reindex request over the fixture datatype.
func (h harness) options() Options {
	return Options{
		Datatype:       testDatatype,
		Destinations:   []string{testDest},
		MetadataFields: map[string]string{"related_keys": "${references.board.resolved_keys}"},
		ConfigHash:     "sha256:config",
		BatchSize:      2,
	}
}

// openState opens and migrates a temporary sqlite state store.
func openState(t *testing.T) state.Store {
	t.Helper()
	store, err := state.Open(t.Context(), state.Options{
		Driver: state.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("opening the state store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("closing the state store: %v", err)
		}
	})
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatalf("migrating the state store: %v", err)
	}
	return store
}

// seedItem writes one item, one reference edge and one view per name, each recorded as embedded by
// the previous model — which is the state a rebind leaves behind.
func seedItem(t *testing.T, store state.Store, itemID string) {
	t.Helper()
	ctx := t.Context()
	it := state.Item{
		ID: itemID, Source: testSource, Fingerprint: "fp-" + itemID,
		Metadata: map[string]string{"name": itemID, "language": "Go"},
	}
	if err := store.PutItem(ctx, testDatatype, it); err != nil {
		t.Fatalf("PutItem %s: %v", itemID, err)
	}
	edges := []state.RefEdge{{Name: "board", Kind: "inget", Key: "monday/" + itemID, Fingerprint: "d-1"}}
	if err := store.PutRefs(ctx, state.ItemKey{Datatype: testDatatype, ItemID: itemID}, edges); err != nil {
		t.Fatalf("PutRefs %s: %v", itemID, err)
	}
	for _, name := range testViews {
		v := state.ViewState{
			Name:         name,
			InputHash:    "in:" + itemID + ":" + name,
			Text:         itemID + " " + name + " text",
			EmbeddedHash: "sha256:previous",
			Model:        oldModel,
			Dims:         testDims,
			Signature:    "fake:" + oldModel + ",dimensions=" + strconv.Itoa(testDims),
		}
		if err := store.PutViewState(ctx, testDatatype, itemID, v); err != nil {
			t.Fatalf("PutViewState %s/%s: %v", itemID, name, err)
		}
	}
}

// viewsOf reads one item's persisted views.
func viewsOf(t *testing.T, store state.Store, itemID string) map[string]state.ViewState {
	t.Helper()
	views, err := store.ViewState(t.Context(), testDatatype, itemID)
	if err != nil {
		t.Fatalf("ViewState %s: %v", itemID, err)
	}
	return views
}
