// Progress tests cover honest totals, concurrent updates, warning interleaving, output
// safety, and cleanup. They never contact a source, model, or database.
package progress

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDisplay(out *bytes.Buffer, mode string) *Display {
	stopped := make(chan struct{})
	close(stopped)
	return &Display{out: out, mode: mode, width: func() int { return 72 },
		now:  func() time.Time { return time.Unix(100, 0) },
		stop: make(chan struct{}), stopped: stopped, total: -1}
}

func TestUnknownTotalAndFailedCompletion(t *testing.T) {
	var out bytes.Buffer
	d := testDisplay(&out, "plain")
	d.Observe(Event{Kind: "start", Phase: "fetch", Datatype: "github/repo", Total: -1})
	d.Observe(Event{Kind: "enumerated", Count: 4})
	d.Observe(Event{Kind: "completed", Stage: "fetched", Count: 2})
	d.Observe(Event{Kind: "completed", Stage: "skipped", Count: 1})
	d.Observe(Event{Kind: "completed", Stage: "failed", Count: 1})
	if frame := strings.Join(d.frame(d.now()), "\n"); strings.Contains(frame, "%") || !strings.Contains(frame, "4 items seen") {
		t.Fatalf("unknown denominator was misrepresented: %s", frame)
	}
	d.Observe(Event{Kind: "listed", Total: 4})
	d.Observe(Event{Kind: "done", Stage: "failed"})
	d.Close()
	for _, want := range []string{"100.0%", "4/4 finished", "[failed]", "Fetched 2 | skipped 1 | failed 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("display lacks %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("plain output emitted terminal escapes")
	}
}

func TestConcurrentObserverTotalsAndCleanup(t *testing.T) {
	var out bytes.Buffer
	d, err := New(&out, "plain")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithObserver(context.Background(), d)
	Emit(ctx, Event{Kind: "start", Phase: "ingest", Total: 100})
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			id := fmt.Sprint(i)
			Emit(ctx, Event{Kind: "active", Item: id, Stage: "generating"})
			Emit(ctx, Event{Kind: "count", Stage: "views", Count: 7})
			Emit(ctx, Event{Kind: "count", Stage: "embeddings", Count: 7})
			Emit(ctx, Event{Kind: "completed", Item: id, Stage: "processed", Count: 1})
		})
	}
	wg.Wait()
	Emit(ctx, Event{Kind: "done", Stage: "complete"})
	Close(ctx)
	Close(ctx)
	Emit(ctx, Event{Kind: "completed", Count: 100}) // Closed observers ignore late updates.
	if d.completed != 100 || len(d.active) != 0 || d.counters["embeddings"] != 700 {
		t.Fatalf("incorrect concurrent counters: %d active=%d %v", d.completed, len(d.active), d.counters)
	}
	select {
	case <-d.stopped:
	default:
		t.Fatal("refresh goroutine was not stopped")
	}
	if !strings.Contains(out.String(), "embeddings 700") {
		t.Fatal("final counters were not rendered")
	}
}

func TestInterleavedLogsAndTerminalSafety(t *testing.T) {
	var out bytes.Buffer
	d := testDisplay(&out, "terminal")
	d.Observe(Event{Kind: "start", Phase: "fetch", Datatype: "repo\x1b[2J", Total: -1})
	d.Observe(Event{Kind: "active", Item: "owner/a\n\x1b[2J", Stage: strings.Repeat("x", 100)})
	if _, err := d.Write([]byte("{\"level\":\"WARN\",\"msg\":\"test warning\"}\n")); err != nil {
		t.Fatal(err)
	}
	d.Observe(Event{Kind: "done", Stage: "cancelled"})
	d.Close()
	before := out.Len()
	if _, err := d.Write([]byte("final diagnostic\n")); err != nil {
		t.Fatal(err)
	}
	if after := out.String()[before:]; after != "final diagnostic\n" {
		t.Fatalf("a post-close diagnostic erased the final panel: %q", after)
	}
	if strings.Contains(out.String(), "\x1b[2J") || !strings.Contains(out.String(), "test warning") {
		t.Fatal("source escaped terminal control or warning disappeared")
	}
	for _, line := range d.frame(d.now()) {
		if len(clip(line, 20)) > 20 {
			t.Fatal("narrow-terminal clipping failed")
		}
	}
}

func TestModesAndEarlyClose(t *testing.T) {
	for _, mode := range []string{"off", "auto"} {
		d, err := New(&bytes.Buffer{}, mode)
		if err != nil || d != nil {
			t.Fatalf("non-terminal mode %s returned %v, %v", mode, d, err)
		}
	}
	if _, err := New(&bytes.Buffer{}, "invalid"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	var out bytes.Buffer
	d := testDisplay(&out, "plain")
	d.Observe(Event{Kind: "start", Phase: "ingest", Total: 4})
	d.Observe(Event{Kind: "completed", Stage: "processed", Count: 1})
	d.Close()
	if !strings.Contains(out.String(), "[stopped]") || strings.Contains(out.String(), "100.0%") {
		t.Fatal("early exit was promoted to successful completion")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := WithObserver(ctx, nil); got.Err() != context.Canceled {
		t.Fatal("nil observer changed cancellation")
	}
	Emit(ctx, Event{Kind: "start"})
	Close(ctx)
}
