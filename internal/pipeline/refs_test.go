// The step 11 gate: a changed referent invalidates exactly the dependent views of the items
// that reference it, and nothing else.
//
// The harness datatype is wired with one inget reference injected as a fragment under the key
// "ref:linked_repo". Only the harness views whose globs include "**" match that key; the rest
// name specific paths and must not regenerate. That split is the assertion — an invalidation
// that regenerated every view would be indistinguishable from a full rebuild, which is the cost
// D12 exists to avoid.
package pipeline

import (
	"testing"

	"github.com/maxwellcudlitz/inget/internal/enrich/refs"
	"github.com/maxwellcudlitz/inget/internal/state"
)

func TestChangedReferentInvalidatesOnlyTheDependentViews(t *testing.T) {
	h := newCascadeHarness(t)
	withReference(t, h, "linked", "a terraform module registry", "publishes modules")
	h.writeRun(baseVersion, noChange, "")

	if _, _, err := h.run(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	wantKey := refs.IngetKey(refDatatype, refItemID)
	edges, err := h.store.Refs(h.ctx, state.ItemKey{Datatype: testDatatype, ItemID: testItemID})
	if err != nil {
		t.Fatalf("reading edges: %v", err)
	}
	if len(edges) != 1 || edges[0].Key != wantKey || edges[0].Fingerprint == "" {
		t.Fatalf("edges = %+v, want one resolved edge to %s", edges, wantKey)
	}
	for _, row := range h.dest.rowsWritten() {
		if len(row.RelatedKeys) != 1 || row.RelatedKeys[0] != wantKey {
			t.Fatalf("row related keys = %v, want [%s]", row.RelatedKeys, wantKey)
		}
		if row.Metadata["related_keys"] != wantKey {
			t.Errorf("metadata related_keys = %q, want %q", row.Metadata["related_keys"], wantKey)
		}
	}
	// The payload has to reach the prompt, not merely be recorded.
	if prompt := h.gen.promptFor("role"); !contains(prompt, "publishes modules") {
		t.Errorf("the reference payload did not reach the view prompt:\n%s", prompt)
	}

	// A second identical run must do nothing: a reference that resolved to the same payload is
	// not a change, so no item is enqueued and no token is spent.
	h.reset()
	plan, _, err := h.run()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(plan.WorkItems) != 0 || h.gen.Calls() != 0 {
		t.Fatalf("second run: work=%d generator calls=%d, want none", len(plan.WorkItems), h.gen.Calls())
	}

	// The referent's view text changes, and its own pipeline marks the edges pointing at it.
	seedRefReferent(t, h, refItemID, "a terraform module registry", "publishes and verifies modules")
	if _, err := refs.Cascade(h.ctx, h.store, refDatatype, refItemID, 0, 2); err != nil {
		t.Fatalf("cascade: %v", err)
	}

	h.reset()
	plan, stats, err := h.run()
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if plan.Invalidated != 1 || len(plan.WorkItems) != 1 {
		t.Fatalf("invalidated=%d work=%v, want the referring item enqueued once",
			plan.Invalidated, plan.WorkItems)
	}
	want := dependentViews()
	if stats.ViewsGenerated != want {
		t.Errorf("views generated = %d, want %d: only the views depending on the reference",
			stats.ViewsGenerated, want)
	}
	if stats.ViewsSkipped != numViews-want {
		t.Errorf("views skipped = %d, want %d", stats.ViewsSkipped, numViews-want)
	}
	if stats.FragmentsEnrich != 0 {
		t.Errorf("fragments enriched = %d, want 0: no fragment changed", stats.FragmentsEnrich)
	}
	if got := h.dest.rows(); got != want {
		t.Errorf("rows upserted = %d, want %d", got, want)
	}

	// The mark is cleared by the item's own checkpoint, so a fourth run finds nothing stale.
	h.reset()
	plan, _, err = h.run()
	if err != nil {
		t.Fatalf("fourth run: %v", err)
	}
	if plan.Invalidated != 0 || h.gen.Calls() != 0 {
		t.Errorf("fourth run: invalidated=%d generator calls=%d, want none",
			plan.Invalidated, h.gen.Calls())
	}
}

func TestUnextractableReferenceKeyDoesNotChurn(t *testing.T) {
	h := newCascadeHarness(t)
	// key_from names a metadata field the record does not carry, which is what an item with no
	// linked record looks like.
	withReference(t, h, "absent", "unused", "unused")
	h.writeRun(baseVersion, noChange, "")

	if _, _, err := h.run(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	stale, err := h.store.StaleReferrers(h.ctx, testDatatype)
	if err != nil {
		t.Fatalf("StaleReferrers: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %+v, want nothing: no key means no edge to be stale about", stale)
	}
	h.reset()
	plan, _, err := h.run()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(plan.WorkItems) != 0 || h.gen.Calls() != 0 {
		t.Errorf("second run: work=%d calls=%d, want none", len(plan.WorkItems), h.gen.Calls())
	}
}

func TestUnresolvedReferentRecordsAnEdgeAndResolvesLater(t *testing.T) {
	const later = "acme/not-fetched-yet"
	h := newCascadeHarness(t)
	withReference(t, h, "linked", "unused", "unused")
	// Point at a record no run has fetched, which is ordinary: a board knows about a repository
	// before inget does.
	h.metadata = map[string]string{"linked": later}
	h.writeRun(baseVersion, noChange, "")

	if _, _, err := h.run(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	edges, err := h.store.Refs(h.ctx, state.ItemKey{Datatype: testDatatype, ItemID: testItemID})
	if err != nil {
		t.Fatalf("reading edges: %v", err)
	}
	// The edge is the point: without it, the referent's first run has nothing to walk back to and
	// this item never learns that its reference became resolvable.
	if len(edges) != 1 || edges[0].Fingerprint != "" {
		t.Fatalf("edges = %+v, want one unresolved edge", edges)
	}

	seedRefReferent(t, h, later, "now it exists", "now it has a role")
	if _, err := refs.Cascade(h.ctx, h.store, refDatatype, later, 0, 2); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	h.reset()
	plan, stats, err := h.run()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if plan.Invalidated != 1 {
		t.Fatalf("invalidated = %d, want 1", plan.Invalidated)
	}
	if stats.ViewsGenerated != dependentViews() {
		t.Errorf("views generated = %d, want %d", stats.ViewsGenerated, dependentViews())
	}
}

func TestCascadeCapDefersInvalidations(t *testing.T) {
	h := newCascadeHarness(t)
	withReference(t, h, "linked", "described", "role text")
	items := []string{"acme/one", "acme/three", "acme/two"}
	h.writeRunWithItems(baseVersion, noChange, "", items...)
	// Every item references the same record: the invalidation storm the per-run cap bounds.
	for _, id := range items {
		seedStaleReferrer(t, h, id)
	}
	if _, err := refs.Cascade(h.ctx, h.store, refDatatype, refItemID, 0, 2); err != nil {
		t.Fatalf("cascade: %v", err)
	}

	rc := h.runConfig()
	rc.MaxCascadePerRun = 2
	plan, _, err := Run(h.ctx, h.deps, h.arts, rc)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if plan.Invalidated != 2 || plan.Deferred != 1 {
		t.Fatalf("invalidated=%d deferred=%d, want 2 taken and 1 deferred",
			plan.Invalidated, plan.Deferred)
	}

	// The deferred item kept its mark, so the next run finds it with no queue of its own.
	plan, _, err = Run(h.ctx, h.deps, h.arts, rc)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if plan.Invalidated != 1 || plan.Deferred != 0 {
		t.Errorf("invalidated=%d deferred=%d, want the remainder taken next run",
			plan.Invalidated, plan.Deferred)
	}
}
