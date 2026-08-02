// Scope resolution for `inget reindex`, and the opt-in prune that reaches past it.
//
// Everything here answers one question: what is this pass allowed to touch. A --view the datatype
// does not declare, a --destination it does not write to, and a prune of rows no configured
// datatype claims are all cases where the wrong answer is silent — a pass that rewrites nothing, or
// a delete of rows somebody still wants.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/model"
	"github.com/maxwellcudlitz/inget/internal/reindex"
)

// checkViews rejects a --view name the datatype does not declare, which is otherwise a silent
// no-op pass.
func checkViews(dt config.Datatype, views []string) error {
	for _, name := range views {
		if !slices.Contains(viewNames(dt), name) {
			return fmt.Errorf("datatype %s has no view %q (declared: %v)", dt.Name, name, viewNames(dt))
		}
	}
	return nil
}

// scopedDestinations intersects the datatype's destinations with the --destination filter.
func scopedDestinations(dt config.Datatype, filter []string) ([]string, error) {
	if len(filter) == 0 {
		return dt.Destinations, nil
	}
	var names []string
	for _, name := range dt.Destinations {
		if slices.Contains(filter, name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("datatype %s writes to %v, none of which is in %v",
			dt.Name, dt.Destinations, filter)
	}
	return names, nil
}

// injectsMetadata reports whether the datatype resolves references into metadata, whose values
// state does not persist and a rewritten row therefore cannot carry.
func injectsMetadata(dt config.Datatype) bool {
	for _, ref := range dt.References {
		if ref.InjectAs == "metadata" {
			return true
		}
	}
	return false
}

// pruneUnconfigured deletes rows belonging to datatypes this configuration does not declare.
//
// It is opt-in because a destination table may legitimately hold a datatype that was removed from
// configuration but not from the index, and because the predicate is "not written by the current
// embedder" rather than "not wanted". It runs only after a complete pass: every configured
// datatype, every view, nothing interrupted, nothing failed.
func pruneUnconfigured(ctx context.Context, cfg *config.Config, emb model.Embedder,
	datatypes []config.Datatype, opts reindexOptions, reports []reindex.Report) error {
	if !opts.pruneUnknown {
		return nil
	}
	if len(opts.views) > 0 || len(opts.destinations) > 0 || len(datatypes) != len(cfg.Datatypes) {
		return fmt.Errorf("--prune-unconfigured needs a complete pass: every datatype, every view, " +
			"every destination")
	}
	for _, r := range reports {
		if r.Interrupted || r.Failed > 0 {
			return fmt.Errorf("--prune-unconfigured refused: the %s pass was interrupted or had "+
				"%d failed items", r.Datatype, r.Failed)
		}
	}

	for _, name := range destinationNames(datatypes) {
		sink, err := openDestination(ctx, cfg, name, emb)
		if err != nil {
			return err
		}
		deleted, err := sink.PruneStaleVectors(ctx, "")
		_ = sink.Close()
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "pruned rows of unconfigured datatypes",
			"destination", name, "deleted", deleted)
	}
	return nil
}

// destinationNames is every destination the given datatypes write to, once each.
func destinationNames(datatypes []config.Datatype) []string {
	var names []string
	for _, dt := range datatypes {
		for _, name := range dt.Destinations {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// reportReindex writes the reports as JSON on stdout and one summary line each to the log.
func reportReindex(reports []reindex.Report) error {
	for _, r := range reports {
		slog.Info("reindex complete",
			"datatype", r.Datatype, "run_id", r.RunID, "embedder", r.Embedder, "dims", r.Dims,
			"rebound", r.Rebound, "items", r.Items, "unchanged", r.Unchanged, "failed", r.Failed,
			"vectors", r.Vectors, "pruned", r.Pruned,
			"resuming", r.Resuming, "interrupted", r.Interrupted)
	}
	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling reindex reports: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
