// The three phases themselves.
//
// Each phase logs what it did rather than returning a description of it: the counts go on the
// Report for a caller to print, and the per-object detail goes to the log at debug level, which
// is where "why did that blob disappear" gets answered.
package gc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
)

// collectRuns is phase 1: delete run directories older than the retention window, except the
// latest committed run of each source and datatype.
//
// That exception is what makes the window safe to shorten. The latest committed run is the one
// `inget run` reads, so collecting it because it happened to be old would leave a datatype with
// no artifacts at all until the next fetch — which for a source whose data has not changed may
// be a long time.
func collectRuns(ctx context.Context, arts *artifact.Store, o Options, report *Report) ([]retained, error) {
	cutoff := o.Now.Add(-o.RunRetention)
	var kept []retained

	for _, dt := range o.Datatypes {
		dirs, err := arts.RunDirs(ctx, dt.Source, dt.Name)
		if err != nil {
			return nil, err
		}
		newest := newestCommitted(dirs)

		for _, dir := range dirs {
			if dir.RunID == newest || !dir.CreatedAt.Before(cutoff) {
				kept = append(kept, retained{Datatype: dt, RunID: dir.RunID, Committed: dir.Committed})
				report.RunsRetained++
				continue
			}
			slog.InfoContext(ctx, "collecting run", "source", dt.Source, "datatype", dt.Name,
				"run_id", dir.RunID, "created_at", dir.CreatedAt, "committed", dir.Committed,
				"dry_run", o.DryRun)
			report.RunsDeleted++
			if o.DryRun {
				continue
			}
			objects, err := arts.DeleteRun(ctx, dt.Source, dt.Name, dir.RunID)
			if err != nil {
				return nil, err
			}
			report.ObjectsDeleted += objects
		}
	}
	return kept, nil
}

// newestCommitted returns the latest committed run identifier, or "" when there is none. Run
// IDs are ULIDs, so the last committed entry of a chronologically ordered listing is it.
func newestCommitted(dirs []artifact.RunDir) string {
	for i := len(dirs) - 1; i >= 0; i-- {
		if dirs[i].Committed {
			return dirs[i].RunID
		}
	}
	return ""
}

// collectBlobs is phase 2: delete blobs no retained run and no live fragment row references.
//
// The retention set is read from the retained runs' manifests and from state, in that order,
// and only then is the blob namespace walked. An uncommitted retained directory contributes
// nothing, because its records cannot be read — the blobs a dead fetch left behind are
// therefore collected, which is correct and recoverable: content addressing means the next
// fetch re-uploads exactly what it needs.
func collectBlobs(ctx context.Context, arts *artifact.Store, st State, o Options,
	kept []retained, report *Report) error {
	live, err := st.LiveBlobRefs(ctx)
	if err != nil {
		return err
	}
	for _, r := range kept {
		if !r.Committed {
			continue
		}
		run, err := arts.OpenRun(ctx, r.Datatype.Source, r.Datatype.Name, r.RunID)
		if err != nil {
			return fmt.Errorf("reading retained run %s: %w", r.RunID, err)
		}
		refs, err := run.BlobRefs(ctx)
		if err != nil {
			return err
		}
		for digest := range refs {
			live[digest] = struct{}{}
		}
	}

	return arts.Blobs(ctx, func(digest string) error {
		if _, ok := live[digest]; ok {
			report.BlobsRetained++
			return nil
		}
		slog.DebugContext(ctx, "collecting blob", "digest", digest, "dry_run", o.DryRun)
		report.BlobsDeleted++
		if o.DryRun {
			return nil
		}
		return arts.DeleteBlob(ctx, digest)
	})
}

// collectDerivations is phase 3: drop the cached derivations of fragments that have been absent
// for longer than retention.missing_runs.
//
// A dry run reports the window rather than a count. Counting would mean running the delete and
// rolling it back, and a phase that reports nothing it did not do is worth more here than an
// exact figure for the one phase whose input is a single number.
func collectDerivations(ctx context.Context, st State, o Options, report *Report) error {
	if o.DryRun {
		slog.InfoContext(ctx, "skipping derivation collection in a dry run",
			"missing_runs", o.MissingRuns)
		return nil
	}
	deleted, err := st.CollectDerivations(ctx, o.MissingRuns)
	if err != nil {
		return err
	}
	report.DerivationsDeleted = deleted
	slog.InfoContext(ctx, "collected derivations", "deleted", deleted, "missing_runs", o.MissingRuns)
	return nil
}
