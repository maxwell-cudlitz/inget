// Cascade cases: what a changed referent marks, how deep it travels, and what the cap defers.
//
// The cycle case is the one the design's edge-case table names. It is expressed as depth
// accounting rather than as a visited set: a mark carries the depth it was made at, so two
// items referencing each other stop after enrich.max_reference_depth hops instead of marking
// each other forever.
package refs

import (
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/state"
)

// edgeTo returns an edge from an item to one persisted record.
func edgeTo(name, datatype, itemID string) state.RefEdge {
	return state.RefEdge{Name: name, Kind: KindInget, Key: IngetKey(datatype, itemID), Fingerprint: "sha256:old"}
}

// seedEdge records that from references the given record.
func seedEdge(t *testing.T, store state.Store, from state.ItemKey, edges ...state.RefEdge) {
	t.Helper()
	if err := store.PutRefs(t.Context(), from, edges); err != nil {
		t.Fatalf("seeding edges: %v", err)
	}
}

func TestCascadeMarksTheItemsThatReferenceAChangedRecord(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	one := state.ItemKey{Datatype: referrer, ItemID: "1"}
	two := state.ItemKey{Datatype: referrer, ItemID: "2"}
	other := state.ItemKey{Datatype: referrer, ItemID: "3"}
	seedEdge(t, store, one, edgeTo("linked_repo", referent, repoID))
	seedEdge(t, store, two, edgeTo("linked_repo", referent, repoID))
	seedEdge(t, store, other, edgeTo("linked_repo", referent, "acme/untouched"))

	marked, err := Cascade(ctx, store, referent, repoID, 0, 2)
	if err != nil {
		t.Fatalf("Cascade: %v", err)
	}
	if marked != 2 {
		t.Errorf("marked = %d, want 2", marked)
	}

	stale, err := store.StaleReferrers(ctx, referrer)
	if err != nil {
		t.Fatalf("StaleReferrers: %v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("stale = %+v, want the two items referencing the changed record", stale)
	}
	for _, s := range stale {
		if s.ItemID == other.ItemID {
			t.Error("an item referencing a different record was marked")
		}
		if s.Depth != 1 {
			t.Errorf("depth = %d, want 1 for a directly referencing item", s.Depth)
		}
	}
}

func TestCascadeStopsAtTheDepthBound(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	seedEdge(t, store, state.ItemKey{Datatype: referrer, ItemID: "1"},
		edgeTo("linked_repo", referent, repoID))

	// An item already processed at the bound cascades no further, which is what terminates a
	// cycle: X marks Y at 1, Y marks X at 2, and X at 2 marks nothing.
	marked, err := Cascade(ctx, store, referent, repoID, 2, 2)
	if err != nil {
		t.Fatalf("Cascade: %v", err)
	}
	if marked != 0 {
		t.Errorf("marked = %d, want 0 at the depth bound", marked)
	}
	stale, err := store.StaleReferrers(ctx, referrer)
	if err != nil {
		t.Fatalf("StaleReferrers: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("stale = %+v, want nothing marked at the bound", stale)
	}
}

func TestCascadeCycleTerminates(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	const maxDepth = 2
	x := state.ItemKey{Datatype: referrer, ItemID: "x"}
	y := state.ItemKey{Datatype: referrer, ItemID: "y"}
	// x references y and y references x: the cycle the design bounds by depth.
	seedEdge(t, store, x, edgeTo("peer", referrer, y.ItemID))
	seedEdge(t, store, y, edgeTo("peer", referrer, x.ItemID))

	depth, hops := 0, 0
	for hops < 10 {
		marked, err := Cascade(ctx, store, referrer, x.ItemID, depth, maxDepth)
		if err != nil {
			t.Fatalf("Cascade: %v", err)
		}
		if marked == 0 {
			break
		}
		hops++
		depth++
	}
	if hops > maxDepth {
		t.Errorf("cascade travelled %d hops, want at most %d", hops, maxDepth)
	}
}

func TestMarkKeepsTheShallowestDepth(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	item := state.ItemKey{Datatype: referrer, ItemID: "1"}
	seedEdge(t, store, item, edgeTo("linked_repo", referent, repoID))

	if _, err := Cascade(ctx, store, referent, repoID, 1, 5); err != nil {
		t.Fatalf("Cascade: %v", err)
	}
	if _, err := Cascade(ctx, store, referent, repoID, 0, 5); err != nil {
		t.Fatalf("Cascade: %v", err)
	}
	stale, err := store.StaleReferrers(ctx, referrer)
	if err != nil {
		t.Fatalf("StaleReferrers: %v", err)
	}
	if len(stale) != 1 || stale[0].Depth != 1 {
		t.Fatalf("stale = %+v, want depth 1: the shortest path decides how far the cascade may go", stale)
	}
}

func TestReResolutionClearsTheMark(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	item := state.ItemKey{Datatype: referrer, ItemID: "1"}
	seedEdge(t, store, item, edgeTo("linked_repo", referent, repoID))
	if _, err := Cascade(ctx, store, referent, repoID, 0, 2); err != nil {
		t.Fatalf("Cascade: %v", err)
	}

	// Rewriting the edge set is what an item's checkpoint does, and it is the only thing that
	// clears the mark.
	seedEdge(t, store, item, edgeTo("linked_repo", referent, repoID))

	stale, err := store.StaleReferrers(ctx, referrer)
	if err != nil {
		t.Fatalf("StaleReferrers: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("stale = %+v, want nothing after re-resolution", stale)
	}
}

func TestWithinCapDefersTheRemainder(t *testing.T) {
	items := []string{"a", "b", "c", "d"}

	if got := WithinCap(t.Context(), referrer, items, 0); len(got) != 4 {
		t.Errorf("a cap of 0 took %d items, want all of them", len(got))
	}
	if got := WithinCap(t.Context(), referrer, items, 10); len(got) != 4 {
		t.Errorf("a cap above the count took %d items, want all of them", len(got))
	}
	got := WithinCap(t.Context(), referrer, items, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("WithinCap = %v, want the first two", got)
	}
}
