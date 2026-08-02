// Shared fixtures for the refs tests.
//
// State is a real sqlite store rather than a stub. The behaviour under test is the interaction
// between resolution and persistence — a digest written by one run compared against by the
// next, a mark set by one datatype consumed by another — and a stub of the Store interface
// would only prove that the stub agrees with itself.
package refs

import (
	"path/filepath"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/state"
)

const (
	referrer = "monday/item"
	referent = "github/repo"
	repoID   = "acme/thing"
)

// openStore returns a migrated sqlite state store in a temp directory.
func openStore(t *testing.T) state.Store {
	t.Helper()
	store, err := state.Open(t.Context(), state.Options{
		Driver: state.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatalf("migrating state: %v", err)
	}
	return store
}

// seedReferent writes a referenced record: an item with metadata and one generated view.
func seedReferent(t *testing.T, store state.Store, description, roleText string) {
	t.Helper()
	ctx := t.Context()
	err := store.PutItem(ctx, referent, state.Item{
		ID:          repoID,
		Source:      "github",
		Fingerprint: "fp-1",
		Metadata:    map[string]string{"description": description},
	})
	if err != nil {
		t.Fatalf("seeding referent: %v", err)
	}
	err = store.PutViewState(ctx, referent, repoID, state.ViewState{
		Name:         "role",
		InputHash:    "sha256:input",
		Text:         roleText,
		EmbeddedHash: "sha256:embedded",
		Signature:    "sha256:sig",
	})
	if err != nil {
		t.Fatalf("seeding referent view: %v", err)
	}
}

// noSecret is the secret resolver for references that need no credential.
func noSecret(config.SecretRef) (string, error) { return "", nil }

// ingetSet builds a one-reference set pointing at the referent datatype.
func ingetSet(t *testing.T, store state.Store, injectAs string) *Set {
	t.Helper()
	set, err := New([]config.Reference{{
		Name:     "linked_repo",
		Resolver: KindInget,
		Datatype: referent,
		KeyFrom:  "metadata:linked",
		Fields:   []string{"description", "view:role"},
		InjectAs: injectAs,
	}}, store, noSecret)
	if err != nil {
		t.Fatalf("building reference set: %v", err)
	}
	return set
}

// resolveItem resolves one item's references against its stored edges.
func resolveItem(t *testing.T, set *Set, store state.Store, key string) Resolution {
	t.Helper()
	from := state.ItemKey{Datatype: referrer, ItemID: "42"}
	stored, err := store.Refs(t.Context(), from)
	if err != nil {
		t.Fatalf("reading stored edges: %v", err)
	}
	in := stubInput(map[string]string{"linked": key}, nil)
	in.Datatype, in.ItemID = from.Datatype, from.ItemID
	in.Stored = stored

	res, err := set.Resolve(t.Context(), in)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	return res
}
