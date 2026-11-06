// The worker pool: claiming, dispatch, graceful drain, and run closeout.
//
// Two rules shape this file. A single item's failure is that item's problem, so it is
// recorded and the pool continues; anything that stops the pool from making progress —
// a claim that cannot be read, a cancelled context — fails the run loudly. And the
// shutdown channel comes from the context the caller wired to the signal handler, never
// from a channel created here.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// runWorkers processes the run's work queue and closes out the run.
func runWorkers(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig, runID string, result *reconcileResult, stats *statsCollector) (*Stats, error) {
	cfg := deps.Config
	ex := &execution{
		deps:                     deps,
		arts:                     arts,
		runID:                    runID,
		gen:                      newLimiter(rc.Concurrency),
		derive:                   &singleflight.Group{},
		concurrency:              max(rc.Concurrency, 1),
		forceViews:               result.rebuild,
		cacheOnly:                rc.CachedFragmentsOnly,
		cachedFragmentSignatures: rc.CachedFragmentSignatures,
		refDepth:                 rc.MaxReferenceDepth,
		changed:                  result.changed,
		stats:                    stats,
	}
	if cfg.FragmentEnricher && deps.FragEnricher != nil {
		ex.fragSig = deps.FragEnricher.Signature()
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(ex.concurrency)

	claimErr := claimLoop(gctx, ex, result, g)
	poolErr := g.Wait()

	if err := errors.Join(claimErr, poolErr); err != nil {
		final := stats.snapshot()
		_ = deps.State.FinishRun(ctx, runID, state.RunFailed, final)
		return nil, fmt.Errorf("run %s of %s: %w", runID, cfg.Name, err)
	}

	final := stats.snapshot()
	status := state.RunOK
	if ShuttingDown(ctx) {
		// Partial failures leave the run ok: a failed item wrote no guards, so the next run
		// reconciles it as changed and retries it. An interrupted drain is different — work
		// is still queued, and this status is what tells the next run to adopt that queue
		// instead of computing a new one.
		status = state.RunInterrupted
	}
	if err := deps.State.FinishRun(ctx, runID, status, final); err != nil {
		return nil, fmt.Errorf("finishing run %s: %w", runID, err)
	}
	if len(result.plan.Estimate.ChangedSignatures) > 0 &&
		(!result.rebuild || result.partial || final.ItemsFailed > 0 || ShuttingDown(ctx)) {
		slog.InfoContext(ctx, "leaving changed enricher signatures unrecorded until a full rebuild succeeds",
			"datatype", cfg.Name, "rebuild_enabled", result.rebuild, "restricted", result.partial,
			"failed_items", final.ItemsFailed)
	} else {
		recordSignatures(ctx, deps, len(rc.CachedFragmentSignatures) > 0)
	}
	slog.InfoContext(ctx, "pipeline complete",
		"datatype", cfg.Name, "status", status,
		"processed", final.ItemsProcessed, "failed", final.ItemsFailed,
		"views_generated", final.ViewsGenerated, "embeddings", final.EmbeddingsStored)
	return &final, nil
}

// claimLoop claims one item at a time and dispatches it to the pool, stopping on shutdown,
// context cancellation, an exhausted queue, or a claim failure.
//
// g.Go blocks once the pool is full, so claiming one item per iteration is backpressure
// rather than a throughput limit. Batching would claim rows this process cannot start yet,
// and a claimed row is a row no other process may take.
func claimLoop(ctx context.Context, ex *execution, result *reconcileResult, g *errgroup.Group) error {
	cfg := ex.deps.Config
	for {
		if ShuttingDown(ctx) {
			slog.InfoContext(ctx, "shutdown signalled, draining in-flight items",
				"datatype", cfg.Name, "run_id", ex.runID)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("claiming %s work: %w", cfg.Name, err)
		}

		claimed, err := ex.deps.State.ClaimWork(ctx, ex.runID, cfg.Name, 1)
		if err != nil {
			return fmt.Errorf("claiming %s work: %w", cfg.Name, err)
		}
		if len(claimed) == 0 {
			return nil // queue exhausted
		}

		for _, itemID := range claimed {
			rec, ok := result.records[itemID]
			if !ok {
				// Enqueued but absent from the artifact run being read, which happens when a
				// resumed run adopts a queue computed against an older fetch. Fail the item
				// rather than let it block the queue.
				completeFailed(ctx, ex, itemID,
					fmt.Errorf("item is not in artifact run %s", result.plan.ArtifactRunID))
				continue
			}
			g.Go(func() error {
				if err := processItem(ctx, ex, rec); err != nil {
					slog.ErrorContext(ctx, "item processing failed",
						"datatype", cfg.Name, "item_id", itemID, "error", err)
					completeFailed(ctx, ex, itemID, err)
				}
				return nil // a single item's failure does not abort the pool
			})
		}
	}
}

// completeFailed records an item as failed. Failing to record that is worth a log and
// nothing more: the item wrote no guards either, so the next run retries it.
func completeFailed(ctx context.Context, ex *execution, itemID string, cause error) {
	ex.stats.addFailed(1)
	ingestProgress(ctx, ex.deps.Config.Name, "completed", itemID, "failed", 0, 1)
	if err := ex.deps.State.CompleteWork(ctx, ex.runID, ex.deps.Config.Name, itemID, cause); err != nil {
		slog.WarnContext(ctx, "recording item failure",
			"datatype", ex.deps.Config.Name, "item_id", itemID, "error", err)
	}
}

// recordSignatures stores the signature of every enricher scope the run used, so the next
// `inget plan` can report a prompt or model change before it is paid for (D2).
func recordSignatures(ctx context.Context, deps Deps, historicalFragments bool) {
	for scope, sig := range currentSignatures(deps) {
		if historicalFragments && scope == deps.Config.Name+":fragment" {
			continue // historical cache reuse does not attest to producing current-signature summaries
		}
		if err := deps.State.PutSignature(ctx, scope, sig); err != nil {
			slog.WarnContext(ctx, "recording enricher signature", "scope", scope, "error", err)
		}
	}
}
