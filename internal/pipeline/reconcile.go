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

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/enrich/refs"
)

// reconcileResult holds the computed delta from artifact scanning.
type reconcileResult struct {
	plan       Plan
	tombstones []string
	partial    bool                        // the run saw a subset of items, so absence proves nothing
	records    map[string]*artifact.Record // itemID → record for processing
	// changed is the items whose own content moved, as opposed to the ones a reference
	// invalidated. Only the former cascade unconditionally; see processItem.
	changed map[string]bool
}

// reconcile opens the given artifact run and computes what changed against state.
func reconcile(ctx context.Context, deps Deps, arts *artifact.Store, rc RunConfig, artifactRunID string) (*reconcileResult, error) {
	cfg := deps.Config
	run, err := arts.OpenRun(ctx, cfg.Source, cfg.Name, artifactRunID)
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
	candidates := make([]string, 0, len(d.Added)+len(d.Modified))
	candidates = append(candidates, d.Added...)
	candidates = append(candidates, d.Modified...)
	changed := make(map[string]bool, len(candidates))
	for _, id := range candidates {
		changed[id] = true
	}

	invalidated, deferred, err := invalidatedItems(ctx, deps, rc, records, changed)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, invalidated...)
	workItems, partial := selectWork(candidates, rc)

	// Only the producer knows which items it enumerated. Records omit unchanged items and
	// failed downloads, so their absence can never establish deletion. Full, untruncated
	// enumeration permits the manifest's explicit tombstones; restricted consumption does not.
	var tombstones []string
	if run.Manifest.ProcessTombstones() && !partial {
		tombstones, err = explicitTombstones(run.Manifest.Tombstones, records)
		if err != nil {
			return nil, fmt.Errorf("reading tombstones for %s: %w", cfg.Name, err)
		}
	}
	slog.InfoContext(ctx, "reconciled items",
		"datatype", cfg.Name,
		"added", len(d.Added),
		"modified", len(d.Modified),
		"unchanged", len(d.Unchanged),
		"deleted", len(tombstones))

	return &reconcileResult{
		plan: Plan{
			Datatype:      cfg.Name,
			ArtifactRunID: run.Manifest.RunID,
			TotalItems:    len(incoming),
			Added:         len(d.Added),
			Modified:      len(d.Modified),
			Deleted:       len(tombstones),
			Unchanged:     len(d.Unchanged),
			Invalidated:   len(invalidated),
			Deferred:      deferred,
			WorkItems:     workItems,
			Tombstones:    tombstones,
			Restricted:    partial,
		},
		tombstones: tombstones,
		partial:    partial,
		records:    records,
		changed:    changed,
	}, nil
}

// explicitTombstones validates the producer's deletion claims and makes their application
// deterministic. A record and tombstone for one ID contradict each other; refuse the run
// before deleting state or spending model calls rather than choosing either claim.
func explicitTombstones(ids []string, records map[string]*artifact.Record) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("manifest contains an empty tombstone ID")
		}
		if _, present := records[id]; present {
			return nil, fmt.Errorf("manifest tombstone %q also has an item record", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// invalidatedItems is the pull half of the reference cascade (D12): the items of this datatype
// carrying a staleness mark, capped at enrich.max_cascade_per_run.
//
// An item already in the work set is not counted twice, and an item absent from the artifact
// run is skipped rather than enqueued: a marked item that has since been deleted upstream would
// otherwise fail every run forever, since the claim loop cannot process what it cannot read.
// Its mark is collected with the rest of its state by `inget state gc`.
func invalidatedItems(ctx context.Context, deps Deps, rc RunConfig, records map[string]*artifact.Record, changed map[string]bool) ([]string, int, error) {
	stale, err := deps.State.StaleReferrers(ctx, deps.Config.Name)
	if err != nil {
		return nil, 0, fmt.Errorf("reading invalidated %s items: %w", deps.Config.Name, err)
	}
	if len(stale) == 0 {
		return nil, 0, nil
	}

	eligible := make([]string, 0, len(stale))
	for _, s := range stale {
		if changed[s.ItemID] {
			continue
		}
		if _, ok := records[s.ItemID]; !ok {
			slog.DebugContext(ctx, "invalidated item is not in the artifact run, skipping",
				"datatype", deps.Config.Name, "item_id", s.ItemID, "depth", s.Depth)
			continue
		}
		eligible = append(eligible, s.ItemID)
	}
	taken := refs.WithinCap(ctx, deps.Config.Name, eligible, rc.MaxCascadePerRun)
	if len(taken) > 0 {
		slog.InfoContext(ctx, "reference invalidations entering the work set",
			"datatype", deps.Config.Name, "invalidated", len(taken), "deferred", len(eligible)-len(taken))
	}
	return taken, len(eligible) - len(taken), nil
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
