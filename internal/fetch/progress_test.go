// Progress observes the real fetch lifecycle and item outcomes without changing artifact semantics.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/maxwell-cudlitz/inget/internal/progress"
	"github.com/maxwell-cudlitz/inget/internal/source"
)

func TestProgressCountsFetchOutcomes(t *testing.T) {
	unchanged := item("acme/unchanged", "v1")
	broken := item("acme/broken", "v1")
	broken.fail = true
	noisy := item("acme/noisy", "v1")
	noisy.warnings = []string{"acme/noisy: skipped a secret"}
	h := newHarness(t, unchanged, broken, noisy)
	h.checkpoint(unchanged, blobDigests(unchanged))
	observer := newFetchObserver()
	h.ctx = progress.WithObserver(h.ctx, observer)
	report := h.run()
	events := observer.snapshot()
	assertFetchLifecycle(t, events, "complete")
	assertFetchCount(t, events, "enumerated", "", report.Enumerated)
	assertFetchCount(t, events, "completed", "skipped", report.SkippedUnchanged)
	assertFetchCount(t, events, "completed", "failed", report.Failed)
	assertFetchCount(t, events, "completed", "fetched", report.Items)
	assertFetchCount(t, events, "count", "fragments", report.Fragments)
	assertFetchCount(t, events, "count", "blobs", report.BlobsWritten+report.BlobsReused)
	assertFetchCount(t, events, "count", "warnings", len(report.Warnings))
	var listed, committing bool
	for _, event := range events {
		if event.Kind == "listed" {
			listed = event.Total == report.Enumerated
		}
		if event.Kind == "stage" && event.Stage == "committing" {
			committing = true
		}
		if event.Kind == "active" && event.Item == unchanged.id {
			t.Error("an unchanged item was reported active")
		}
	}
	if !listed || !committing {
		t.Errorf("missing known total or artifact commit stage: %+v", events)
	}
	if h.latestManifest().Counts.Items != report.Items {
		t.Error("observed fetch changed committed item count")
	}
}

func TestProgressCountsReusedBlobReferences(t *testing.T) {
	one := item("acme/a", "v1")
	h := newHarness(t, one)
	h.run()
	h.checkpoint(one, blobDigests(one))
	h.conn.items[0].fingerprint = "acme/a:v2"
	h.conn.reset()
	observer := newFetchObserver()
	h.ctx = progress.WithObserver(h.ctx, observer)
	report := h.run()
	if report.BlobsWritten != 0 || report.BlobsReused != 2 {
		t.Fatalf("blob reuse report = %+v", report)
	}
	assertFetchCount(t, observer.snapshot(), "count", "blobs", report.BlobsReused)
}

func TestProgressWarningCountIsNotCappedByManifest(t *testing.T) {
	noisy := item("acme/noisy", "v1")
	noisy.warnings = make([]string, maxWarnings+2)
	for i := range noisy.warnings {
		noisy.warnings[i] = "acme/noisy: omitted content"
	}
	h := newHarness(t, noisy)
	observer := newFetchObserver()
	h.ctx = progress.WithObserver(h.ctx, observer)
	report := h.run()
	if len(report.Warnings) != maxWarnings+1 {
		t.Fatalf("manifest warnings = %d, want bounded warnings and overflow summary", len(report.Warnings))
	}
	assertFetchCount(t, observer.snapshot(), "count", "warnings", len(noisy.warnings))
}

func TestProgressDryRunDoesNotReportFetchedWork(t *testing.T) {
	unchanged := item("acme/unchanged", "v1")
	h := newHarness(t, unchanged, item("acme/new", "v1"))
	h.checkpoint(unchanged, blobDigests(unchanged))
	observer := newFetchObserver()
	h.ctx = progress.WithObserver(h.ctx, observer)
	report := h.run(func(rc *RunConfig) { rc.DryRun = true })
	events := observer.snapshot()
	assertFetchLifecycle(t, events, "complete")
	assertFetchCount(t, events, "completed", "planned", report.Items)
	assertFetchCount(t, events, "completed", "skipped", report.SkippedUnchanged)
	assertFetchCount(t, events, "count", "blobs", 0)
	for _, event := range events {
		if event.Kind == "active" || event.Stage == "committing" {
			t.Errorf("dry run reported execution: %+v", event)
		}
	}
	if h.conn.fetchCount() != 0 {
		t.Fatal("observed dry run fetched an item")
	}
}

func TestProgressReportsLockFailure(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"))
	release, err := h.state.Lock(h.ctx, h.config().lockKey())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	observer := newFetchObserver()
	ctx := progress.WithObserver(h.ctx, observer)
	if _, err := Run(ctx, h.deps(), h.config()); err == nil {
		t.Fatal("expected held fetch lock to fail")
	}
	assertFetchLifecycle(t, observer.snapshot(), "failed")
}

func TestProgressListsTotalBeforeWorkersDrainAndReportsCancellation(t *testing.T) {
	h := newHarness(t, item("acme/a", "v1"))
	observer := newFetchObserver()
	ctx, cancel := context.WithCancel(progress.WithObserver(h.ctx, observer))
	defer cancel()
	started := make(chan struct{})
	deps := h.deps()
	deps.Connector = &waitingConnector{Connector: deps.Connector, started: started}
	finished := make(chan error, 1)
	go func() {
		_, err := Run(ctx, deps, h.config())
		finished <- err
	}()
	select {
	case event := <-observer.listed:
		if event.Total != 1 {
			t.Fatalf("listed total = %d, want 1", event.Total)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listing did not publish its total while fetch was blocked")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("item worker did not start")
	}
	assertFetchCount(t, observer.snapshot(), "completed", "fetched", 0)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled fetch error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled fetch did not finish")
	}
	events := observer.snapshot()
	assertFetchLifecycle(t, events, "cancelled")
	assertFetchCount(t, events, "completed", "cancelled", 1)
}

// fetchObserver records callbacks from concurrent workers and announces completed listing.
type fetchObserver struct {
	mu     sync.Mutex
	events []progress.Event
	listed chan progress.Event
}

func newFetchObserver() *fetchObserver {
	return &fetchObserver{listed: make(chan progress.Event, 1)}
}

func (o *fetchObserver) Observe(event progress.Event) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
	if event.Kind == "listed" {
		o.listed <- event
	}
}

func (o *fetchObserver) snapshot() []progress.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]progress.Event(nil), o.events...)
}

func assertFetchLifecycle(t *testing.T, events []progress.Event, outcome string) {
	t.Helper()
	if len(events) < 2 || events[0].Kind != "start" || events[0].Total != -1 || events[0].Stage != "waiting for lock" {
		t.Fatalf("missing indeterminate fetch start: %+v", events)
	}
	last := events[len(events)-1]
	if last.Kind != "done" || last.Stage != outcome {
		t.Errorf("final progress = %+v, want done/%s", last, outcome)
	}
	for _, event := range events {
		if event.Phase != "fetch" || event.Datatype != testDatatype {
			t.Errorf("wrong event identity: %+v", event)
		}
	}
}

func assertFetchCount(t *testing.T, events []progress.Event, kind, stage string, want int) {
	t.Helper()
	var got int
	for _, event := range events {
		if event.Kind == kind && event.Stage == stage {
			got += event.Count
		}
	}
	if got != want {
		t.Errorf("progress %s/%s = %d, want %d", kind, stage, got, want)
	}
}

// waitingConnector keeps a fetch active until cancellation, while delegating enumeration.
type waitingConnector struct {
	source.Connector
	started chan struct{}
}

func (c *waitingConnector) Fetch(ctx context.Context, _ string, _ source.Ref) (source.Result, error) {
	close(c.started)
	<-ctx.Done()
	return source.Result{}, fmt.Errorf("waiting for source: %w", ctx.Err())
}
