// The artifact high-water mark: how far a datatype has consumed its producer's output.
//
// A consumer that only ever reads the newest committed run is correct for one producer that
// enumerates everything on a schedule, and silently lossy for many producers that each commit
// one item — the pattern a CI job submitting its own repository creates. This mark is what
// makes the second topology work: every run newer than it is pending, and `inget run` drains
// them oldest first.
//
// The mark moves forward only. The upsert compares run identifiers, which are ULIDs and so
// order lexically, and refuses a value that is not newer. Rewinding it would replay work
// already paid for, and there is no caller for which that is the intent.
package state

import (
	"context"
	"fmt"
)

const (
	consumedSelectSQL = `SELECT run_id FROM consumed_artifacts WHERE datatype = ?`

	consumedUpsertSQL = `
INSERT INTO consumed_artifacts (datatype, run_id) VALUES (?, ?)
ON CONFLICT (datatype) DO UPDATE SET
    run_id      = excluded.run_id,
    consumed_at = CURRENT_TIMESTAMP
WHERE excluded.run_id > consumed_artifacts.run_id`
)

// ConsumedArtifactRun implements Store.
func (s *store) ConsumedArtifactRun(ctx context.Context, datatype string) (string, error) {
	var runID string
	found, err := s.get(ctx, consumedSelectSQL, []any{datatype}, &runID)
	if err != nil {
		return "", fmt.Errorf("reading the consumed artifact run of %s: %w", datatype, err)
	}
	if !found {
		return "", nil
	}
	return runID, nil
}

// PutConsumedArtifactRun implements Store.
func (s *store) PutConsumedArtifactRun(ctx context.Context, datatype, runID string) error {
	if datatype == "" || runID == "" {
		return fmt.Errorf("consumed artifact run needs a datatype and a run id, got %q and %q", datatype, runID)
	}
	if _, err := s.exec(ctx, consumedUpsertSQL, datatype, runID); err != nil {
		return fmt.Errorf("recording consumed artifact run %s for %s: %w", runID, datatype, err)
	}
	return nil
}
