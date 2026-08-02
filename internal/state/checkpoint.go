// The per-item commit point: every guard an item's processing advanced, plus its work row,
// written together or not at all.
//
// The pipeline calls this once per item, after the destination upsert has returned. That
// ordering is the whole reason the method exists: each guard in state is a claim that work
// downstream of it already happened, so a guard committed before that work can fail is a
// claim the run cannot honour. An item whose views failed to generate must reconcile as
// changed on the next run, and it only does that if its fingerprint was never written.
package state

import (
	"context"
	"fmt"
)

// CheckpointItem implements Store.
func (s *store) CheckpointItem(ctx context.Context, datatype string, cp Checkpoint) error {
	if cp.RunID == "" {
		return fmt.Errorf("checkpointing %s/%s: no run id", datatype, cp.Item.ID)
	}
	item, err := itemArgs(datatype, cp.Item)
	if err != nil {
		return err
	}
	frags, err := fragmentRows(datatype, cp.Item.ID, cp.Fragments)
	if err != nil {
		return err
	}
	views := make([][]any, 0, len(cp.Views))
	for _, v := range cp.Views {
		args, err := viewArgs(datatype, cp.Item.ID, v)
		if err != nil {
			return err
		}
		views = append(views, args)
	}

	err = s.inTx(ctx, func(t tx) error {
		if err := t.exec(ctx, itemUpsertSQL, item...); err != nil {
			return err
		}
		if err := putFragments(ctx, t, datatype, cp.Item.ID, frags); err != nil {
			return err
		}
		if err := t.execMany(ctx, viewUpsertSQL, views); err != nil {
			return err
		}
		affected, err := t.execAffected(ctx, workCompleteSQL,
			WorkDone, nil, cp.RunID, datatype, cp.Item.ID)
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("item is not enqueued")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("checkpointing %s/%s in run %s: %w", datatype, cp.Item.ID, cp.RunID, err)
	}
	return nil
}
