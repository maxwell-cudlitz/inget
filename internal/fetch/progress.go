// Fetch progress events describe observed work without changing the committed artifact or report.
// Enumeration remains indeterminate until listing finishes; workers never read its counters.
package fetch

import (
	"context"
	"errors"

	"github.com/maxwell-cudlitz/inget/internal/progress"
)

// progress attaches the immutable datatype identity to one observation.
func (r *runner) progress(ctx context.Context, kind, item, stage string, total, count int) {
	emitProgress(ctx, r.cfg.Datatype, kind, item, stage, total, count)
}

// emitProgress is also usable before the runner has been initialized.
func emitProgress(ctx context.Context, datatype, kind, item, stage string, total, count int) {
	progress.Emit(ctx, progress.Event{
		Phase: "fetch", Datatype: datatype, Kind: kind, Item: item, Stage: stage,
		Total: total, Count: count,
	})
}

// finishProgress runs after lock release and reports the invocation's existing result.
func finishProgress(ctx context.Context, datatype string, err error) {
	stage := "complete"
	if err != nil {
		stage = "failed"
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		stage = "cancelled"
	}
	emitProgress(ctx, datatype, "done", "", stage, -1, 0)
}
