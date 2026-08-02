// Run history: what ran, over what, with which configuration, and how it ended.
package state

import (
	"context"
	"fmt"
	"slices"
)

// terminalStatuses are the statuses FinishRun accepts. A typo here would make an
// interrupted run unresumable, so the set is closed rather than free text.
var terminalStatuses = []string{RunOK, RunFailed, RunInterrupted}

const (
	// Re-opening a run is an upsert: a resumed process adopts the interrupted run's ID so
	// that the work queue it already made progress against is the one it keeps using.
	// started_at deliberately keeps the original value; the run is one run.
	runStartSQL = `
INSERT INTO runs (run_id, "binary", datatype, scope, status, config_hash)
VALUES (?, ?, ?, ?, '` + RunRunning + `', ?)
ON CONFLICT (run_id) DO UPDATE SET
    status      = '` + RunRunning + `',
    finished_at = NULL`

	runFinishSQL = `
UPDATE runs SET status = ?, stats = ?, finished_at = CURRENT_TIMESTAMP WHERE run_id = ?`

	// Run IDs are ULIDs, so ordering by the identifier orders by start time without
	// reading a timestamp. COALESCE rather than IS NOT DISTINCT FROM because SQLite
	// spells null-safe comparison differently and this runs once per process.
	runResumableSQL = `
SELECT run_id FROM runs
WHERE "binary" = ? AND COALESCE(datatype, '') = ? AND config_hash = ?
  AND status IN ('` + RunRunning + `', '` + RunInterrupted + `')
ORDER BY run_id DESC LIMIT 1`

	runStatusSQL = `
SELECT status FROM runs WHERE run_id = ?`
)

// StartRun implements Store.
func (s *store) StartRun(ctx context.Context, r Run) error {
	switch {
	case r.ID == "":
		return fmt.Errorf("run has no ID")
	case r.Binary == "":
		return fmt.Errorf("run %s has no binary name", r.ID)
	case r.Scope != ScopeFull && r.Scope != ScopePartial:
		return fmt.Errorf("run %s scope %q: want %s or %s", r.ID, r.Scope, ScopeFull, ScopePartial)
	}
	_, err := s.exec(ctx, runStartSQL, r.ID, r.Binary, nullText(r.Datatype), r.Scope, r.ConfigHash)
	if err != nil {
		return fmt.Errorf("starting run %s: %w", r.ID, err)
	}
	return nil
}

// FinishRun implements Store.
func (s *store) FinishRun(ctx context.Context, runID, status string, stats any) error {
	if !slices.Contains(terminalStatuses, status) {
		return fmt.Errorf("run %s status %q: want one of %v", runID, status, terminalStatuses)
	}
	encoded, err := marshalJSON(stats)
	if err != nil {
		return fmt.Errorf("run %s: %w", runID, err)
	}
	affected, err := s.exec(ctx, runFinishSQL, status, encoded, runID)
	if err != nil {
		return fmt.Errorf("finishing run %s: %w", runID, err)
	}
	if affected == 0 {
		return fmt.Errorf("finishing run %s: no such run", runID)
	}
	return nil
}

// ResumableRun implements Store.
func (s *store) ResumableRun(ctx context.Context, binary, datatype, configHash string) (string, bool, error) {
	var runID string
	found, err := s.get(ctx, runResumableSQL, []any{binary, datatype, configHash}, &runID)
	if err != nil {
		return "", false, fmt.Errorf("looking for a resumable %s run: %w", binary, err)
	}
	return runID, found, nil
}

// RunStatus implements Store.
func (s *store) RunStatus(ctx context.Context, runID string) (string, bool, error) {
	var status string
	found, err := s.get(ctx, runStatusSQL, []any{runID}, &status)
	if err != nil {
		return "", false, fmt.Errorf("reading status of run %s: %w", runID, err)
	}
	return status, found, nil
}
