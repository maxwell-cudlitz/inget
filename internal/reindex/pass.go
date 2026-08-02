// Run lifecycle: rebinding, run resolution, the claim loop, and the prune that follows.
//
// The shape mirrors internal/pipeline's run.go on purpose. A reindex is the same kind of thing —
// take the datatype lock, adopt or start a run, drain a work queue, close the run with the status
// it earned — and an operator reading one should recognise the other.
package reindex

import (
	"context"
	"log/slog"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/pipeline"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// rebind moves every named destination onto the configured embedder, recording which bindings
// actually changed.
func rebind(ctx context.Context, deps Deps, o Options, report *Report) error {
	emb := deps.Embedder
	for _, name := range o.Destinations {
		changed, err := deps.Destinations[name].RebindModel(ctx, emb.Model(), emb.Dims(), emb.Signature())
		if err != nil {
			return err
		}
		if changed {
			report.Rebound = append(report.Rebound, name)
		}
	}
	return nil
}

// runPass resolves the run row, drains the work queue, and closes the run with the status the
// pass earned.
func runPass(ctx context.Context, deps Deps, o Options, items []string, report *Report) error {
	runID, resuming, err := resolveRun(ctx, deps, o, items)
	if err != nil {
		return err
	}
	report.RunID, report.Resuming = runID, resuming

	passErr := claimLoop(ctx, deps, o, runID, len(items), report)
	status := state.RunOK
	switch {
	case passErr != nil:
		status = state.RunFailed
	case report.Interrupted:
		status = state.RunInterrupted
	}
	if err := deps.State.FinishRun(ctx, runID, status, report); err != nil {
		if passErr != nil {
			return passErr
		}
		return err
	}
	return passErr
}

// resolveRun adopts an unfinished reindex of the same datatype and configuration, or starts a
// new one. Re-enqueueing is idempotent, so an adopted run keeps the progress it had.
func resolveRun(ctx context.Context, deps Deps, o Options, items []string) (string, bool, error) {
	existing, found, err := deps.State.ResumableRun(ctx, Binary, o.Datatype, o.ConfigHash)
	if err != nil {
		return "", false, err
	}
	if found {
		reset, err := deps.State.ResetClaims(ctx, existing, o.Datatype)
		if err != nil {
			return "", false, err
		}
		slog.InfoContext(ctx, "resuming reindex", "run_id", existing, "datatype", o.Datatype,
			"reset_claims", reset)
		if err := deps.State.EnqueueWork(ctx, existing, o.Datatype, items); err != nil {
			return "", false, err
		}
		return existing, true, nil
	}

	runID, err := artifact.NewRunID()
	if err != nil {
		return "", false, err
	}
	run := state.Run{
		ID: runID, Binary: Binary, Datatype: o.Datatype,
		Scope: scope(o), ConfigHash: o.ConfigHash,
	}
	if err := deps.State.StartRun(ctx, run); err != nil {
		return "", false, err
	}
	if err := deps.State.EnqueueWork(ctx, runID, o.Datatype, items); err != nil {
		return "", false, err
	}
	slog.InfoContext(ctx, "started reindex", "run_id", runID, "datatype", o.Datatype, "items", len(items))
	return runID, false, nil
}

// scope records how much of the datatype the pass covered, using the same vocabulary a fetch
// does: a view-restricted pass looked at part of it.
func scope(o Options) string {
	if o.fullScope() {
		return state.ScopeFull
	}
	return state.ScopePartial
}

// claimLoop drains the work queue in batches until it is empty or a shutdown is signalled.
func claimLoop(ctx context.Context, deps Deps, o Options, runID string, total int, report *Report) error {
	for {
		if pipeline.ShuttingDown(ctx) {
			slog.InfoContext(ctx, "shutdown requested, stopping reindex claims",
				"datatype", o.Datatype, "items_rewritten", report.Items)
			report.Interrupted = true
			return nil
		}
		ids, err := deps.State.ClaimWork(ctx, runID, o.Datatype, o.BatchSize)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := reindexBatch(ctx, deps, o, runID, ids, report); err != nil {
			return err
		}
		slog.InfoContext(ctx, "reindex progress", "datatype", o.Datatype,
			"done", report.Items+report.Unchanged+report.Failed, "total", total,
			"vectors", report.Vectors)
	}
}

// prune deletes the rows the new embedder never rewrote, which after a full pass are the rows
// state no longer knows about.
func prune(ctx context.Context, deps Deps, o Options, report *Report) error {
	if !o.fullScope() {
		slog.InfoContext(ctx, "skipping the prune after a view-restricted reindex",
			"datatype", o.Datatype, "views", o.Views)
		return nil
	}
	if report.Interrupted {
		slog.WarnContext(ctx, "skipping the prune after an interrupted reindex",
			"datatype", o.Datatype, "note", "re-run to finish, then the prune runs")
		return nil
	}
	for _, name := range o.Destinations {
		deleted, err := deps.Destinations[name].PruneStaleVectors(ctx, o.Datatype)
		if err != nil {
			return err
		}
		report.Pruned += deleted
	}
	return nil
}
