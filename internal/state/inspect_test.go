// Conformance cases for the introspection reads behind `inget state show`.
package state

import (
	"testing"
)

var inspectCases = []storeCase{
	{
		name: "counts report every cascade level",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h.Store, testItem, "fp-1")
			seedItem(t, h.Store, "acme/gone", "fp-2")
			if err := h.Tombstone(ctx, testDatatype, []string{"acme/gone"}); err != nil {
				t.Fatalf("Tombstone: %v", err)
			}
			putFragmentSet(t, h.Store, testItem, fragment("README.md", "sha-a"), fragment("gone.md", "sha-b"))
			putFragmentSet(t, h.Store, testItem, fragment("README.md", "sha-a"))
			if err := h.PutDerivation(ctx, derivation("key:a", "sig", "output")); err != nil {
				t.Fatalf("PutDerivation: %v", err)
			}
			for _, v := range []ViewState{
				{Name: "purpose", InputHash: "in-1", Text: "text", EmbeddedHash: "emb-1",
					Model: "test/embedder", Dims: 4, Signature: "sig"},
				{Name: "pending", InputHash: "in-2", Text: "text", Signature: "sig"},
			} {
				if err := h.PutViewState(ctx, testDatatype, testItem, v); err != nil {
					t.Fatalf("PutViewState %s: %v", v.Name, err)
				}
			}
			from := ItemKey{Datatype: testDatatype, ItemID: testItem}
			edges := []RefEdge{{Name: "board", Kind: "inget", Key: "acme/other", Fingerprint: "d-1"}}
			if err := h.PutRefs(ctx, from, edges); err != nil {
				t.Fatalf("PutRefs: %v", err)
			}
			if _, err := h.MarkRefsStale(ctx, "inget", "acme/other", 1); err != nil {
				t.Fatalf("MarkRefsStale: %v", err)
			}

			got, err := h.Counts(ctx, testDatatype)
			if err != nil {
				t.Fatalf("Counts: %v", err)
			}
			want := Counts{
				Items: 1, Tombstoned: 1, Fragments: 2, MissingFragments: 1,
				Derivations: 1, Views: 2, EmbeddedViews: 1, Refs: 1, StaleRefs: 1,
			}
			if got != want {
				t.Errorf("Counts = %+v, want %+v", got, want)
			}
		},
	},
	{
		name: "counts of an unknown datatype are zero rather than an error",
		run: func(t *testing.T, h harness) {
			got, err := h.Counts(t.Context(), "monday/item")
			if err != nil {
				t.Fatalf("Counts: %v", err)
			}
			if got != (Counts{}) {
				t.Errorf("Counts = %+v, want the zero value", got)
			}
		},
	},
	{
		name: "recent runs carry status, stats and both timestamps",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedRun(t, h.Store, testRun)
			if err := h.FinishRun(ctx, testRun, RunOK, map[string]int{"items_processed": 3}); err != nil {
				t.Fatalf("FinishRun: %v", err)
			}

			runs, err := h.RecentRuns(ctx, "", 10)
			if err != nil {
				t.Fatalf("RecentRuns: %v", err)
			}
			if len(runs) != 1 {
				t.Fatalf("RecentRuns returned %d runs, want 1", len(runs))
			}
			got := runs[0]
			switch {
			case got.ID != testRun:
				t.Errorf("run ID = %q, want %q", got.ID, testRun)
			case got.Status != RunOK:
				t.Errorf("status = %q, want %q", got.Status, RunOK)
			case got.Datatype != testDatatype:
				t.Errorf("datatype = %q, want %q", got.Datatype, testDatatype)
			case got.StartedAt.IsZero():
				t.Error("started_at is zero; the store writes it with the insert")
			case got.FinishedAt.IsZero():
				t.Error("finished_at is zero on a finished run")
			case got.FinishedAt.Before(got.StartedAt):
				t.Errorf("finished_at %v precedes started_at %v", got.FinishedAt, got.StartedAt)
			case got.Stats == "" || got.Stats == "{}":
				t.Errorf("stats = %q, want the JSON FinishRun wrote", got.Stats)
			}
		},
	},
	{
		name: "an unfinished run has no finish time",
		run: func(t *testing.T, h harness) {
			seedRun(t, h.Store, testRun)
			runs, err := h.RecentRuns(t.Context(), testDatatype, 10)
			if err != nil {
				t.Fatalf("RecentRuns: %v", err)
			}
			if len(runs) != 1 {
				t.Fatalf("RecentRuns returned %d runs, want 1", len(runs))
			}
			if !runs[0].FinishedAt.IsZero() {
				t.Errorf("finished_at = %v on a running run, want the zero time", runs[0].FinishedAt)
			}
		},
	},
	{
		name: "the datatype filter and the limit both apply",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedRun(t, h.Store, testRun)
			other := Run{ID: "01K1QZ8Q0000000000000000AB", Binary: "inget", Datatype: "monday/item",
				Scope: ScopeFull, ConfigHash: testConfig}
			if err := h.StartRun(ctx, other); err != nil {
				t.Fatalf("StartRun: %v", err)
			}

			filtered, err := h.RecentRuns(ctx, "monday/item", 10)
			if err != nil {
				t.Fatalf("RecentRuns: %v", err)
			}
			if len(filtered) != 1 || filtered[0].ID != other.ID {
				t.Errorf("RecentRuns(monday/item) = %+v, want only %s", filtered, other.ID)
			}

			limited, err := h.RecentRuns(ctx, "", 1)
			if err != nil {
				t.Fatalf("RecentRuns: %v", err)
			}
			// Newest first, and run IDs are ULIDs, so the newest is the larger identifier.
			if len(limited) != 1 || limited[0].ID != other.ID {
				t.Errorf("RecentRuns(limit 1) = %+v, want only the newest run %s", limited, other.ID)
			}

			if _, err := h.RecentRuns(ctx, "", 0); err == nil {
				t.Error("RecentRuns(limit 0): want an error")
			}
		},
	},
}
