// Run lifecycle: lock acquisition, resumption, tombstones, and completion.
//
// Run is the entrypoint for a pipeline execution. It reconciles, estimates, resolves the
// run row, applies tombstones, and hands the work queue to the pool in workers.go.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// Run executes a full pipeline run for the configured datatype.
//
// In dry-run mode (rc.DryRun) it reconciles, estimates, and returns the plan without
// taking the datatype lock, claiming work, or calling a model: a plan is a read, and
// failing it because a run holds the lock would make the cost guard unavailable exactly
// when someone is about to spend money.
func Run(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig) (*Plan, *Stats, error) {
	cfg := deps.Config
	slog.InfoContext(ctx, "pipeline starting",
		"datatype", cfg.Name, "concurrency", rc.Concurrency, "dry_run", rc.DryRun)

	if rc.DryRun {
		result, err := planOnly(ctx, deps, arts, rc)
		if err != nil {
			return nil, nil, err
		}
		return &result.plan, nil, nil
	}

	release, err := deps.State.Lock(ctx, cfg.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("acquiring lock for %s: %w", cfg.Name, err)
	}
	defer func() {
		if err := release(); err != nil {
			slog.WarnContext(ctx, "releasing lock", "datatype", cfg.Name, "error", err)
		}
	}()

	result, err := planOnly(ctx, deps, arts, rc)
	if err != nil {
		return nil, nil, err
	}

	runID, resuming, err := resolveRun(ctx, deps, rc, result)
	if err != nil {
		return nil, nil, err
	}
	result.plan.Resuming = resuming
	result.plan.RunID = runID

	stats := &statsCollector{}
	if err := processTombstones(ctx, deps, result.tombstones, stats); err != nil {
		_ = deps.State.FinishRun(ctx, runID, state.RunFailed, nil)
		return nil, nil, err
	}

	final, err := runWorkers(ctx, deps, arts, rc, runID, result, stats)
	if err != nil {
		return &result.plan, nil, err
	}
	return &result.plan, final, nil
}

// planOnly reconciles the artifact run against state, surfaces any signature change, and — in
// plan mode — estimates what the work would cost. Both modes go through it, which is what makes
// `inget plan` and `inget run` agree on the work set by construction rather than by review.
func planOnly(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig) (*reconcileResult, error) {
	result, err := reconcile(ctx, deps, arts, rc)
	if err != nil {
		return nil, err
	}
	if err := surfaceSignatureChanges(ctx, deps, result); err != nil {
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
