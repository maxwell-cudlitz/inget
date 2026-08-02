// Reconciliation: scan an artifact run against persisted state to compute the work set.
//
// This is the first phase of both `inget run` and `inget plan`: open the artifact, walk its
// records, compare each item's fingerprint against state, and partition them into added,
// modified, unchanged and deleted.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/delta"
)

// reconcileResult holds the computed delta from artifact scanning.
type reconcileResult struct {
	plan       Plan
	tombstones []string
	partial    bool                        // the run saw a subset of items, so absence proves nothing
	records    map[string]*artifact.Record // itemID → record for processing
}

// reconcile opens the latest artifact run and computes what changed against state.
func reconcile(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig) (*reconcileResult, error) {
	cfg := deps.Config
	run, err := arts.OpenRun(ctx, cfg.Source, cfg.Name, artifact.LatestRun)
	if err != nil {
		return nil, fmt.Errorf("opening artifact run for %s: %w", cfg.Name, err)
	}
	slog.InfoContext(ctx, "opened artifact run",
		"datatype", cfg.Name,
		"run_id", run.Manifest.RunID,
		"scope", run.Manifest.Scope,
		"items", run.Manifest.Counts.Items)

	cached, err := deps.State.ItemFingerprints(ctx, cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("loading item fingerprints for %s: %w", cfg.Name, err)
	}

	incoming := make([]delta.IncomingFragment, 0, run.Manifest.Counts.Items)
	records := make(map[string]*artifact.Record, run.Manifest.Counts.Items)
	err = run.Records(ctx, func(rec *artifact.Record) error {
		incoming = append(incoming, delta.IncomingFragment{
			Key:         rec.ItemID,
			Fingerprint: rec.Fingerprint,
		})
		records[rec.ItemID] = rec
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading artifact records for %s: %w", cfg.Name, err)
	}

	d := delta.Reconcile(cached, incoming)
	slog.InfoContext(ctx, "reconciled items",
		"datatype", cfg.Name,
		"added", len(d.Added),
		"modified", len(d.Modified),
		"unchanged", len(d.Unchanged),
		"deleted", len(d.Deleted))

	candidates := make([]string, 0, len(d.Added)+len(d.Modified))
	candidates = append(candidates, d.Added...)
	candidates = append(candidates, d.Modified...)
	workItems, partial := selectWork(candidates, rc)

	// Tombstones need to have seen everything. A full-scope fetch establishes that upstream;
	// --only or --limit takes it away again, because an item this run never looked at is not
	// an item that was deleted (D6).
	var tombstones []string
	if run.Manifest.ProcessTombstones() && !partial {
		tombstones = d.Deleted
	}

	return &reconcileResult{
		plan: Plan{
			Datatype:      cfg.Name,
			ArtifactRunID: run.Manifest.RunID,
			TotalItems:    len(incoming),
			Added:         len(d.Added),
			Modified:      len(d.Modified),
			Deleted:       len(d.Deleted),
			Unchanged:     len(d.Unchanged),
			WorkItems:     workItems,
			Tombstones:    tombstones,
		},
		tombstones: tombstones,
		partial:    partial,
		records:    records,
	}, nil
}

// selectWork applies --only and --limit, reporting whether the result is a subset of what
// changed. The candidates are sorted first so that --limit takes the same items every time:
// a limited run that picked a different arbitrary subset on each invocation would make no
// progress through a backlog.
func selectWork(candidates []string, rc RunConfig) (work []string, partial bool) {
	slices.Sort(candidates)

	if len(rc.Only) > 0 {
		only := make(map[string]struct{}, len(rc.Only))
		for _, id := range rc.Only {
			only[id] = struct{}{}
		}
		filtered := make([]string, 0, len(only))
		for _, id := range candidates {
			if _, ok := only[id]; ok {
				filtered = append(filtered, id)
			}
		}
		candidates, partial = filtered, true
	}
	if rc.Limit > 0 && len(candidates) > rc.Limit {
		candidates, partial = candidates[:rc.Limit], true
	}
	return candidates, partial
}
