// Conformance cases for fragments: the level-1 guard and the GC counter.
package state

import "testing"

// fragmentSet is the fixture item's fragment set.
func fragmentSet() []FragmentState {
	return []FragmentState{
		{Key: "README.md", Fingerprint: "fp-readme", BlobRef: "sha-readme", SizeBytes: 120, Tier: 0},
		{Key: "cmd/main.go", Fingerprint: "fp-main", BlobRef: "sha-main", SizeBytes: 340, Tier: 1},
		{Key: "go.mod", Fingerprint: "fp-mod", SizeBytes: 40, Tier: 2},
	}
}

var fragmentCases = []storeCase{
	{
		name: "fragments round-trip through the store",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			if err := h.PutFragments(ctx, testDatatype, testItem, fragmentSet()); err != nil {
				t.Fatalf("PutFragments: %v", err)
			}
			got, err := h.Fragments(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Fragments: %v", err)
			}
			if len(got) != 3 {
				t.Fatalf("Fragments returned %d entries, want 3", len(got))
			}
			readme := got["README.md"]
			if readme.Fingerprint != "fp-readme" || readme.BlobRef != "sha-readme" ||
				readme.SizeBytes != 120 || readme.Tier != 0 || readme.MissingRuns != 0 {
				t.Errorf("README.md = %+v, want the seeded values", readme)
			}
			// A fragment with no blob — truncated, or empty content — reads back with an
			// empty reference rather than needing the caller to handle a null.
			if mod := got["go.mod"]; mod.BlobRef != "" {
				t.Errorf("go.mod BlobRef = %q, want empty", mod.BlobRef)
			}
		},
	},
	{
		name: "a changed fragment overwrites its fingerprint",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			if err := h.PutFragments(ctx, testDatatype, testItem, fragmentSet()); err != nil {
				t.Fatalf("PutFragments: %v", err)
			}
			changed := fragmentSet()
			changed[0].Fingerprint = "fp-readme-2"
			if err := h.PutFragments(ctx, testDatatype, testItem, changed); err != nil {
				t.Fatalf("PutFragments again: %v", err)
			}
			got, err := h.Fragments(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Fragments: %v", err)
			}
			if got["README.md"].Fingerprint != "fp-readme-2" || len(got) != 3 {
				t.Errorf("Fragments = %+v, want README.md at fp-readme-2 and 3 entries", got)
			}
		},
	},
	{
		name: "an absent fragment accumulates missing runs and resets when it returns",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			full := fragmentSet()
			if err := h.PutFragments(ctx, testDatatype, testItem, full); err != nil {
				t.Fatalf("PutFragments: %v", err)
			}
			// Two runs in which go.mod was deleted upstream.
			for range 2 {
				if err := h.PutFragments(ctx, testDatatype, testItem, full[:2]); err != nil {
					t.Fatalf("PutFragments without go.mod: %v", err)
				}
			}
			got, err := h.Fragments(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Fragments: %v", err)
			}
			if got["go.mod"].MissingRuns != 2 {
				t.Errorf("go.mod MissingRuns = %d, want 2", got["go.mod"].MissingRuns)
			}
			if got["README.md"].MissingRuns != 0 {
				t.Errorf("README.md MissingRuns = %d, want 0 — it was present every run",
					got["README.md"].MissingRuns)
			}

			// The file comes back; its counter starts over, so retention counts
			// consecutive absences rather than total ones.
			if err := h.PutFragments(ctx, testDatatype, testItem, full); err != nil {
				t.Fatalf("PutFragments with go.mod restored: %v", err)
			}
			got, err = h.Fragments(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Fragments: %v", err)
			}
			if got["go.mod"].MissingRuns != 0 {
				t.Errorf("go.mod MissingRuns after restore = %d, want 0", got["go.mod"].MissingRuns)
			}
		},
	},
	{
		name: "an empty fragment set still counts every fragment as missing",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			if err := h.PutFragments(ctx, testDatatype, testItem, fragmentSet()); err != nil {
				t.Fatalf("PutFragments: %v", err)
			}
			if err := h.PutFragments(ctx, testDatatype, testItem, nil); err != nil {
				t.Fatalf("PutFragments(nil): %v", err)
			}
			got, err := h.Fragments(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Fragments: %v", err)
			}
			for key, frag := range got {
				if frag.MissingRuns != 1 {
					t.Errorf("%s MissingRuns = %d, want 1", key, frag.MissingRuns)
				}
			}
		},
	},
	{
		name: "a fragment without a key is rejected and writes nothing",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			bad := append(fragmentSet(), FragmentState{Fingerprint: "fp-nameless"})
			if err := h.PutFragments(ctx, testDatatype, testItem, bad); err == nil {
				t.Fatal("PutFragments with a keyless fragment: want an error")
			}
			got, err := h.Fragments(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Fragments: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("Fragments = %+v, want nothing written", got)
			}
		},
	},
}
