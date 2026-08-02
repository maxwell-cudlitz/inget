// Conformance cases for the derivation cache and for view state: the payload of the level-1
// guard and the level-2 and level-3 guards themselves.
package state

import (
	"slices"
	"testing"
)

// derivation builds a cached derivation for one fragment under one signature.
func derivation(cacheKey, signature, output string) Derivation {
	return Derivation{
		CacheKey:  cacheKey,
		Datatype:  testDatatype,
		ItemID:    testItem,
		FragKey:   "README.md",
		Signature: signature,
		Output:    output,
	}
}

var derivationCases = []storeCase{
	{
		name: "an uncached key is a miss",
		run: func(t *testing.T, h harness) {
			output, found, err := h.Derivation(t.Context(), "sha256:absent")
			if err != nil {
				t.Fatalf("Derivation: %v", err)
			}
			if found || output != "" {
				t.Errorf("Derivation = %q, %v; want a miss", output, found)
			}
		},
	},
	{
		name: "a cached derivation is returned",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutDerivation(ctx, derivation("sha256:a", "sig-1", "a summary")); err != nil {
				t.Fatalf("PutDerivation: %v", err)
			}
			output, found, err := h.Derivation(ctx, "sha256:a")
			if err != nil || !found {
				t.Fatalf("Derivation: found=%v err=%v", found, err)
			}
			if output != "a summary" {
				t.Errorf("Derivation = %q, want %q", output, "a summary")
			}
		},
	},
	{
		name: "a new signature is a different cache key, so the old output survives",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutDerivation(ctx, derivation("sha256:a", "sig-1", "old")); err != nil {
				t.Fatalf("PutDerivation: %v", err)
			}
			if err := h.PutDerivation(ctx, derivation("sha256:b", "sig-2", "new")); err != nil {
				t.Fatalf("PutDerivation: %v", err)
			}
			for key, want := range map[string]string{"sha256:a": "old", "sha256:b": "new"} {
				output, found, err := h.Derivation(ctx, key)
				if err != nil || !found {
					t.Fatalf("Derivation %s: found=%v err=%v", key, found, err)
				}
				if output != want {
					t.Errorf("Derivation %s = %q, want %q", key, output, want)
				}
			}
		},
	},
	{
		name: "rewriting a cache key replaces its output",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutDerivation(ctx, derivation("sha256:a", "sig-1", "first")); err != nil {
				t.Fatalf("PutDerivation: %v", err)
			}
			if err := h.PutDerivation(ctx, derivation("sha256:a", "sig-1", "second")); err != nil {
				t.Fatalf("PutDerivation again: %v", err)
			}
			output, _, err := h.Derivation(ctx, "sha256:a")
			if err != nil {
				t.Fatalf("Derivation: %v", err)
			}
			if output != "second" {
				t.Errorf("Derivation = %q, want %q", output, "second")
			}
		},
	},
	{
		name: "a derivation without a cache key is rejected",
		run: func(t *testing.T, h harness) {
			if err := h.PutDerivation(t.Context(), derivation("", "sig-1", "output")); err == nil {
				t.Error("PutDerivation with no cache key: want an error")
			}
		},
	},
}

var viewCases = []storeCase{
	{
		name: "view state round-trips through the store",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			want := ViewState{
				Name:         "role",
				InputHash:    "sha256:input",
				Text:         "This service ingests things.",
				EmbeddedHash: "sha256:embedded",
				Model:        "Qwen/Qwen3-Embedding-0.6B",
				Dims:         1024,
				Signature:    "sig-1",
			}
			if err := h.PutViewState(ctx, testDatatype, testItem, want); err != nil {
				t.Fatalf("PutViewState: %v", err)
			}
			got, err := h.ViewState(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("ViewState: %v", err)
			}
			if got["role"] != want {
				t.Errorf("ViewState[role] = %+v, want %+v", got["role"], want)
			}
		},
	},
	{
		name: "a generated but unembedded view reads back with empty embedding fields",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			v := ViewState{Name: "surface", InputHash: "sha256:input", Text: "text", Signature: "sig-1"}
			if err := h.PutViewState(ctx, testDatatype, testItem, v); err != nil {
				t.Fatalf("PutViewState: %v", err)
			}
			got, err := h.ViewState(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("ViewState: %v", err)
			}
			if got["surface"] != v {
				t.Errorf("ViewState[surface] = %+v, want %+v", got["surface"], v)
			}
		},
	},
	{
		name: "views are keyed by name within an item",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			for _, name := range []string{"role", "surface", "stack"} {
				v := ViewState{Name: name, InputHash: "sha256:" + name, Text: name, Signature: "sig-1"}
				if err := h.PutViewState(ctx, testDatatype, testItem, v); err != nil {
					t.Fatalf("PutViewState %s: %v", name, err)
				}
			}
			// Regenerating one view must not disturb the others.
			updated := ViewState{Name: "role", InputHash: "sha256:role-2", Text: "again", Signature: "sig-2"}
			if err := h.PutViewState(ctx, testDatatype, testItem, updated); err != nil {
				t.Fatalf("PutViewState role again: %v", err)
			}
			got, err := h.ViewState(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("ViewState: %v", err)
			}
			if len(got) != 3 || got["role"] != updated || got["stack"].InputHash != "sha256:stack" {
				t.Errorf("ViewState = %+v, want three views with only role updated", got)
			}
		},
	},
	{
		name: "ViewedItems lists only items with stored views, sorted",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			for _, id := range []string{"b/two", "a/one", "c/none"} {
				seedItem(t, h, id, "fp-1")
			}
			for _, id := range []string{"b/two", "a/one"} {
				v := ViewState{Name: "role", InputHash: "sha256:" + id, Text: "text", Signature: "sig-1"}
				if err := h.PutViewState(ctx, testDatatype, id, v); err != nil {
					t.Fatalf("PutViewState %s: %v", id, err)
				}
			}
			// A second view of an already-listed item must not duplicate it.
			second := ViewState{Name: "stack", InputHash: "sha256:stack", Text: "text", Signature: "sig-1"}
			if err := h.PutViewState(ctx, testDatatype, "a/one", second); err != nil {
				t.Fatalf("PutViewState a/one stack: %v", err)
			}
			got, err := h.ViewedItems(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ViewedItems: %v", err)
			}
			if !slices.Equal(got, []string{"a/one", "b/two"}) {
				t.Errorf("ViewedItems = %v, want [a/one b/two]", got)
			}
		},
	},
	{
		name: "a view without a name is rejected",
		run: func(t *testing.T, h harness) {
			err := h.PutViewState(t.Context(), testDatatype, testItem, ViewState{InputHash: "x"})
			if err == nil {
				t.Error("PutViewState with no name: want an error")
			}
		},
	},
}
