// Resolution cases: what a reference injects, what it records, and when it reports a change.
//
// The change cases are the load-bearing ones. A digest that moved when nothing did would pay
// for a regeneration on every run, and a digest that failed to move when the referent changed
// would leave the referring view stale forever — the exact failure D12 exists to prevent.
package refs

import (
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

func TestResolveInjectsFragmentPayload(t *testing.T) {
	store := openStore(t)
	seedReferent(t, store, "a terraform module registry", "publishes modules")
	set := ingetSet(t, store, InjectFragment)

	res := resolveItem(t, set, store, repoID)

	if len(res.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(res.Entries))
	}
	entry := res.Entries[0]
	if entry.Key != EntryPrefix+"linked_repo" {
		t.Errorf("entry key = %q, want %q", entry.Key, EntryPrefix+"linked_repo")
	}
	for _, want := range []string{"a terraform module registry", "publishes modules", repoID} {
		if !strings.Contains(entry.Content, want) {
			t.Errorf("entry content is missing %q:\n%s", want, entry.Content)
		}
	}
	if len(res.Metadata) != 0 {
		t.Errorf("metadata = %v, want nothing for a fragment-injected reference", res.Metadata)
	}
	if res.Digest != "" {
		t.Errorf("digest = %q, want empty: a fragment payload is in the document", res.Digest)
	}

	want := IngetKey(referent, repoID)
	if len(res.RelatedKeys) != 1 || res.RelatedKeys[0] != want {
		t.Errorf("related keys = %v, want [%s]", res.RelatedKeys, want)
	}
	if len(res.Edges) != 1 || res.Edges[0].Key != want || res.Edges[0].Fingerprint == "" {
		t.Errorf("edges = %+v, want one resolved edge for %s", res.Edges, want)
	}
	if len(res.ChangedKeys) != 1 {
		t.Errorf("changed keys = %v, want the reference to read as changed on first resolution", res.ChangedKeys)
	}
}

func TestResolveInjectsMetadataPayload(t *testing.T) {
	store := openStore(t)
	seedReferent(t, store, "a terraform module registry", "publishes modules")
	set := ingetSet(t, store, InjectMetadata)

	res := resolveItem(t, set, store, repoID)

	if got := res.Metadata["linked_repo.description"]; got != "a terraform module registry" {
		t.Errorf("metadata description = %q", got)
	}
	if got := res.Metadata["linked_repo.view:role"]; got != "publishes modules" {
		t.Errorf("metadata view = %q", got)
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries = %v, want nothing for a metadata-injected reference", res.Entries)
	}
	// Metadata reaches every prompt, so the change is a flag and the payload contributes a
	// digest to every view's level-2 key instead of a scoped compose entry.
	if !res.MetadataChanged {
		t.Error("MetadataChanged = false, want true on first resolution")
	}
	if res.Digest == "" {
		t.Error("digest is empty; a metadata payload has nowhere else to be guarded")
	}
	if len(res.ChangedKeys) != 0 {
		t.Errorf("changed keys = %v, want none: no glob can scope metadata", res.ChangedKeys)
	}
}

func TestResolveReportsChangeOnlyWhenThePayloadMoves(t *testing.T) {
	store := openStore(t)
	seedReferent(t, store, "first", "role one")
	set := ingetSet(t, store, InjectFragment)
	from := state.ItemKey{Datatype: referrer, ItemID: "42"}

	first := resolveItem(t, set, store, repoID)
	if err := store.PutRefs(t.Context(), from, first.Edges); err != nil {
		t.Fatalf("persisting edges: %v", err)
	}

	// Same referent, untouched: no change, so no view regenerates and no tokens are spent.
	if again := resolveItem(t, set, store, repoID); len(again.ChangedKeys) != 0 {
		t.Errorf("changed keys = %v, want none for an unchanged referent", again.ChangedKeys)
	}

	// The referent's view text changes, which is what a referrer pulls.
	seedReferent(t, store, "first", "role two")
	changed := resolveItem(t, set, store, repoID)
	if len(changed.ChangedKeys) != 1 {
		t.Fatalf("changed keys = %v, want the reference to be reported as changed", changed.ChangedKeys)
	}
	if changed.Edges[0].Fingerprint == first.Edges[0].Fingerprint {
		t.Error("the digest did not move when the referent's view text did")
	}
}

func TestResolveRecordsAnEdgeForAnAbsentReferent(t *testing.T) {
	store := openStore(t)
	set := ingetSet(t, store, InjectFragment)

	res := resolveItem(t, set, store, "acme/not-fetched-yet")

	// The edge is the whole point: without it the referent's first run has nothing to walk
	// back to, and this item would never learn that its reference became resolvable.
	if len(res.Edges) != 1 {
		t.Fatalf("edges = %+v, want one unresolved edge", res.Edges)
	}
	if res.Edges[0].Fingerprint != "" {
		t.Errorf("fingerprint = %q, want empty for an unresolved referent", res.Edges[0].Fingerprint)
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries = %v, want nothing injected for an unresolved referent", res.Entries)
	}
}

func TestNewReturnsNilForNoReferences(t *testing.T) {
	set, err := New(nil, openStore(t), noSecret)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if set != nil {
		t.Error("New returned a set for a datatype that declares no references")
	}
}

func TestNewRejectsAnUnknownResolver(t *testing.T) {
	_, err := New([]config.Reference{{
		Name: "x", Resolver: "carrier-pigeon", KeyFrom: "metadata:x", InjectAs: InjectFragment,
	}}, openStore(t), noSecret)
	if err == nil {
		t.Fatal("expected an error for an unregistered resolver")
	}
}
