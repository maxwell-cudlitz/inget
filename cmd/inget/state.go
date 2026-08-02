// The `inget state` command group: inspect what is persisted, collect what is not needed, and
// remove a lock whose holder is gone.
//
// All three are operator tools rather than pipeline stages, which is why they share a parent
// command and a file. Output is JSON on stdout so `inget state show | jq` works; diagnostics go to
// the log on stderr.
package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// defaultRunHistory is how many runs `state show` reports per datatype.
const defaultRunHistory = 5

// stateCommand builds the `inget state` group.
func stateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Inspect and maintain persisted state",
		Long: "state groups the operator commands over the state store and the artifact store:\n" +
			"show reports what each cascade level holds, gc reclaims retired runs, unreferenced\n" +
			"blobs and cold derivations, and unlock removes a lock no live process holds.",
	}
	cmd.AddCommand(stateShowCommand(), stateGCCommand(), stateUnlockCommand())
	return cmd
}

// stateReport is one datatype's persisted state.
type stateReport struct {
	Datatype         string            `json:"datatype"`
	Source           string            `json:"source"`
	LatestArtifactID string            `json:"latest_artifact_run,omitempty"`
	Counts           state.Counts      `json:"counts"`
	Runs             []state.RunRecord `json:"recent_runs"`
}

// stateShowCommand builds `inget state show`.
func stateShowCommand() *cobra.Command {
	var history int

	cmd := &cobra.Command{
		Use:   "show [datatype]",
		Short: "Report what each cascade level holds",
		Long: "show reports per datatype how many items, fragments, derivations, views, vectors and\n" +
			"reference edges are persisted, the newest committed artifact run, and how the recent\n" +
			"runs ended. It writes nothing.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStateShow(cmd, args, history)
		},
	}
	cmd.Flags().IntVar(&history, "runs", defaultRunHistory, "how many recent runs to report per datatype")
	return cmd
}

// runStateShow reads the counts and run history of every requested datatype.
func runStateShow(cmd *cobra.Command, args []string, history int) error {
	cfg, store, datatypes, err := stateSetup(cmd, args)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// The artifact store is opened for one field per datatype, so an unreachable bucket degrades
	// this command rather than failing it: "what has state processed" is still answerable, and a
	// blob store that cannot be listed is itself worth reporting.
	arts, err := artifact.Open(cmd.Context(), artifact.FromConfig(cfg.Artifacts))
	if err != nil {
		slog.WarnContext(cmd.Context(), "artifact store is unreachable; reporting state only",
			"url", cfg.Artifacts.URL, "error", err.Error())
		arts = nil
	} else {
		defer func() { _ = arts.Close() }()
	}

	reports := make([]stateReport, 0, len(datatypes))
	for _, dt := range datatypes {
		counts, err := store.Counts(cmd.Context(), dt.Name)
		if err != nil {
			return err
		}
		runs, err := store.RecentRuns(cmd.Context(), dt.Name, history)
		if err != nil {
			return err
		}
		report := stateReport{Datatype: dt.Name, Source: dt.Source, Counts: counts, Runs: runs}
		report.LatestArtifactID = latestArtifact(cmd.Context(), arts, dt)
		reports = append(reports, report)
		slog.InfoContext(cmd.Context(), "state", "datatype", dt.Name,
			"items", counts.Items, "tombstoned", counts.Tombstoned, "fragments", counts.Fragments,
			"derivations", counts.Derivations, "views", counts.Views,
			"embedded_views", counts.EmbeddedViews, "stale_refs", counts.StaleRefs,
			"latest_artifact_run", report.LatestArtifactID)
	}
	return printJSON(reports)
}

// latestArtifact returns the newest committed run for a datatype, or "" when there is none or the
// store could not answer. A datatype that has never been fetched is a normal state, not an error,
// and a store that cannot be listed has already been reported once by the caller.
func latestArtifact(ctx context.Context, arts *artifact.Store, dt config.Datatype) string {
	if arts == nil {
		return ""
	}
	latest, err := arts.LatestRun(ctx, dt.Source, dt.Name)
	switch {
	case err == nil:
		return latest
	case errors.Is(err, artifact.ErrNotFound):
		return ""
	default:
		slog.WarnContext(ctx, "reading the latest artifact run",
			"datatype", dt.Name, "source", dt.Source, "error", err.Error())
		return ""
	}
}
