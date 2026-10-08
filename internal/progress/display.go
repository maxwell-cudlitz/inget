// The display serializes progress updates and normal diagnostic writes. A periodic
// refresh keeps elapsed and active-stage times moving even during a slow model call.
package progress

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type activity struct {
	stage string
	since time.Time
}

// Display is a command-owned observer and a coordinated stderr writer.
type Display struct {
	mu        sync.Mutex
	out       io.Writer
	mode      string
	width     func() int
	now       func() time.Time
	stop      chan struct{}
	stopped   chan struct{}
	closed    bool
	writeErr  error
	lines     int
	started   time.Time
	lastPlain time.Time
	phase     string
	datatype  string
	stage     string
	status    string
	total     int
	seen      int
	completed int
	counters  map[string]int
	active    map[string]activity
}

// New returns an optional display. Auto is enabled only on a capable terminal;
// plain prints periodic lines without escape sequences, and off changes nothing.
func New(out io.Writer, mode string) (*Display, error) {
	resolved, err := resolveMode(out, mode)
	if err != nil || resolved == "off" {
		return nil, err
	}
	d := &Display{out: out, mode: resolved, width: func() int { return terminalWidth(out) },
		now: time.Now, stop: make(chan struct{}), stopped: make(chan struct{}), total: -1}
	go d.refresh()
	return d, nil
}

// Observe applies a worker update. Progress never performs work on the worker's behalf.
func (d *Display) Observe(e Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	now := d.now()
	force := false
	switch e.Kind {
	case "start":
		if !d.started.IsZero() {
			if d.status == "running" {
				d.clearLocked()
			} else {
				d.renderLocked(true)
				d.lines = 0 // Retain the previous run's final summary above the next one.
			}
		}
		d.started, d.lastPlain = now, time.Time{}
		d.phase, d.datatype, d.stage, d.status = e.Phase, e.Datatype, e.Stage, "running"
		if d.stage == "" {
			d.stage = "processing items"
		}
		d.total, d.seen, d.completed = e.Total, 0, 0
		d.counters, d.active = make(map[string]int), make(map[string]activity)
		force = true
	case "enumerated":
		d.seen += e.Count
	case "listed":
		d.total = e.Total
		force = true
	case "active":
		d.active[e.Item] = activity{activityStage(e), now}
	case "stage":
		if e.Item == "" {
			d.stage = e.Stage
		} else {
			d.active[e.Item] = activity{activityStage(e), now}
		}
	case "completed":
		delete(d.active, e.Item)
		d.completed += e.Count
		d.counters[e.Stage] += e.Count
	case "count":
		d.counters[e.Stage] += e.Count
	case "done":
		d.status = e.Stage
		if d.status == "complete" && d.counters["failed"] > 0 {
			d.status = "complete with failures"
		}
		force = true
	}
	if force || now.Sub(d.lastPlain) >= time.Second {
		d.renderLocked(force)
	}
}

// Write clears the live panel while ordinary redacted logs are written, then redraws
// it. This keeps JSON diagnostics readable and progress away from program stdout.
func (d *Display) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.clearLocked()
	}
	n, err := d.out.Write(p)
	if err != nil {
		return n, fmt.Errorf("writing command diagnostics: %w", err)
	}
	if !d.closed && d.mode == "terminal" {
		d.renderLocked(true)
	}
	return n, nil
}

// Close stops refresh and leaves the final panel visible. A command that exits without
// a lifecycle completion is marked stopped, never silently promoted to complete.
func (d *Display) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	if d.status == "running" {
		d.status = "stopped"
	}
	d.renderLocked(true)
	d.closed = true
	err := d.writeErr
	close(d.stop)
	d.mu.Unlock()
	<-d.stopped
	if err != nil {
		// An output failure must not alter ingestion. Report it once when closing.
		_, _ = fmt.Fprintf(d.out, "progress output failed: %v\n", err)
	}
}

func (d *Display) refresh() {
	defer close(d.stopped)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.mu.Lock()
			if !d.closed && d.status == "running" {
				d.renderLocked(false)
			}
			d.mu.Unlock()
		}
	}
}
