// Conformance cases for items: the level-0 guard and the tombstone lifecycle.
package state

import (
	"maps"
	"testing"
)

var itemCases = []storeCase{
	{
		name: "item round-trips through the store",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			want := Item{
				ID:           testItem,
				Source:       testSource,
				Fingerprint:  "fp-1",
				ComposedHash: "sha256:composed",
				Metadata:     map[string]string{"language": "Go", "stars": "3"},
				RunID:        testRun,
			}
			if err := h.PutItem(ctx, testDatatype, want); err != nil {
				t.Fatalf("PutItem: %v", err)
			}
			got, found, err := h.Item(ctx, testDatatype, testItem)
			if err != nil || !found {
				t.Fatalf("Item: found=%v err=%v", found, err)
			}
			if got.Source != want.Source || got.Fingerprint != want.Fingerprint ||
				got.ComposedHash != want.ComposedHash || got.RunID != want.RunID {
				t.Errorf("Item = %+v, want %+v", got, want)
			}
			if !maps.Equal(got.Metadata, want.Metadata) {
				t.Errorf("metadata = %v, want %v", got.Metadata, want.Metadata)
			}
		},
	},
	{
		name: "unwritten item is absent rather than an error",
		run: func(t *testing.T, h harness) {
			got, found, err := h.Item(t.Context(), testDatatype, "nobody/nothing")
			if err != nil {
				t.Fatalf("Item: %v", err)
			}
			if found {
				t.Errorf("Item found = true for an unwritten item, got %+v", got)
			}
		},
	},
	{
		name: "an unset composed hash reads back empty",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			got, _, err := h.Item(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Item: %v", err)
			}
			if got.ComposedHash != "" {
				t.Errorf("ComposedHash = %q, want empty", got.ComposedHash)
			}
		},
	},
	{
		name: "put overwrites the fingerprint of an existing item",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			seedItem(t, h, testItem, "fp-2")

			fingerprints, err := h.ItemFingerprints(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ItemFingerprints: %v", err)
			}
			if len(fingerprints) != 1 || fingerprints[testItem] != "fp-2" {
				t.Errorf("fingerprints = %v, want one entry at fp-2", fingerprints)
			}
		},
	},
	{
		name: "fingerprints are scoped to one datatype",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			other := Item{ID: "1234", Source: "monday", Fingerprint: "fp-x"}
			if err := h.PutItem(ctx, "monday/item", other); err != nil {
				t.Fatalf("PutItem monday: %v", err)
			}
			fingerprints, err := h.ItemFingerprints(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ItemFingerprints: %v", err)
			}
			if _, present := fingerprints["1234"]; present || len(fingerprints) != 1 {
				t.Errorf("fingerprints = %v, want only the %s item", fingerprints, testDatatype)
			}
		},
	},
	{
		name: "a tombstoned item disappears and reappears on write",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h, testItem, "fp-1")
			if err := h.Tombstone(ctx, testDatatype, []string{testItem}); err != nil {
				t.Fatalf("Tombstone: %v", err)
			}
			fingerprints, err := h.ItemFingerprints(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ItemFingerprints: %v", err)
			}
			if len(fingerprints) != 0 {
				t.Errorf("fingerprints after tombstone = %v, want empty", fingerprints)
			}
			if _, found, err := h.Item(ctx, testDatatype, testItem); err != nil || found {
				t.Errorf("Item after tombstone: found=%v err=%v, want not found", found, err)
			}

			// An item that comes back is the same item: writing it clears the tombstone,
			// so its cached derivations stay reachable.
			seedItem(t, h, testItem, "fp-1")
			if _, found, err := h.Item(ctx, testDatatype, testItem); err != nil || !found {
				t.Errorf("Item after revival: found=%v err=%v, want found", found, err)
			}
		},
	},
	{
		name: "tombstoning nothing is not an error",
		run: func(t *testing.T, h harness) {
			if err := h.Tombstone(t.Context(), testDatatype, nil); err != nil {
				t.Errorf("Tombstone(nil): %v", err)
			}
		},
	},
	{
		name: "an item without an ID is rejected",
		run: func(t *testing.T, h harness) {
			if err := h.PutItem(t.Context(), testDatatype, Item{Fingerprint: "fp"}); err == nil {
				t.Error("PutItem with no ID: want an error")
			}
		},
	},
}
