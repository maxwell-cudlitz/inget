// Fixtures for the reference cascade tests: one inget reference on the harness datatype, and a
// referenced record in a second datatype.
//
// The referent is written straight into state rather than produced by a run of its own. What
// step 11 is being tested on is what happens to the referring item when that record moves, and
// running a second pipeline to move it would test the pipeline twice and the cascade once.
package pipeline

import (
	"testing"

	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/enrich/refs"
	"github.com/maxwellcudlitz/inget/internal/state"
)

const (
	refDatatype = "other/thing"
	refItemID   = "acme/referent"
	refName     = "linked_repo"
)

// noRefSecret is the secret resolver for references that need no credential.
func noRefSecret(config.SecretRef) (string, error) { return "", nil }

// withReference wires the harness datatype to one inget reference and seeds its referent.
// keyField is the metadata field key_from reads, so a test can point it at a field the record
// does not carry.
func withReference(t *testing.T, h *cascadeHarness, keyField, description, roleText string) {
	t.Helper()
	set, err := refs.New([]config.Reference{{
		Name:     refName,
		Resolver: refs.KindInget,
		Datatype: refDatatype,
		KeyFrom:  "metadata:" + keyField,
		Fields:   []string{"description", "view:role"},
		InjectAs: refs.InjectFragment,
	}}, h.store, noRefSecret)
	if err != nil {
		t.Fatalf("building reference set: %v", err)
	}
	h.deps.Refs = set
	h.deps.Config.MetadataFields = map[string]string{"related_keys": "${references.*.resolved_keys}"}
	h.metadata = map[string]string{"linked": refItemID}
	seedRefReferent(t, h, refItemID, description, roleText)
}

// seedRefReferent writes a referenced record: an item with metadata and one generated view.
func seedRefReferent(t *testing.T, h *cascadeHarness, itemID, description, roleText string) {
	t.Helper()
	err := h.store.PutItem(h.ctx, refDatatype, state.Item{
		ID: itemID, Source: "other", Fingerprint: "fp-referent",
		Metadata: map[string]string{"description": description},
	})
	if err != nil {
		t.Fatalf("seeding referent: %v", err)
	}
	err = h.store.PutViewState(h.ctx, refDatatype, itemID, state.ViewState{
		Name: "role", InputHash: "sha256:in", Text: roleText,
		EmbeddedHash: "sha256:emb", Signature: "sha256:sig",
	})
	if err != nil {
		t.Fatalf("seeding referent view: %v", err)
	}
}

// seedStaleReferrer records that an item references the referent with an out-of-date payload,
// without processing it. It is how the cap test produces several stale items at once.
func seedStaleReferrer(t *testing.T, h *cascadeHarness, itemID string) {
	t.Helper()
	err := h.store.PutRefs(h.ctx, state.ItemKey{Datatype: testDatatype, ItemID: itemID},
		[]state.RefEdge{{
			Name: refName, Kind: refs.KindInget,
			Key: refs.IngetKey(refDatatype, refItemID), Fingerprint: "sha256:old",
		}})
	if err != nil {
		t.Fatalf("seeding edges: %v", err)
	}
	// A matching fingerprint makes the item reconcile as unchanged, so the only reason it can
	// enter a work set is the invalidation.
	err = h.store.PutItem(h.ctx, testDatatype, state.Item{
		ID: itemID, Source: testSource,
		Fingerprint: recordFingerprint(itemID, baseVersion, noChange, ""),
	})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}
}

// dependentViews counts the harness views whose globs match a reference compose entry.
func dependentViews() int {
	n := 0
	for _, v := range testViews() {
		if delta.MatchesAny(refs.EntryPrefix+refName, v.DependsOn) {
			n++
		}
	}
	return n
}
