// Run lifecycle: the artifact backlog, lock acquisition, resumption, tombstones, and
// completion.
//
// Run is the entrypoint for a pipeline execution. It resolves which committed artifact runs
// are still owed work, then for each one reconciles, estimates, resolves the run row, applies
// tombstones, and hands the work queue to the pool in workers.go.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// Run executes a full pipeline run for the configured datatype, draining the artifact backlog.
//
// In dry-run mode (rc.DryRun) it reconciles the oldest pending artifact run, estimates it, and
// returns the plan without taking the datatype lock, claiming work, or calling a model: a plan
// is a read, and failing it because a run holds the lock would make the cost guard unavailable
// exactly when someone is about to spend money. The plan reports the whole backlog in
// PendingArtifactRuns but prices only the run it describes, so a backlog of more than one is
// warned about rather than silently under-quoted.
func Run(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig) (*Plan, *Stats, error) {
	cfg := deps.Config
	ingestInitial(ctx, cfg.Name, rc.DryRun)
	slog.InfoContext(ctx, "pipeline starting",
		"datatype", cfg.Name, "concurrency", rc.Concurrency, "dry_run", rc.DryRun)

	pending, err := pendingRuns(ctx, deps, arts)
	if err != nil {
		if !rc.DryRun {
			ingestDone(ctx, cfg.Name, nil, err)
		}
		return nil, nil, err
	}

	if rc.DryRun {
		result, err := planOnly(ctx, deps, arts, rc, pending[0])
		if err != nil {
			return nil, nil, err
		}
		result.plan.PendingArtifactRuns = len(pending)
		if len(pending) > 1 {
			slog.WarnContext(ctx, "the backlog holds more than one artifact run; the estimate prices the oldest",
				"datatype", cfg.Name, "pending_runs", len(pending), "priced_run", pending[0])
		}
		return &result.plan, nil, nil
	}

	ingestProgress(ctx, cfg.Name, "stage", "", "waiting for lock", 0, 0)
	release, err := deps.State.Lock(ctx, cfg.Name)
	if err != nil {
		ingestDone(ctx, cfg.Name, nil, err)
		return nil, nil, fmt.Errorf("acquiring lock for %s: %w", cfg.Name, err)
	}
	defer func() {
		if err := release(); err != nil {
			slog.WarnContext(ctx, "releasing lock", "datatype", cfg.Name, "error", err)
		}
	}()

	return drain(ctx, deps, arts, rc, pending)
}

// drain consumes each pending artifact run in turn, advancing the high-water mark behind it.
//
// The returned plan is the last one processed and the stats are the invocation's total. An
// error stops the drain: whatever it was, the next run in the backlog would meet it too, and
// the mark still names the last run that finished cleanly.
func drain(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig, pending []string) (*Plan, *Stats, error) {
	cfg := deps.Config
	total := &Stats{}
	advancing := true
	var last *Plan

	for i, artifactRunID := range pending {
		plan, stats, err := consume(ctx, deps, arts, rc, artifactRunID)
		if plan != nil {
			plan.PendingArtifactRuns = len(pending) - i
			last = plan
		}
		if err != nil {
			ingestDone(ctx, cfg.Name, stats, err)
			return last, nil, err
		}
		total.add(stats)

		// A restricted or partly failed pass leaves the mark where it is, so the same
		// artifact run is reconciled again next invocation and its unfinished items retry.
		// Once that has happened the mark must not jump over it either, which is what
		// advancing tracks.
		clean := stats != nil && stats.ItemsFailed == 0 && !ShuttingDown(ctx) && !rc.DryRun && plan != nil && !plan.Restricted
		if advancing && clean {
			if err := deps.State.PutConsumedArtifactRun(ctx, cfg.Name, artifactRunID); err != nil {
				ingestDone(ctx, cfg.Name, stats, err)
				return last, nil, err
			}
		} else {
			advancing = false
			slog.InfoContext(ctx, "leaving the artifact mark where it is",
				"datatype", cfg.Name, "artifact_run_id", artifactRunID,
				"failed_items", failedCount(stats), "restricted", plan != nil && plan.Restricted)
		}
		ingestDone(ctx, cfg.Name, stats, nil)
		if ShuttingDown(ctx) {
			slog.InfoContext(ctx, "shutdown requested, stopping the drain",
				"datatype", cfg.Name, "consumed", i+1, "pending", len(pending))
			break
		}
	}
	return last, total, nil
}

// failedCount is the failure count of a possibly absent stats snapshot, for one log field.
func failedCount(stats *Stats) int {
	if stats == nil {
		return 0
	}
	return stats.ItemsFailed
}

// consume processes exactly one artifact run: reconcile, resolve the run row, tombstone, work.
func consume(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig, artifactRunID string) (*Plan, *Stats, error) {
	ingestProgress(ctx, deps.Config.Name, "stage", "", "reconciling", 0, 0)
	result, err := planOnly(ctx, deps, arts, rc, artifactRunID)
	if err != nil {
		return nil, nil, err
	}
	ingestProgress(ctx, deps.Config.Name, "start", "", "", len(result.plan.WorkItems), 0)

	runID, resuming, err := resolveRun(ctx, deps, rc, result)
	if err != nil {
		return nil, nil, err
	}
	result.plan.Resuming = resuming
	result.plan.RunID = runID

	stats := &statsCollector{}
	if err := processTombstones(ctx, deps, result.tombstones, stats); err != nil {
		_ = deps.State.FinishRun(ctx, runID, state.RunFailed, nil)
		return &result.plan, nil, err
	}

	final, err := runWorkers(ctx, deps, arts, rc, runID, result, stats)
	if err != nil {
		return &result.plan, nil, err
	}
	return &result.plan, final, nil
}

// planOnly reconciles one artifact run against state, surfaces any signature change, and — in
// plan mode — estimates what the work would cost. Both modes go through it, which is what makes
// `inget plan` and `inget run` agree on the work set by construction rather than by review.
func planOnly(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig, artifactRunID string) (*reconcileResult, error) {
	result, err := reconcile(ctx, deps, arts, rc, artifactRunID)
	if err != nil {
		return nil, err
	}
	if err := surfaceSignatureChanges(ctx, deps, rc, result); err != nil {
		return nil, err
	}
	if !rc.DryRun {
		return result, nil
	}
	if err := estimateWork(ctx, deps, rc, result); err != nil {
		return nil, err
	}
	return result, nil
}

// resolveRun either resumes an existing run or starts a new one.
func resolveRun(ctx context.Context, deps Deps, rc RunConfig, result *reconcileResult) (string, bool, error) {
	cfg := deps.Config

	existing, found, err := deps.State.ResumableRun(ctx, rc.Binary, cfg.Name, rc.ConfigHash)
	if err != nil {
		return "", false, fmt.Errorf("checking for resumable run: %w", err)
	}
	if found {
		slog.InfoContext(ctx, "resuming previous run", "run_id", existing, "datatype", cfg.Name)
		reset, err := deps.State.ResetClaims(ctx, existing, cfg.Name)
		if err != nil {
			return "", false, fmt.Errorf("resetting claims for run %s: %w", existing, err)
		}
		if reset > 0 {
			slog.InfoContext(ctx, "reset abandoned claims", "count", reset)
		}
		if result.rebuild {
			// An explicit signature rebuild must revisit items completed under the previous
			// prompt or model, including after an interrupted rebuild.
			if err := deps.State.ReplaceWork(ctx, existing, cfg.Name, result.plan.WorkItems); err != nil {
				return "", false, fmt.Errorf("replacing work for signature rebuild %s: %w", existing, err)
			}
			return existing, true, nil
		}
		// Re-enqueue the work items (idempotent: already-done items are untouched).
		if err := deps.State.EnqueueWork(ctx, existing, cfg.Name, result.plan.WorkItems); err != nil {
			return "", false, fmt.Errorf("re-enqueueing work for run %s: %w", existing, err)
		}
		return existing, true, nil
	}

	runID, err := artifact.NewRunID()
	if err != nil {
		return "", false, fmt.Errorf("generating run id: %w", err)
	}
	// A run restricted by --only or --limit has not looked at everything, so it records itself
	// as partial: the scope on the row is what a later reader uses to know whether the absence
	// of an item in this run meant anything (D6).
	scope := state.ScopeFull
	if result.partial {
		scope = state.ScopePartial
	}
	run := state.Run{
		ID:         runID,
		Binary:     rc.Binary,
		Datatype:   cfg.Name,
		Scope:      scope,
		ConfigHash: rc.ConfigHash,
	}
	if err := deps.State.StartRun(ctx, run); err != nil {
		return "", false, fmt.Errorf("starting run: %w", err)
	}
	if err := deps.State.EnqueueWork(ctx, runID, cfg.Name, result.plan.WorkItems); err != nil {
		return "", false, fmt.Errorf("enqueueing work for run %s: %w", runID, err)
	}
	slog.InfoContext(ctx, "started new run", "run_id", runID, "work_items", len(result.plan.WorkItems))
	return runID, false, nil
}

// processTombstones deletes tombstoned items from state and destinations.
func processTombstones(ctx context.Context, deps Deps, tombstones []string, stats *statsCollector) error {
	if len(tombstones) == 0 {
		return nil
	}
	slog.InfoContext(ctx, "processing tombstones", "count", len(tombstones))

	if err := deps.State.Tombstone(ctx, deps.Config.Name, tombstones); err != nil {
		return fmt.Errorf("tombstoning items: %w", err)
	}
	for _, destName := range deps.Config.Destinations {
		dest, ok := deps.Destinations[destName]
		if !ok {
			return fmt.Errorf("destination %q not found in deps", destName)
		}
		for _, itemID := range tombstones {
			if err := dest.DeleteItem(ctx, deps.Config.Name, itemID); err != nil {
				return fmt.Errorf("deleting %s from %s: %w", itemID, destName, err)
			}
		}
	}
	stats.addTombstones(len(tombstones))
	return nil
}
