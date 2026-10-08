// Rendering keeps counts honest while enumeration has no known denominator. Display
// text is ASCII escaped and clipped to terminal width so source names cannot control it.
package progress

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (d *Display) renderLocked(force bool) {
	if d.started.IsZero() || d.writeErr != nil {
		return
	}
	now := d.now()
	if d.mode == "plain" && !force && now.Sub(d.lastPlain) < time.Second {
		return
	}
	d.clearLocked()
	lines := d.frame(now)
	if d.mode == "plain" {
		lines = lines[:min(len(lines), 7)]
		lines = []string{strings.Join(lines, " | ")}
	}
	for _, line := range lines {
		if d.mode == "terminal" {
			line = clip(line, max(1, d.width()-1))
		}
		if _, err := fmt.Fprintln(d.out, line); err != nil {
			d.writeErr = fmt.Errorf("writing progress: %w", err)
			return
		}
	}
	if d.mode == "terminal" {
		d.lines = len(lines)
	}
	d.lastPlain = now
}

func (d *Display) clearLocked() {
	if d.lines == 0 || d.writeErr != nil {
		return
	}
	for range d.lines {
		if _, err := fmt.Fprint(d.out, "\x1b[1A\r\x1b[2K"); err != nil {
			d.writeErr = fmt.Errorf("clearing progress: %w", err)
			return
		}
	}
	d.lines = 0
}

func (d *Display) frame(now time.Time) []string {
	elapsed := now.Sub(d.started)
	header := fmt.Sprintf("INGET  %s  %s  [%s]",
		safe(d.phase), safe(d.datatype), safe(d.status))
	var work string
	if d.total < 0 {
		work = fmt.Sprintf("Discovering: %d items seen | %d finished", d.seen, d.completed)
	} else {
		fraction := float64(d.completed) / float64(max(d.total, 1))
		if d.total == 0 && d.status == "complete" {
			fraction = 1
		}
		fraction = math.Min(fraction, 1)
		filled := int(fraction * 20)
		work = fmt.Sprintf("[%s%s] %5.1f%%  %d/%d finished",
			strings.Repeat("=", filled), strings.Repeat("-", 20-filled), fraction*100, d.completed, d.total)
	}
	var counts, metrics string
	if d.phase == "fetch" {
		counts = fmt.Sprintf("Fetched %d | skipped %d | failed %d",
			d.counters["fetched"], d.counters["skipped"], d.counters["failed"])
		metrics = fmt.Sprintf("Fragments %d | warnings %d | blobs %d",
			d.counters["fragments"], d.counters["warnings"], d.counters["blobs"])
		if d.counters["planned"] > 0 {
			counts += fmt.Sprintf(" | planned %d", d.counters["planned"])
		}
	} else {
		counts = fmt.Sprintf("Processed %d | failed %d | cached views %d",
			d.counters["processed"], d.counters["failed"], d.counters["skipped_views"])
		metrics = fmt.Sprintf("Fragments %d | views %d | embeddings %d",
			d.counters["fragments"], d.counters["views"], d.counters["embeddings"])
	}
	lines := []string{header, work, counts, metrics,
		fmt.Sprintf("Elapsed %s | active workers %d", duration(elapsed), len(d.active)),
		"Stage: " + safe(d.stage)}
	ids := make([]string, 0, len(d.active))
	for id := range d.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids[:min(len(ids), 3)] {
		a := d.active[id]
		lines = append(lines, fmt.Sprintf("  %s  %s (%s)", safe(id), safe(a.stage), duration(now.Sub(a.since))))
	}
	if len(ids) > 3 {
		lines = append(lines, fmt.Sprintf("  +%d other active workers", len(ids)-3))
	}
	return lines
}

func activityStage(e Event) string {
	if e.Detail != "" {
		return e.Stage + ": " + e.Detail
	}
	return e.Stage
}

func duration(d time.Duration) string { return d.Round(time.Second).String() }

func safe(value string) string {
	quoted := strconv.QuoteToASCII(value)
	return quoted[1 : len(quoted)-1]
}

func clip(value string, width int) string {
	if len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	return value[:width-3] + "..."
}
