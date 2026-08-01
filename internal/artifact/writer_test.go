// Writer contract tests: what a run refuses to do.
package artifact

import "testing"

// TestWriteAfterCommit proves a committed run is closed to further records, since a shard
// written after the manifest would be invisible and its items silently lost.
func TestWriteAfterCommit(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	w, err := store.NewWriter(ctx, RunInfo{
		Source: testSource, Datatype: testDatatype, Scope: ScopePartial,
		Producer: testProducer, DomainHash: "sha256:domain", ConfigHash: "sha256:config",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Commit(ctx, CommitInfo{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := w.Write(ctx, testRecord(0)); err == nil {
		t.Error("Write after Commit succeeded, want an error")
	}
	if _, err := w.Commit(ctx, CommitInfo{}); err == nil {
		t.Error("second Commit succeeded, want an error")
	}
}

// TestRecordDatatypeMustMatchRun proves a shard cannot disagree with its manifest.
func TestRecordDatatypeMustMatchRun(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	w, err := store.NewWriter(ctx, RunInfo{
		Source: testSource, Datatype: testDatatype, Scope: ScopeFull,
		Producer: testProducer, DomainHash: "sha256:domain", ConfigHash: "sha256:config",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	rec := testRecord(0)
	rec.Datatype = "monday/item"
	if err := w.Write(ctx, rec); err == nil {
		t.Error("Write accepted a record from another datatype, want an error")
	}
}
