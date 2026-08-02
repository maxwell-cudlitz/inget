// `inget state gc` and `inget state unlock`: the two state commands that change something.
//
// They are separated from `show` because that is the line worth seeing in the file listing: one
// file reads, this one deletes rows and objects and removes locks.
package main

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/gc"
)

// stateGCCommand builds `inget state gc`.
func stateGCCommand() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Reclaim retired runs, unreferenced blobs and cold derivations",
		Long: "gc performs the three-phase collection of docs/artifact-envelope.md: run directories\n" +
			"older than retention.runs except the latest committed one, blobs no retained run and no\n" +
			"live fragment references, then the derivations of fragments missing for more than\n" +
			"retention.missing_runs runs. It takes every datatype's locks for the duration, so a run\n" +
			"or fetch in progress makes it exit rather than race. Blobs are content-addressed and\n" +
			"re-fetchable; --dry-run reports what would go without deleting anything.",
		// Deliberately no datatype argument: whether a blob is referenced is a claim about every
		// datatype at once, so collecting one datatype's runs would delete blobs another still
		// points at.
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStateGC(cmd, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be collected without deleting it")
	return cmd
}

// runStateGC collects across every configured datatype.
func runStateGC(cmd *cobra.Command, dryRun bool) error {
	cfg, store, datatypes, err := stateSetup(cmd, nil)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	arts, err := artifact.Open(cmd.Context(), artifact.FromConfig(cfg.Artifacts))
	if err != nil {
		return fmt.Errorf("opening artifact store: %w", err)
	}
	defer func() { _ = arts.Close() }()

	scope := make([]gc.Datatype, 0, len(datatypes))
	for _, dt := range datatypes {
		scope = append(scope, gc.Datatype{Source: dt.Source, Name: dt.Name})
	}

	report, err := gc.Collect(cmd.Context(), arts, store, gc.Options{
		Datatypes:    scope,
		RunRetention: cfg.Retention.Runs.Duration(),
		MissingRuns:  cfg.Retention.MissingRuns,
		DryRun:       dryRun,
	})
	if err != nil {
		return err
	}
	slog.InfoContext(cmd.Context(), "collection complete",
		"dry_run", report.DryRun, "runs_deleted", report.RunsDeleted,
		"runs_retained", report.RunsRetained, "objects_deleted", report.ObjectsDeleted,
		"blobs_deleted", report.BlobsDeleted, "blobs_retained", report.BlobsRetained,
		"derivations_deleted", report.DerivationsDeleted)
	return printJSON(report)
}

// stateUnlockCommand builds `inget state unlock`.
func stateUnlockCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unlock <datatype>",
		Short: "Remove a lock whose holder is gone",
		Long: "unlock removes the pipeline and fetch locks of one datatype. It only ever has work to\n" +
			"do on the sqlite driver, where a killed process leaves its mutex row behind; a postgres\n" +
			"advisory lock is released by the server when the holder's connection drops, so a lock\n" +
			"that is still held belongs to a live process and unlock reports that instead of\n" +
			"stealing it.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStateUnlock(cmd, args[0])
		},
	}
	return cmd
}

// unlockReport is what unlock did to one lock key.
type unlockReport struct {
	Key     string `json:"key"`
	Removed bool   `json:"removed"`
}

// runStateUnlock releases both locks of one datatype.
func runStateUnlock(cmd *cobra.Command, datatype string) error {
	_, store, datatypes, err := stateSetup(cmd, []string{datatype})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	name := datatypes[0].Name
	reports := make([]unlockReport, 0, 2)
	for _, key := range []string{name, "fetch:" + name} {
		removed, err := store.ForceUnlock(cmd.Context(), key)
		if err != nil {
			return err
		}
		slog.InfoContext(cmd.Context(), "unlock", "key", key, "removed", removed)
		reports = append(reports, unlockReport{Key: key, Removed: removed})
	}
	return printJSON(reports)
}
