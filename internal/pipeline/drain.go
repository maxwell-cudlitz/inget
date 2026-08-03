// The artifact backlog: which committed runs a pipeline invocation still owes work to.
//
// Reading only the newest run is correct when one producer enumerates the whole domain on a
// schedule, and lossy when many producers each commit one item — the topology a CI job
// submitting its own repository creates. Absence from a partial run means nothing, so those
// submissions were never deleted; they were simply never read. So the consumer keeps a
// high-water mark and drains everything past it, oldest first, which is also the order in
// which the records were true.
//
// The mark advances conservatively, because advancing it wrongly loses work silently while
// failing to advance it only costs a reconcile the next invocation. Three conditions must all
// hold: the pass completed without a failed item, it was not restricted by --only or --limit,
// and no earlier run in the same drain failed either of those tests. The last one is what keeps
// the mark meaning "everything up to here is done" rather than "the newest thing I touched".
package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
)

// pendingRuns returns the artifact runs this invocation should consume, oldest first.
//
// An unrecorded mark yields the latest run alone. That is what a store upgraded to this
// feature looks like, and draining a month of retained runs on the strength of a missing row
// would be a surprising way to spend a budget.
//
// An empty backlog yields the latest run too, rather than nothing. Re-running with no new
// fetch has always been a cheap no-op that retries whatever failed last time, and that
// property is worth keeping: reconciling an unchanged run costs one read and no tokens.
func pendingRuns(ctx context.Context, deps Deps, arts *artifact.Store) ([]string, error) {
	cfg := deps.Config
	consumed, err := deps.State.ConsumedArtifactRun(ctx, cfg.Name)
	if err != nil {
		return nil, err
	}
	if consumed == "" {
		latest, err := arts.LatestRun(ctx, cfg.Source, cfg.Name)
		if err != nil {
			return nil, fmt.Errorf("resolving the latest artifact run for %s: %w", cfg.Name, err)
		}
		return []string{latest}, nil
	}

	committed, err := arts.ListRuns(ctx, cfg.Source, cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("listing artifact runs for %s: %w", cfg.Name, err)
	}
	// Run identifiers are ULIDs, so "newer than the mark" is a string comparison.
	pending := make([]string, 0, len(committed))
	for _, runID := range committed {
		if runID > consumed {
			pending = append(pending, runID)
		}
	}
	if len(pending) > 0 {
		slog.InfoContext(ctx, "artifact backlog",
			"datatype", cfg.Name, "consumed_through", consumed, "pending_runs", len(pending))
		return pending, nil
	}

	latest, err := arts.LatestRun(ctx, cfg.Source, cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("resolving the latest artifact run for %s: %w", cfg.Name, err)
	}
	return []string{latest}, nil
}

// add accumulates another pass's counters. A drain reports one total rather than one line per
// artifact run, because the operator asked what the invocation did.
func (s *Stats) add(other *Stats) {
	if other == nil {
		return
	}
	s.ItemsProcessed += other.ItemsProcessed
	s.ItemsFailed += other.ItemsFailed
	s.FragmentsEnrich += other.FragmentsEnrich
	s.ViewsGenerated += other.ViewsGenerated
	s.ViewsSkipped += other.ViewsSkipped
	s.EmbeddingsStored += other.EmbeddingsStored
	s.TombstonesApplied += other.TombstonesApplied
	s.ReferencesResolved += other.ReferencesResolved
}
