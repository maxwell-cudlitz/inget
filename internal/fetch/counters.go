// Counter snapshots and the bounded manifest warning list. Progress callbacks run outside
// the counter mutex, so an observer cannot block another worker while a snapshot is taken.
package fetch

import (
	"context"
	"fmt"
)

// warn records a non-fatal problem. The manifest's array is bounded; the count is not, so a run
// that hit a thousand problems still says so even though it lists two hundred.
func (r *runner) warn(ctx context.Context, line string) {
	r.mu.Lock()
	r.warningCount++
	if len(r.warnings) < maxWarnings {
		r.warnings = append(r.warnings, line)
	}
	r.mu.Unlock()
	r.progress(ctx, "count", "", "warnings", -1, 1)
}

// report snapshots the counters.
func (r *runner) report(runID string) *Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	warnings := append([]string(nil), r.warnings...)
	if r.warningCount > len(r.warnings) {
		warnings = append(warnings, fmt.Sprintf("and %d further warning(s) not listed", r.warningCount-len(r.warnings)))
	}
	return &Report{
		RunID:            runID,
		Source:           r.cfg.Source,
		Datatype:         r.cfg.Datatype,
		Scope:            string(r.cfg.Scope),
		DryRun:           r.cfg.DryRun,
		Enumerated:       r.enumerated,
		Items:            r.items,
		SkippedUnchanged: r.skipped,
		Failed:           r.failed,
		Fragments:        r.fragments,
		BlobsWritten:     r.blobsWritten,
		BlobsReused:      r.blobsReused,
		Truncated:        r.truncated,
		Warnings:         warnings,
	}
}
