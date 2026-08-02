// Conformance cases for run history and resume detection.
package state

import "testing"

// run IDs are ULIDs in production, so lexical order is chronological order; these fixtures
// keep that property.
const (
	firstRun  = "01K1QZ8Q0000000000000000AA"
	secondRun = "01K1QZ8Q0000000000000000BB"
)

// interrupt starts a run and leaves it interrupted, which is the state a killed process
// leaves behind once its signal handler has run.
func interrupt(t *testing.T, h harness, runID string) {
	t.Helper()
	seedRun(t, h, runID)
	if err := h.FinishRun(t.Context(), runID, RunInterrupted, map[string]int{"items": 4}); err != nil {
		t.Fatalf("FinishRun %s: %v", runID, err)
	}
}

var runCases = []storeCase{
	{
		name: "a running run is resumable",
		run: func(t *testing.T, h harness) {
			seedRun(t, h, firstRun)
			got, found, err := h.ResumableRun(t.Context(), "inget", testDatatype, testConfig)
			if err != nil || !found {
				t.Fatalf("ResumableRun: found=%v err=%v", found, err)
			}
			if got != firstRun {
				t.Errorf("ResumableRun = %q, want %q", got, firstRun)
			}
		},
	},
	{
		name: "a completed run is not resumable",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedRun(t, h, firstRun)
			if err := h.FinishRun(ctx, firstRun, RunOK, map[string]any{"items": 10}); err != nil {
				t.Fatalf("FinishRun: %v", err)
			}
			_, found, err := h.ResumableRun(ctx, "inget", testDatatype, testConfig)
			if err != nil {
				t.Fatalf("ResumableRun: %v", err)
			}
			if found {
				t.Error("ResumableRun found a finished run")
			}
		},
	},
	{
		name: "an interrupted run is resumable and the newest one wins",
		run: func(t *testing.T, h harness) {
			interrupt(t, h, firstRun)
			interrupt(t, h, secondRun)
			got, found, err := h.ResumableRun(t.Context(), "inget", testDatatype, testConfig)
			if err != nil || !found {
				t.Fatalf("ResumableRun: found=%v err=%v", found, err)
			}
			if got != secondRun {
				t.Errorf("ResumableRun = %q, want the newer run %q", got, secondRun)
			}
		},
	},
	{
		name: "a changed config hash makes a run unresumable",
		run: func(t *testing.T, h harness) {
			interrupt(t, h, firstRun)
			_, found, err := h.ResumableRun(t.Context(), "inget", testDatatype, "sha256:different")
			if err != nil {
				t.Fatalf("ResumableRun: %v", err)
			}
			if found {
				t.Error("ResumableRun matched a run made under a different configuration")
			}
		},
	},
	{
		name: "runs of another binary or datatype do not match",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			interrupt(t, h, firstRun)
			for _, q := range []struct{ binary, datatype string }{
				{"inget-fetch", testDatatype},
				{"inget", "monday/item"},
			} {
				if _, found, err := h.ResumableRun(ctx, q.binary, q.datatype, testConfig); err != nil || found {
					t.Errorf("ResumableRun(%s, %s): found=%v err=%v, want not found",
						q.binary, q.datatype, found, err)
				}
			}
		},
	},
	{
		name: "a run spanning every datatype is matched by an empty datatype",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			r := Run{ID: firstRun, Binary: "inget", Scope: ScopeFull, ConfigHash: testConfig}
			if err := h.StartRun(ctx, r); err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			got, found, err := h.ResumableRun(ctx, "inget", "", testConfig)
			if err != nil || !found {
				t.Fatalf("ResumableRun: found=%v err=%v", found, err)
			}
			if got != firstRun {
				t.Errorf("ResumableRun = %q, want %q", got, firstRun)
			}
		},
	},
	{
		name: "re-starting an adopted run reopens it",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			interrupt(t, h, firstRun)
			// A resumed process adopts the run ID rather than minting a new one, so
			// StartRun has to accept an ID it has seen before.
			seedRun(t, h, firstRun)
			got, found, err := h.ResumableRun(ctx, "inget", testDatatype, testConfig)
			if err != nil || !found || got != firstRun {
				t.Fatalf("ResumableRun = %q, %v, %v; want %q found", got, found, err, firstRun)
			}
		},
	},
	{
		name: "finishing an unknown run is an error",
		run: func(t *testing.T, h harness) {
			if err := h.FinishRun(t.Context(), "01K1QZ8Q0000000000000000ZZ", RunOK, nil); err == nil {
				t.Error("FinishRun on an unknown run: want an error")
			}
		},
	},
	{
		name: "invalid run values are rejected",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedRun(t, h, firstRun)
			if err := h.FinishRun(ctx, firstRun, "done", nil); err == nil {
				t.Error(`FinishRun with status "done": want an error, the set is closed`)
			}
			if err := h.FinishRun(ctx, firstRun, RunRunning, nil); err == nil {
				t.Error("FinishRun with a non-terminal status: want an error")
			}
			if err := h.StartRun(ctx, Run{Binary: "inget", Scope: ScopeFull}); err == nil {
				t.Error("StartRun with no ID: want an error")
			}
			if err := h.StartRun(ctx, Run{ID: secondRun, Binary: "inget", Scope: "whole"}); err == nil {
				t.Error("StartRun with an unknown scope: want an error")
			}
		},
	},
}
