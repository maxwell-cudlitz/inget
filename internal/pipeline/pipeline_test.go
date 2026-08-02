// Tests for the pipeline's shutdown signal and run statistics.
//
// ShuttingDown is read from the context the caller wired to NotifyShutdown, so it is
// asserted before, at and after channel close — a pool watching a channel nobody closes
// passes every other test and never drains on SIGTERM. The stats collector is asserted
// under concurrent writers, since every worker reports into it.
package pipeline

import (
	"context"
	"testing"
)

func TestIsShuttingDownFalseByDefault(t *testing.T) {
	ctx := context.Background()
	if ShuttingDown(ctx) {
		t.Error("expected false when no shutdown channel is set")
	}
}

func TestIsShuttingDownTrueAfterClose(t *testing.T) {
	ch := make(chan struct{})
	ctx := WithShutdown(context.Background(), ch)
	close(ch)
	if !ShuttingDown(ctx) {
		t.Error("expected true after shutdown channel is closed")
	}
}

func TestIsShuttingDownFalseBeforeClose(t *testing.T) {
	ch := make(chan struct{})
	ctx := WithShutdown(context.Background(), ch)
	if ShuttingDown(ctx) {
		t.Error("expected false before shutdown channel is closed")
	}
}

func TestStatsCollectorSnapshot(t *testing.T) {
	s := &statsCollector{}
	s.addProcessed(3)
	s.addFailed(1)
	s.addFragmentsEnriched(5)
	s.addViewsGenerated(2)
	s.addViewsSkipped(4)
	s.addEmbeddings(2)
	s.addTombstones(1)

	snap := s.snapshot()
	if snap.ItemsProcessed != 3 {
		t.Errorf("processed = %d, want 3", snap.ItemsProcessed)
	}
	if snap.ItemsFailed != 1 {
		t.Errorf("failed = %d, want 1", snap.ItemsFailed)
	}
	if snap.FragmentsEnrich != 5 {
		t.Errorf("fragments = %d, want 5", snap.FragmentsEnrich)
	}
	if snap.ViewsGenerated != 2 {
		t.Errorf("views generated = %d, want 2", snap.ViewsGenerated)
	}
	if snap.ViewsSkipped != 4 {
		t.Errorf("views skipped = %d, want 4", snap.ViewsSkipped)
	}
	if snap.EmbeddingsStored != 2 {
		t.Errorf("embeddings = %d, want 2", snap.EmbeddingsStored)
	}
	if snap.TombstonesApplied != 1 {
		t.Errorf("tombstones = %d, want 1", snap.TombstonesApplied)
	}
}

func TestStatsCollectorConcurrent(t *testing.T) {
	s := &statsCollector{}
	done := make(chan struct{})
	for i := range 10 {
		go func(n int) {
			s.addProcessed(1)
			s.addViewsGenerated(1)
			done <- struct{}{}
			_ = n
		}(i)
	}
	for range 10 {
		<-done
	}
	snap := s.snapshot()
	if snap.ItemsProcessed != 10 {
		t.Errorf("concurrent processed = %d, want 10", snap.ItemsProcessed)
	}
}
