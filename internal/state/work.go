// Per-item work checkpointing, which is what makes an interrupted run resume instead of
// restart (D13). A pod evicted 80% of the way through a 500k-item run comes back at 80%.
package state

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

const (
	// DO NOTHING, not DO UPDATE: re-enqueueing on resume must not reset an item that is
	// already done back to pending.
	workEnqueueSQL = `
INSERT INTO work (run_id, datatype, item_id, status) VALUES (?, ?, ?, '` + WorkPending + `')
ON CONFLICT (run_id, datatype, item_id) DO NOTHING`

	// The subquery selects the batch and the outer UPDATE claims it, so selecting and
	// claiming cannot interleave with another worker. On PostgreSQL the row locking
	// clause makes a concurrent claimer skip these rows rather than block on them; on
	// SQLite the statement holds the single write lock for its duration, which has the
	// same effect. ORDER BY item_id decides which items a batch contains; the order they
	// come back in is normalized by the caller, since RETURNING does not preserve it.
	workClaimSQL = `
UPDATE work SET status = '` + WorkClaimed + `', attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
WHERE (run_id, datatype, item_id) IN (
    SELECT run_id, datatype, item_id FROM work
    WHERE run_id = ? AND datatype = ? AND status = '` + WorkPending + `'
    ORDER BY item_id LIMIT ?%s
)
RETURNING item_id`

	// Only the lock holder calls this, so anything still claimed belongs to a process
	// that is gone.
	workResetSQL = `
UPDATE work SET status = '` + WorkPending + `', updated_at = CURRENT_TIMESTAMP
WHERE run_id = ? AND datatype = ? AND status = '` + WorkClaimed + `'`

	workCompleteSQL = `
UPDATE work SET status = ?, error = ?, updated_at = CURRENT_TIMESTAMP
WHERE run_id = ? AND datatype = ? AND item_id = ?`
)

// EnqueueWork implements Store.
func (s *store) EnqueueWork(ctx context.Context, runID, datatype string, ids []string) error {
	rows := make([][]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []any{runID, datatype, id})
	}
	err := s.inTx(ctx, func(t tx) error {
		return t.execMany(ctx, workEnqueueSQL, rows)
	})
	if err != nil {
		return fmt.Errorf("enqueueing %d %s items in run %s: %w", len(ids), datatype, runID, err)
	}
	return nil
}

// ClaimWork implements Store.
func (s *store) ClaimWork(ctx context.Context, runID, datatype string, n int) ([]string, error) {
	if n <= 0 {
		return nil, fmt.Errorf("claiming work in run %s: batch size %d must be positive", runID, n)
	}
	query := fmt.Sprintf(workClaimSQL, s.d.skipLocked)

	var claimed []string
	err := s.each(ctx, query, []any{runID, datatype, n}, func(rows *sql.Rows) error {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			return fmt.Errorf("scanning claimed item: %w", err)
		}
		claimed = append(claimed, itemID)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claiming %d %s items in run %s: %w", n, datatype, runID, err)
	}
	// RETURNING yields rows in the order the UPDATE happened to touch them, which is not
	// the subquery's ORDER BY and differs between the drivers. The batch's membership is
	// already deterministic; sorting here makes its order deterministic too.
	slices.Sort(claimed)
	return claimed, nil
}

// ResetClaims implements Store.
func (s *store) ResetClaims(ctx context.Context, runID, datatype string) (int, error) {
	affected, err := s.exec(ctx, workResetSQL, runID, datatype)
	if err != nil {
		return 0, fmt.Errorf("reclaiming %s work in run %s: %w", datatype, runID, err)
	}
	return int(affected), nil
}

// CompleteWork implements Store.
func (s *store) CompleteWork(ctx context.Context, runID, datatype, itemID string, cause error) error {
	status, reason := WorkDone, ""
	if cause != nil {
		status, reason = WorkFailed, cause.Error()
	}
	affected, err := s.exec(ctx, workCompleteSQL, status, nullText(reason), runID, datatype, itemID)
	if err != nil {
		return fmt.Errorf("completing %s item %s in run %s: %w", datatype, itemID, runID, err)
	}
	if affected == 0 {
		return fmt.Errorf("completing %s item %s in run %s: not enqueued", datatype, itemID, runID)
	}
	return nil
}
