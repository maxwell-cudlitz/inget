// Tests for the reindex pass: what it rewrites, what it leaves alone, and what happens when it is
// interrupted.
package reindex

import (
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/pipeline"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

func TestRunRewritesEveryStoredViewUnderTheNewEmbedder(t *testing.T) {
	h := newHarness(t, 3)
	h.dest.prune = 4

	report, err := Run(t.Context(), h.deps, h.options())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Items != 3 || report.Vectors != 6 {
		t.Errorf("report items/vectors = %d/%d, want 3/6", report.Items, report.Vectors)
	}
	if report.Failed != 0 || report.Unchanged != 0 {
		t.Errorf("report failed/unchanged = %d/%d, want 0/0", report.Failed, report.Unchanged)
	}
	if len(report.Rebound) != 1 || report.Rebound[0] != testDest {
		t.Errorf("rebound = %v, want [%s]", report.Rebound, testDest)
	}
	if report.Pruned != 4 {
		t.Errorf("pruned = %d, want the 4 the destination reported", report.Pruned)
	}
	if report.RunID == "" {
		t.Error("report carries no run id; the pass must be resumable")
	}

	rows := h.dest.written()
	if len(rows) != 6 {
		t.Fatalf("destination received %d rows, want 6", len(rows))
	}
	for _, r := range rows {
		switch {
		case r.Model != newModel:
			t.Errorf("row %s/%s carries model %q, want %q", r.ItemID, r.ViewName, r.Model, newModel)
		case r.Dims != testDims || len(r.Embedding) != testDims:
			t.Errorf("row %s/%s has dims %d and %d values, want %d",
				r.ItemID, r.ViewName, r.Dims, len(r.Embedding), testDims)
		case r.Text == "":
			t.Errorf("row %s/%s has no text", r.ItemID, r.ViewName)
		case r.Metadata["name"] != r.ItemID:
			t.Errorf("row %s/%s metadata = %v, want the item's stored metadata",
				r.ItemID, r.ViewName, r.Metadata)
		case len(r.RelatedKeys) != 1 || r.RelatedKeys[0] != "monday/"+r.ItemID:
			t.Errorf("row %s/%s related keys = %v, want the stored reference key",
				r.ItemID, r.ViewName, r.RelatedKeys)
		// metadata_fields naming resolved_keys is expanded from those same keys, which is what
		// makes the rewritten row match what the pipeline wrote.
		case r.Metadata["related_keys"] != "monday/"+r.ItemID:
			t.Errorf("row %s/%s expanded related_keys to %q", r.ItemID, r.ViewName,
				r.Metadata["related_keys"])
		}
	}

	// State must now claim the new model, or the next `inget run` would re-embed everything.
	for _, id := range h.items {
		for name, v := range viewsOf(t, h.store, id) {
			if v.Model != newModel || v.Signature != h.emb.Signature() {
				t.Errorf("%s/%s state says model %q signature %q", id, name, v.Model, v.Signature)
			}
			if v.EmbeddedHash != delta.EmbeddingHash(v.Text) {
				t.Errorf("%s/%s embedded hash does not match its text", id, name)
			}
			if v.InputHash != "in:"+id+":"+name {
				t.Errorf("%s/%s input hash = %q; a re-embed must not touch the level-2 guard",
					id, name, v.InputHash)
			}
		}
	}
	if pruned := h.dest.prunedDatatypes(); len(pruned) != 1 || pruned[0] != testDatatype {
		t.Errorf("pruned datatypes = %v, want [%s]", pruned, testDatatype)
	}
}

func TestASecondPassEmbedsNothing(t *testing.T) {
	h := newHarness(t, 2)
	if _, err := Run(t.Context(), h.deps, h.options()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	h.emb.ResetEmbedded()

	report, err := Run(t.Context(), h.deps, h.options())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if h.emb.Embedded() != 0 {
		t.Errorf("second pass embedded %d texts, want 0", h.emb.Embedded())
	}
	if report.Vectors != 0 || report.Unchanged != 2 {
		t.Errorf("second pass vectors/unchanged = %d/%d, want 0/2", report.Vectors, report.Unchanged)
	}
	if len(report.Rebound) != 0 {
		// The fake reports "changed" on every call; what matters is that a real destination
		// would not, so this only asserts the pass reports what the destination told it.
		t.Logf("rebound = %v", report.Rebound)
	}
}

func TestForceRewritesViewsThatAreAlreadyCurrent(t *testing.T) {
	h := newHarness(t, 2)
	if _, err := Run(t.Context(), h.deps, h.options()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	h.emb.ResetEmbedded()

	o := h.options()
	o.Force = true
	report, err := Run(t.Context(), h.deps, o)
	if err != nil {
		t.Fatalf("forced Run: %v", err)
	}
	if h.emb.Embedded() != 4 {
		t.Errorf("forced pass embedded %d texts, want 4", h.emb.Embedded())
	}
	if report.Vectors != 4 {
		t.Errorf("forced pass wrote %d vectors, want 4", report.Vectors)
	}
}

func TestAViewFilterRestrictsThePassAndSkipsThePrune(t *testing.T) {
	h := newHarness(t, 2)
	o := h.options()
	o.Views = []string{"role"}

	report, err := Run(t.Context(), h.deps, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Vectors != 2 {
		t.Errorf("wrote %d vectors, want 2: one view of each item", report.Vectors)
	}
	for _, r := range h.dest.written() {
		if r.ViewName != "role" {
			t.Errorf("wrote view %q, which the filter excluded", r.ViewName)
		}
	}
	// Pruning after a partial pass would delete the very views this pass did not rewrite.
	if pruned := h.dest.prunedDatatypes(); len(pruned) != 0 {
		t.Errorf("pruned %v after a view-restricted pass, want no prune", pruned)
	}
	for _, id := range h.items {
		if got := viewsOf(t, h.store, id)["stack"].Model; got != oldModel {
			t.Errorf("%s/stack model = %q, want the excluded view left at %q", id, got, oldModel)
		}
	}
}

func TestAnInterruptedPassResumesWithoutRepeatingWork(t *testing.T) {
	h := newHarness(t, 4)
	o := h.options()
	o.BatchSize = 1

	// Shut down as soon as the first batch has been written, which is the pod-eviction case: the
	// item in flight is finished, nothing new is claimed.
	stop := make(chan struct{})
	var closed bool
	h.dest.onWrite = func() {
		if !closed {
			closed = true
			close(stop)
		}
	}
	ctx := pipeline.WithShutdown(t.Context(), stop)

	first, err := Run(ctx, h.deps, o)
	if err != nil {
		t.Fatalf("interrupted Run: %v", err)
	}
	if !first.Interrupted {
		t.Fatal("the pass did not report itself interrupted")
	}
	if first.Items != 1 {
		t.Fatalf("interrupted pass rewrote %d items, want 1", first.Items)
	}
	if pruned := h.dest.prunedDatatypes(); len(pruned) != 0 {
		t.Errorf("pruned %v after an interrupted pass, want no prune", pruned)
	}
	status, _, err := h.store.RunStatus(t.Context(), first.RunID)
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if status != state.RunInterrupted {
		t.Errorf("run status = %q, want %q", status, state.RunInterrupted)
	}

	h.dest.onWrite = nil
	second, err := Run(t.Context(), h.deps, o)
	if err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if !second.Resuming || second.RunID != first.RunID {
		t.Errorf("resumed run = %s (resuming=%v), want to adopt %s",
			second.RunID, second.Resuming, first.RunID)
	}
	if second.Items != 3 {
		t.Errorf("resumed pass rewrote %d items, want the 3 that were left", second.Items)
	}
	// Eight views over four items, each written exactly once across the two passes.
	if rows := h.dest.written(); len(rows) != 8 {
		t.Errorf("destination received %d rows across both passes, want 8", len(rows))
	}
}

func TestAnEmptyCorpusWritesNothingAndPrunesNothing(t *testing.T) {
	h := newHarness(t, 0)

	report, err := Run(t.Context(), h.deps, h.options())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Items != 0 || report.Vectors != 0 || report.RunID != "" {
		t.Errorf("report = %+v, want an empty pass with no run", report)
	}
	// The dangerous case: pruning here would empty the table because state is empty.
	if pruned := h.dest.prunedDatatypes(); len(pruned) != 0 {
		t.Errorf("pruned %v with nothing to reindex", pruned)
	}
}

func TestOptionsAreValidated(t *testing.T) {
	h := newHarness(t, 1)
	for _, tc := range []struct {
		name string
		want string
		edit func(o *Options)
	}{
		{"no datatype", "datatype", func(o *Options) { o.Datatype = "" }},
		{"no destinations", "destination", func(o *Options) { o.Destinations = nil }},
		{"unknown destination", "not open", func(o *Options) { o.Destinations = []string{"absent"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := h.options()
			tc.edit(&o)
			_, err := Run(t.Context(), h.deps, o)
			if err == nil {
				t.Fatal("Run: want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}
