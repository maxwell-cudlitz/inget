// Conformance cases for reference edges and recorded signatures: the reverse-dependency
// lookup the cascade walks (D12) and the mass-invalidation detector (D2).
package state

import (
	"slices"
	"testing"
)

var refCases = []storeCase{
	{
		name: "reverse edges name every referring item",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			edge := RefEdge{Name: "linked_repo", Kind: "inget", Key: testItem, Fingerprint: "fp-1"}
			from := []ItemKey{
				{Datatype: "monday/item", ItemID: "111"},
				{Datatype: "monday/item", ItemID: "222"},
			}
			for _, f := range from {
				if err := h.PutRefs(ctx, f, []RefEdge{edge}); err != nil {
					t.Fatalf("PutRefs %s: %v", f.ItemID, err)
				}
			}
			got, err := h.ReferencedBy(ctx, "inget", testItem)
			if err != nil {
				t.Fatalf("ReferencedBy: %v", err)
			}
			if !slices.Equal(got, from) {
				t.Errorf("ReferencedBy = %v, want %v", got, from)
			}
		},
	},
	{
		name: "a key nothing references has no reverse edges",
		run: func(t *testing.T, h harness) {
			got, err := h.ReferencedBy(t.Context(), "inget", "nobody/nothing")
			if err != nil {
				t.Fatalf("ReferencedBy: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("ReferencedBy = %v, want nothing", got)
			}
		},
	},
	{
		name: "reverse edges are scoped by resolver kind",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			from := ItemKey{Datatype: "monday/item", ItemID: "111"}
			edges := []RefEdge{
				{Name: "linked_repo", Kind: "inget", Key: testItem},
				{Name: "ticket", Kind: "http", Key: testItem},
			}
			if err := h.PutRefs(ctx, from, edges); err != nil {
				t.Fatalf("PutRefs: %v", err)
			}
			for _, kind := range []string{"inget", "http"} {
				got, err := h.ReferencedBy(ctx, kind, testItem)
				if err != nil {
					t.Fatalf("ReferencedBy %s: %v", kind, err)
				}
				if len(got) != 1 || got[0] != from {
					t.Errorf("ReferencedBy %s = %v, want [%v]", kind, got, from)
				}
			}
		},
	},
	{
		name: "replacing an edge set removes the edges left out of it",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			from := ItemKey{Datatype: "monday/item", ItemID: "111"}
			edges := []RefEdge{
				{Name: "linked_repo", Kind: "inget", Key: testItem},
				{Name: "linked_repo", Kind: "inget", Key: "maxwellcudlitz/other"},
			}
			if err := h.PutRefs(ctx, from, edges); err != nil {
				t.Fatalf("PutRefs: %v", err)
			}
			// The resolver now returns only the first key: the second edge must not
			// keep invalidating this item forever.
			if err := h.PutRefs(ctx, from, edges[:1]); err != nil {
				t.Fatalf("PutRefs narrowed: %v", err)
			}
			stale, err := h.ReferencedBy(ctx, "inget", "maxwellcudlitz/other")
			if err != nil {
				t.Fatalf("ReferencedBy: %v", err)
			}
			if len(stale) != 0 {
				t.Errorf("ReferencedBy dropped key = %v, want nothing", stale)
			}
			kept, err := h.ReferencedBy(ctx, "inget", testItem)
			if err != nil {
				t.Fatalf("ReferencedBy: %v", err)
			}
			if len(kept) != 1 {
				t.Errorf("ReferencedBy kept key = %v, want one entry", kept)
			}
		},
	},
	{
		name: "clearing an item's references removes all of them",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			from := ItemKey{Datatype: "monday/item", ItemID: "111"}
			edge := RefEdge{Name: "linked_repo", Kind: "inget", Key: testItem}
			if err := h.PutRefs(ctx, from, []RefEdge{edge}); err != nil {
				t.Fatalf("PutRefs: %v", err)
			}
			if err := h.PutRefs(ctx, from, nil); err != nil {
				t.Fatalf("PutRefs(nil): %v", err)
			}
			got, err := h.ReferencedBy(ctx, "inget", testItem)
			if err != nil {
				t.Fatalf("ReferencedBy: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("ReferencedBy = %v, want nothing", got)
			}
		},
	},
	{
		name: "an incomplete edge is rejected",
		run: func(t *testing.T, h harness) {
			from := ItemKey{Datatype: "monday/item", ItemID: "111"}
			err := h.PutRefs(t.Context(), from, []RefEdge{{Name: "linked_repo", Kind: "inget"}})
			if err == nil {
				t.Error("PutRefs with no key: want an error")
			}
		},
	},
}

var signatureCases = []storeCase{
	{
		name: "an unrecorded scope reads back empty without an error",
		run: func(t *testing.T, h harness) {
			got, err := h.Signature(t.Context(), "enricher:github/repo:file")
			if err != nil {
				t.Fatalf("Signature: %v", err)
			}
			if got != "" {
				t.Errorf("Signature = %q, want empty", got)
			}
		},
	},
	{
		name: "a signature change is detectable",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			const scope = "enricher:github/repo:file"
			if err := h.PutSignature(ctx, scope, "sig-1"); err != nil {
				t.Fatalf("PutSignature: %v", err)
			}
			got, err := h.Signature(ctx, scope)
			if err != nil {
				t.Fatalf("Signature: %v", err)
			}
			if got != "sig-1" {
				t.Fatalf("Signature = %q, want sig-1", got)
			}

			// A changed prompt or model produces a new signature; the run compares it
			// with what is stored and reports the cost before spending it.
			if err := h.PutSignature(ctx, scope, "sig-2"); err != nil {
				t.Fatalf("PutSignature again: %v", err)
			}
			got, err = h.Signature(ctx, scope)
			if err != nil {
				t.Fatalf("Signature: %v", err)
			}
			if got != "sig-2" {
				t.Errorf("Signature = %q, want sig-2", got)
			}
		},
	},
	{
		name: "scopes are independent",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutSignature(ctx, "enricher:github/repo:file", "sig-1"); err != nil {
				t.Fatalf("PutSignature: %v", err)
			}
			got, err := h.Signature(ctx, "enricher:monday/item:column")
			if err != nil {
				t.Fatalf("Signature: %v", err)
			}
			if got != "" {
				t.Errorf("Signature of another scope = %q, want empty", got)
			}
		},
	},
	{
		name: "an empty scope or value is rejected",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutSignature(ctx, "", "sig-1"); err == nil {
				t.Error("PutSignature with no scope: want an error")
			}
			if err := h.PutSignature(ctx, "scope", ""); err == nil {
				t.Error("PutSignature with no signature: want an error")
			}
		},
	},
}
