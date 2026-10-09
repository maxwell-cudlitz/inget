// Conformance cases for work checkpointing: the resumability guarantee (D13).
package state

import (
	"errors"
	"slices"
	"sync"
	"testing"
)

// workItems is the queue every case here enqueues.
var workItems = []string{"item-a", "item-b", "item-c", "item-d"}

// enqueued starts a run and fills its queue.
func enqueued(t *testing.T, h harness) {
	t.Helper()
	seedRun(t, h, firstRun)
	if err := h.EnqueueWork(t.Context(), firstRun, testDatatype, workItems); err != nil {
		t.Fatalf("EnqueueWork: %v", err)
	}
}

// claim claims n items and fails the test on error.
func claim(t *testing.T, h harness, n int) []string {
	t.Helper()
	claimed, err := h.ClaimWork(t.Context(), firstRun, testDatatype, n)
	if err != nil {
		t.Fatalf("ClaimWork(%d): %v", n, err)
	}
	return claimed
}

var workCases = []storeCase{
	{
		name: "work is claimed in batches and each item once",
		run: func(t *testing.T, h harness) {
			enqueued(t, h)
			first := claim(t, h, 2)
			if !slices.Equal(first, workItems[:2]) {
				t.Fatalf("first claim = %v, want %v", first, workItems[:2])
			}
			second := claim(t, h, 10)
			if !slices.Equal(second, workItems[2:]) {
				t.Fatalf("second claim = %v, want %v", second, workItems[2:])
			}
			if remaining := claim(t, h, 10); len(remaining) != 0 {
				t.Errorf("third claim = %v, want nothing left", remaining)
			}
		},
	},
	{
		name: "completed work is not claimed again, and re-enqueueing preserves it",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueued(t, h)
			for _, id := range claim(t, h, 2) {
				if err := h.CompleteWork(ctx, firstRun, testDatatype, id, nil); err != nil {
					t.Fatalf("CompleteWork %s: %v", id, err)
				}
			}
			// The resumed process enqueues the whole item set again, because it cannot
			// know what the previous attempt reached.
			if err := h.EnqueueWork(ctx, firstRun, testDatatype, workItems); err != nil {
				t.Fatalf("EnqueueWork again: %v", err)
			}
			got := claim(t, h, 10)
			if !slices.Equal(got, workItems[2:]) {
				t.Errorf("claim after re-enqueue = %v, want only the unfinished %v", got, workItems[2:])
			}
		},
	},
	{
		name: "interrupted work is reclaimable",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueued(t, h)
			stranded := claim(t, h, 2)

			// The process dies here: two items are claimed and neither is completed. The
			// replacement takes the lock, resets the claims, and finds them pending.
			reset, err := h.ResetClaims(ctx, firstRun, testDatatype)
			if err != nil {
				t.Fatalf("ResetClaims: %v", err)
			}
			if reset != len(stranded) {
				t.Errorf("ResetClaims = %d, want %d", reset, len(stranded))
			}
			if got := claim(t, h, 2); !slices.Equal(got, stranded) {
				t.Errorf("claim after reset = %v, want the stranded items %v", got, stranded)
			}
		},
	},
	{
		name: "replacing a queue removes old statuses and enqueues the exact new set",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueued(t, h)
			claimed := claim(t, h, 2)
			if err := h.CompleteWork(ctx, firstRun, testDatatype, claimed[0], nil); err != nil {
				t.Fatalf("CompleteWork: %v", err)
			}
			want := []string{"item-new", "item-c"}
			if err := h.ReplaceWork(ctx, firstRun, testDatatype, want); err != nil {
				t.Fatalf("ReplaceWork: %v", err)
			}
			if got := claim(t, h, 10); !slices.Equal(got, []string{"item-c", "item-new"}) {
				t.Errorf("claim after ReplaceWork = %v, want [item-c item-new]", got)
			}
		},
	},
	{
		name: "failed work stays failed rather than retrying forever",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueued(t, h)
			id := claim(t, h, 1)[0]
			cause := errors.New("generator returned an empty view")
			if err := h.CompleteWork(ctx, firstRun, testDatatype, id, cause); err != nil {
				t.Fatalf("CompleteWork: %v", err)
			}
			reset, err := h.ResetClaims(ctx, firstRun, testDatatype)
			if err != nil {
				t.Fatalf("ResetClaims: %v", err)
			}
			if reset != 0 {
				t.Errorf("ResetClaims = %d, want 0 — a failure is not a stranded claim", reset)
			}
			if got := claim(t, h, 10); slices.Contains(got, id) {
				t.Errorf("claim = %v, want the failed item %s left alone", got, id)
			}
		},
	},
	{
		name: "queues are scoped by run and datatype",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueued(t, h)
			if err := h.EnqueueWork(ctx, firstRun, "monday/item", []string{"111"}); err != nil {
				t.Fatalf("EnqueueWork monday: %v", err)
			}
			got, err := h.ClaimWork(ctx, firstRun, "monday/item", 10)
			if err != nil {
				t.Fatalf("ClaimWork monday: %v", err)
			}
			if !slices.Equal(got, []string{"111"}) {
				t.Errorf("ClaimWork monday = %v, want [111]", got)
			}
			if remaining := claim(t, h, 10); len(remaining) != len(workItems) {
				t.Errorf("claim = %v, want the %s queue untouched", remaining, testDatatype)
			}
		},
	},
	{
		name: "concurrent workers never claim the same item",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueued(t, h)

			const workers = 4
			var (
				mu      sync.Mutex
				claimed []string
				wg      sync.WaitGroup
			)
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					got, err := h.ClaimWork(ctx, firstRun, testDatatype, 2)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						t.Errorf("ClaimWork: %v", err)
						return
					}
					claimed = append(claimed, got...)
				}()
			}
			wg.Wait()

			slices.Sort(claimed)
			if !slices.Equal(claimed, workItems) {
				t.Errorf("claimed = %v, want every item exactly once: %v", claimed, workItems)
			}
		},
	},
	{
		name: "completing work that was never enqueued is an error",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			enqueued(t, h)
			if err := h.CompleteWork(ctx, firstRun, testDatatype, "item-z", nil); err == nil {
				t.Error("CompleteWork on an unqueued item: want an error")
			}
		},
	},
	{
		name: "a non-positive batch size is rejected",
		run: func(t *testing.T, h harness) {
			enqueued(t, h)
			if _, err := h.ClaimWork(t.Context(), firstRun, testDatatype, 0); err == nil {
				t.Error("ClaimWork(0): want an error")
			}
		},
	},
	{
		name: "enqueueing nothing is not an error",
		run: func(t *testing.T, h harness) {
			seedRun(t, h, firstRun)
			if err := h.EnqueueWork(t.Context(), firstRun, testDatatype, nil); err != nil {
				t.Errorf("EnqueueWork(nil): %v", err)
			}
		},
	},
}
