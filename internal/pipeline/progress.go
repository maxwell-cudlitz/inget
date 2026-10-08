// Observer hooks for ingestion: item identifiers, execution stages, and actual work counts.
// Counts are deltas and are emitted where successful work is counted, never for cache hits.
package pipeline

import (
	"context"

	"github.com/maxwell-cudlitz/inget/internal/progress"
)

func ingestProgress(ctx context.Context, datatype, kind, item, stage string, total, count int) {
	progress.Emit(ctx, progress.Event{
		Phase: "ingest", Datatype: datatype, Kind: kind, Item: item, Stage: stage,
		Total: total, Count: count,
	})
}

func ingestInitial(ctx context.Context, datatype string, dryRun bool) {
	if !dryRun {
		ingestProgress(ctx, datatype, "start", "", "finding artifacts", -1, 0)
	}
}

// ingestDone closes one artifact's display after its work and consumed mark have finished.
// Cancellation and graceful interruption take precedence over partial item failures.
func ingestDone(ctx context.Context, datatype string, stats *Stats, err error) {
	status := "complete"
	switch {
	case ctx.Err() != nil:
		status = "cancelled"
	case ShuttingDown(ctx):
		status = "interrupted"
	case err != nil || failedCount(stats) > 0:
		status = "failed"
	}
	ingestProgress(ctx, datatype, "done", "", status, 0, 0)
}

// ingestDetail identifies the fragment/view currently using a model permit.
func ingestDetail(ctx context.Context, datatype, item, stage, detail string) {
	progress.Emit(ctx, progress.Event{
		Phase: "ingest", Datatype: datatype, Kind: "stage", Item: item, Stage: stage, Detail: detail,
	})
}
