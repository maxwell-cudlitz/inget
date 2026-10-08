// Lifecycle cases prove observer hooks preserve failure, planning, and cancellation behavior.
package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/destination"
	"github.com/maxwell-cudlitz/inget/internal/progress"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

type progressFailDestination struct{ destination.Destination }

func (d progressFailDestination) Upsert(context.Context, []destination.Row) error {
	return errors.New("test destination failure")
}

type progressFailCheckpoint struct{ state.Store }

func (s progressFailCheckpoint) PutConsumedArtifactRun(context.Context, string, string) error {
	return errors.New("test artifact checkpoint failure")
}

func TestIngestProgressFailedItemRetainsActualCounts(t *testing.T) {
	h := newCascadeHarness(t)
	capture := &progressCapture{}
	h.ctx = progress.WithObserver(h.ctx, capture)
	h.deps.Destinations["test-dest"] = progressFailDestination{h.dest}
	h.writeRun(baseVersion, noChange, "")
	_, stats, err := h.run()
	if err != nil || stats.ItemsFailed != 1 || stats.ItemsProcessed != 0 {
		t.Fatalf("partial failure behavior changed: stats=%+v err=%v", stats, err)
	}
	events := capture.take()
	assertTerminal(t, events, "failed")
	if eventCount(events, "completed", "failed") != 1 || eventCount(events, "completed", "processed") != 0 {
		t.Error("failed item progress claims successful completion")
	}
	if eventCount(events, "count", "embeddings") != h.emb.Embedded() || h.emb.Embedded() != numViews {
		t.Error("embeddings completed before failed storage were not counted")
	}
	if len(eventMatches(events, "stage", "checkpoint")) != 0 {
		t.Error("failed destination reached checkpoint stage")
	}
}

func TestIngestProgressArtifactCheckpointFailure(t *testing.T) {
	h := newCascadeHarness(t)
	capture := &progressCapture{}
	h.ctx = progress.WithObserver(h.ctx, capture)
	h.deps.State = progressFailCheckpoint{h.store}
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.run(); err == nil {
		t.Fatal("artifact checkpoint failure was swallowed")
	}
	events := capture.take()
	assertTerminal(t, events, "failed")
	if len(eventMatches(events, "done", "complete")) != 0 {
		t.Error("artifact was reported complete before its checkpoint failed")
	}
}

func TestIngestProgressInterruptedRun(t *testing.T) {
	h := newCascadeHarness(t)
	capture := &progressCapture{}
	h.ctx = progress.WithObserver(h.ctx, capture)
	h.writeRun(baseVersion, noChange, "")
	if _, _, err := h.runInterrupted(); err != nil {
		t.Fatal(err)
	}
	events := capture.take()
	assertTerminal(t, events, "interrupted")
	if len(eventMatches(events, "active", "")) != 0 || h.gen.Calls() != 0 {
		t.Error("interrupted run performed work")
	}
}

func TestIngestProgressCancellationStillEmitsDone(t *testing.T) {
	for _, stage := range []string{"finding artifacts", ""} {
		name := stage
		if name == "" {
			name = "reconciled"
		}
		t.Run(name, func(t *testing.T) {
			h := newCascadeHarness(t)
			h.writeRun(baseVersion, noChange, "")
			ctx, cancel := context.WithCancel(h.ctx)
			defer cancel()
			capture := &progressCapture{on: func(event progress.Event) {
				if event.Kind == "start" && event.Stage == stage {
					cancel()
				}
			}}
			h.ctx = progress.WithObserver(ctx, capture)
			if _, _, err := h.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("run error = %v, want context cancellation", err)
			}
			assertTerminal(t, capture.take(), "cancelled")
			if h.gen.Calls() != 0 || h.emb.Embedded() != 0 {
				t.Error("cancelled run called a model")
			}
		})
	}
}

func TestIngestProgressPlanningEmitsNothing(t *testing.T) {
	h := newCascadeHarness(t)
	capture := &progressCapture{}
	h.ctx = progress.WithObserver(h.ctx, capture)
	h.writeRun(baseVersion, noChange, "")
	if _, err := h.plan(); err != nil {
		t.Fatal(err)
	}
	if events := capture.take(); len(events) != 0 {
		t.Errorf("planning emitted execution progress: %+v", events)
	}
}
