// Conformance cases for the per-item commit point.
//
// The behaviour worth pinning is atomicity: a checkpoint that cannot complete must leave no
// part of itself behind, because every guard it writes is a claim that work already happened.
package state

import "testing"

// checkpoint builds a complete checkpoint for the test item.
func checkpoint(fingerprint string) Checkpoint {
	return Checkpoint{
		RunID: testRun,
		Item: Item{
			ID:           testItem,
			Source:       testSource,
			Fingerprint:  fingerprint,
			ComposedHash: "sha256:composed",
			Metadata:     map[string]string{"full_name": testItem},
			RunID:        testRun,
		},
		Fragments: []FragmentState{
			{Key: "README.md", Fingerprint: "fp-readme", BlobRef: "sha256:blob", SizeBytes: 12, Tier: 1},
		},
		Views: []ViewState{
			{Name: "role", InputHash: "sha256:input", Text: "a summary",
				EmbeddedHash: "sha256:embedded", Model: "e5", Dims: 8, Signature: "sig"},
		},
	}
}

// enqueuedItem starts the test run and queues the test item, which is the state a checkpoint
// expects to find.
func enqueuedItem(t *testing.T, h harness) {
	t.Helper()
	ctx := t.Context()
	run := Run{ID: testRun, Binary: "inget", Datatype: testDatatype, Scope: ScopeFull, ConfigHash: testConfig}
	if err := h.StartRun(ctx, run); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := h.EnqueueWork(ctx, testRun, testDatatype, []string{testItem}); err != nil {
		t.Fatalf("EnqueueWork: %v", err)
	}
}

var checkpointCases = []storeCase{
	{
		name: "a checkpoint writes every guard and completes the work row",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueuedItem(t, h)
			if err := h.CheckpointItem(ctx, testDatatype, checkpoint("fp-1")); err != nil {
				t.Fatalf("CheckpointItem: %v", err)
			}

			fps, err := h.ItemFingerprints(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ItemFingerprints: %v", err)
			}
			if fps[testItem] != "fp-1" {
				t.Errorf("item fingerprint = %q, want %q", fps[testItem], "fp-1")
			}
			frags, err := h.Fragments(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("Fragments: %v", err)
			}
			if frags["README.md"].Fingerprint != "fp-readme" {
				t.Errorf("fragment fingerprint = %q, want %q", frags["README.md"].Fingerprint, "fp-readme")
			}
			views, err := h.ViewState(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("ViewState: %v", err)
			}
			if views["role"].EmbeddedHash != "sha256:embedded" {
				t.Errorf("view embedded hash = %q, want %q", views["role"].EmbeddedHash, "sha256:embedded")
			}
			// The work row is done, so a second claim finds nothing.
			claimed, err := h.ClaimWork(ctx, testRun, testDatatype, 10)
			if err != nil {
				t.Fatalf("ClaimWork: %v", err)
			}
			if len(claimed) != 0 {
				t.Errorf("ClaimWork returned %v, want nothing left to claim", claimed)
			}
		},
	},
	{
		name: "an unenqueued item is refused and writes nothing",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			// No StartRun, no EnqueueWork: the work row this checkpoint would complete does
			// not exist, so the whole transaction must roll back.
			if err := h.CheckpointItem(ctx, testDatatype, checkpoint("fp-1")); err == nil {
				t.Fatal("CheckpointItem: want an error for an item that was never enqueued")
			}

			fps, err := h.ItemFingerprints(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ItemFingerprints: %v", err)
			}
			if len(fps) != 0 {
				t.Errorf("a refused checkpoint left an item behind: %v", fps)
			}
			views, err := h.ViewState(ctx, testDatatype, testItem)
			if err != nil {
				t.Fatalf("ViewState: %v", err)
			}
			if len(views) != 0 {
				t.Errorf("a refused checkpoint left a view behind: %v", views)
			}
		},
	},
	{
		name: "a checkpoint without a run id is refused",
		run: func(t *testing.T, h harness) {
			cp := checkpoint("fp-1")
			cp.RunID = ""
			if err := h.CheckpointItem(t.Context(), testDatatype, cp); err == nil {
				t.Fatal("CheckpointItem: want an error when no run is named")
			}
		},
	},
	{
		name: "HasDerivation reports presence without recording a hit",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if found, err := h.HasDerivation(ctx, "sha256:absent"); err != nil || found {
				t.Fatalf("HasDerivation on an absent key = %v, %v; want false, nil", found, err)
			}
			if err := h.PutDerivation(ctx, derivation("sha256:a", "sig-1", "a summary")); err != nil {
				t.Fatalf("PutDerivation: %v", err)
			}
			found, err := h.HasDerivation(ctx, "sha256:a")
			if err != nil {
				t.Fatalf("HasDerivation: %v", err)
			}
			if !found {
				t.Error("HasDerivation = false, want true for a cached key")
			}
		},
	},
	{
		name: "RunStatus reports the recorded lifecycle",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if _, found, err := h.RunStatus(ctx, testRun); err != nil || found {
				t.Fatalf("RunStatus on an unknown run = %v, %v; want false, nil", found, err)
			}
			enqueuedItem(t, h)
			status, found, err := h.RunStatus(ctx, testRun)
			if err != nil || !found {
				t.Fatalf("RunStatus: found=%v err=%v", found, err)
			}
			if status != RunRunning {
				t.Errorf("status = %q, want %q", status, RunRunning)
			}
			if err := h.FinishRun(ctx, testRun, RunInterrupted, nil); err != nil {
				t.Fatalf("FinishRun: %v", err)
			}
			if status, _, _ := h.RunStatus(ctx, testRun); status != RunInterrupted {
				t.Errorf("status after FinishRun = %q, want %q", status, RunInterrupted)
			}
		},
	},
}
