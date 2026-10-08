// Capture-observer integration tests verify progress against the fake model's real calls.
package pipeline

import (
	"sync"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/progress"
)

type progressCapture struct {
	mu     sync.Mutex
	events []progress.Event
	on     func(progress.Event)
}

func (c *progressCapture) Observe(event progress.Event) {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
	if c.on != nil {
		c.on(event)
	}
}

func (c *progressCapture) take() []progress.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	events := c.events
	c.events = nil
	return events
}

func eventCount(events []progress.Event, kind, stage string) int {
	n := 0
	for _, event := range events {
		if event.Kind == kind && event.Stage == stage {
			n += event.Count
		}
	}
	return n
}

func eventMatches(events []progress.Event, kind, stage string) []progress.Event {
	var found []progress.Event
	for _, event := range events {
		if event.Kind == kind && event.Stage == stage {
			found = append(found, event)
		}
	}
	return found
}

func assertTerminal(t *testing.T, events []progress.Event, status string) {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Kind != "done" || events[len(events)-1].Stage != status {
		t.Fatalf("last progress event = %v, want done/%s", events, status)
	}
	for _, event := range events {
		if event.Phase != "ingest" || event.Datatype != testDatatype {
			t.Errorf("progress identity = %q/%q", event.Phase, event.Datatype)
		}
		if event.Item != "" && event.Item != testItemID {
			t.Errorf("unexpected item or source content in event: %+v", event)
		}
	}
}

func TestIngestProgressCountsActualCascadeWork(t *testing.T) {
	h := newCascadeHarness(t)
	capture := &progressCapture{}
	h.ctx = progress.WithObserver(h.ctx, capture)
	for _, tc := range []struct {
		name      string
		changed   int
		version   string
		processed int
		fragments int
		views     int
		skipped   int
	}{
		{"initial", noChange, "", 1, numFrags, numViews, 0},
		{"identical", noChange, "", 0, 0, 0, 0},
		{"one change", 50, "v2", 1, 1, 3, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.reset()
			h.writeRun(baseVersion, tc.changed, tc.version)
			if _, _, err := h.run(); err != nil {
				t.Fatal(err)
			}
			events := capture.take()
			assertTerminal(t, events, "complete")
			initial := eventMatches(events, "start", "finding artifacts")
			if len(initial) != 1 || initial[0].Total != -1 {
				t.Fatalf("initial discovery progress = %+v", initial)
			}
			starts := eventMatches(events, "start", "")
			if len(starts) != 1 || starts[0].Total != tc.processed {
				t.Fatalf("artifact start = %+v, want total %d", starts, tc.processed)
			}
			for stage, want := range map[string]int{
				"fragments": tc.fragments, "views": tc.views,
				"embeddings": tc.views, "skipped_views": tc.skipped,
			} {
				if got := eventCount(events, "count", stage); got != want {
					t.Errorf("%s progress count = %d, want %d", stage, got, want)
				}
			}
			if got := eventCount(events, "completed", "processed"); got != tc.processed {
				t.Errorf("processed progress = %d, want %d", got, tc.processed)
			}
			if h.gen.Calls() != tc.fragments+tc.views || h.emb.Embedded() != tc.views {
				t.Errorf("model calls do not match progress: generation=%d embedding=%d", h.gen.Calls(), h.emb.Embedded())
			}
			if tc.processed > 0 {
				assertProgressStages(t, events)
			}
		})
	}
}

func assertProgressStages(t *testing.T, events []progress.Event) {
	t.Helper()
	want := []string{"enrichment", "generation", "embedding", "storage", "checkpoint"}
	position := 0
	for _, event := range events {
		if event.Kind == "stage" && position < len(want) && event.Stage == want[position] {
			position++
		}
	}
	if position != len(want) || len(eventMatches(events, "active", "")) != 1 {
		t.Errorf("missing active item or ordered stages %v", want)
	}
}
