// The run: lock, enumerate, fetch what changed, commit.
//
// Enumeration and fetching overlap. Refs are handed to an errgroup bounded by the source's
// concurrency as they arrive, and because a bounded errgroup's Go blocks when it is full, the
// enumerator is throttled by the fetchers rather than racing ahead of them. Nothing accumulates
// except the identifier set tombstones are computed from.
//
// The level-0 skip is the cheapest guard in the system: a repository whose pushed_at matches what
// state already recorded costs one line of a listing response and no further request at all.
// Comparison is for inequality only, so a clock that moved backwards causes a redundant fetch and
// never a missed change.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/source"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// Run executes one fetch and returns what it did. A dry run enumerates, applies the level-0
// guard, and reports; it opens no writer, so it writes no blob and commits no run.
func Run(ctx context.Context, deps Deps, rc RunConfig) (report *Report, err error) {
	if err := rc.normalize(); err != nil {
		return nil, err
	}
	emitProgress(ctx, rc.Datatype, "start", "", "waiting for lock", -1, 0)
	defer func() { finishProgress(ctx, rc.Datatype, err) }()
	release, err := deps.State.Lock(ctx, rc.lockKey())
	if err != nil {
		return nil, fmt.Errorf("locking %s for fetch: %w", rc.Datatype, err)
	}
	defer func() {
		if err := release(); err != nil {
			slog.ErrorContext(ctx, "releasing fetch lock", "datatype", rc.Datatype, "error", err.Error())
		}
	}()

	emitProgress(ctx, rc.Datatype, "stage", "", "loading state", -1, 0)
	prior, err := deps.State.ItemFingerprints(ctx, rc.Datatype)
	if err != nil {
		return nil, err
	}
	r := &runner{deps: deps, cfg: rc, prior: prior, seen: map[string]bool{}}

	if rc.DryRun {
		if err := r.enumerate(ctx); err != nil {
			return nil, err
		}
		return r.report(""), nil
	}
	return r.execute(ctx)
}

// runner holds one run's mutable state. Counters touched by workers are guarded by mu; the
// enumeration counters are not, because yield is called from one goroutine.
type runner struct {
	deps   Deps
	cfg    RunConfig
	prior  map[string]string
	writer *artifact.Writer

	seen       map[string]bool // lowercased IDs enumerated, for tombstones
	enumerated int
	skipped    int
	truncated  bool

	mu           sync.Mutex
	items        int
	failed       int
	fragments    int
	blobsWritten int
	blobsReused  int
	warnings     []string
	warningCount int
}

// execute runs the non-dry path: open a run, fetch concurrently, commit.
func (r *runner) execute(ctx context.Context) (*Report, error) {
	runID, err := artifact.NewRunID()
	if err != nil {
		return nil, err
	}
	if err := r.deps.State.StartRun(ctx, state.Run{
		ID: runID, Binary: r.cfg.Binary, Datatype: r.cfg.Datatype,
		Scope: string(r.cfg.Scope), ConfigHash: r.cfg.ConfigHash,
	}); err != nil {
		return nil, err
	}

	report, err := r.write(ctx, runID)
	if err != nil {
		// The run row records the failure; the artifact run has no _COMMIT and so is
		// invisible to every reader, which is what makes an abandoned fetch inert.
		if finishErr := r.deps.State.FinishRun(ctx, runID, state.RunFailed, r.report(runID)); finishErr != nil {
			slog.ErrorContext(ctx, "recording failed fetch run", "run_id", runID, "error", finishErr.Error())
		}
		return nil, err
	}
	if err := r.deps.State.FinishRun(ctx, runID, state.RunOK, report); err != nil {
		return nil, err
	}
	return report, nil
}

// write opens the artifact writer, fetches every changed item into it, and commits.
func (r *runner) write(ctx context.Context, runID string) (*Report, error) {
	r.progress(ctx, "stage", "", "opening artifact run", -1, 0)
	writer, err := r.deps.Artifacts.NewWriter(ctx, artifact.RunInfo{
		RunID:      runID,
		Source:     r.cfg.Source,
		Datatype:   r.cfg.Datatype,
		Scope:      r.cfg.Scope,
		Producer:   r.cfg.Producer,
		DomainHash: r.cfg.DomainHash,
		ConfigHash: r.cfg.ConfigHash,
	})
	if err != nil {
		return nil, fmt.Errorf("starting artifact run: %w", err)
	}
	r.writer = writer
	// Close is a no-op after a successful commit and abandons the run otherwise.
	defer func() { _ = writer.Close() }()

	if err := r.enumerate(ctx); err != nil {
		return nil, err
	}
	tombstones := r.tombstones()
	report := r.report(runID)
	report.Tombstones = len(tombstones)

	r.progress(ctx, "stage", "", "committing", -1, 0)
	if _, err := writer.Commit(ctx, artifact.CommitInfo{
		ItemsSkippedUnchanged: r.skipped,
		BlobsWritten:          report.BlobsWritten,
		BlobsReused:           report.BlobsReused,
		Tombstones:            tombstones,
		Truncated:             r.truncated,
		Warnings:              report.Warnings,
	}); err != nil {
		return nil, err
	}
	return report, nil
}

// enumerate walks the domain, applying the level-0 guard and dispatching what changed.
//
// The pool's context governs enumeration, so a fatal worker error stops the enumerator. The workers
// themselves are handed the run's context instead, because errgroup cancels its context when Wait
// returns and the artifact writer is still needed after that: a shard bound to a cancelled context
// cannot be flushed, and the commit would fail on a run that had otherwise succeeded.
//
// A worker failure does not cancel anything by itself. fetchItem reports per-item problems through
// the counters and returns nil, so only an infrastructure error reaches the group.
func (r *runner) enumerate(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(r.cfg.Concurrency)

	r.progress(ctx, "stage", "", "listing items", -1, 0)
	query := source.ListQuery{Only: r.cfg.Only, Since: r.cfg.Since, Limit: r.cfg.Limit}
	listErr := r.deps.Connector.List(groupCtx, r.cfg.Datatype, query, func(ref source.Ref) error {
		r.enumerated++
		r.progress(ctx, "enumerated", ref.ID, "", -1, 1)
		r.seen[strings.ToLower(ref.ID)] = true
		if fingerprint, known := r.prior[ref.ID]; known && fingerprint != "" && fingerprint == ref.Fingerprint {
			r.skipped++
			r.progress(ctx, "completed", ref.ID, "skipped", -1, 1)
			return nil
		}
		if r.cfg.DryRun {
			r.mu.Lock()
			r.items++
			r.mu.Unlock()
			r.progress(ctx, "completed", ref.ID, "planned", -1, 1)
			return nil
		}
		group.Go(func() error { return r.fetchItem(ctx, ref) })
		return nil
	})

	// A limit reached is a successful stop, but it also means enumeration did not see the
	// whole domain, which degrades a full run to partial semantics for deletions.
	if errors.Is(listErr, source.ErrStopList) {
		r.truncated = true
		listErr = nil
	}
	r.progress(ctx, "listed", "", "", r.enumerated, 0)
	if !r.cfg.DryRun {
		r.progress(ctx, "stage", "", "fetching items", -1, 0)
	}
	if waitErr := group.Wait(); waitErr != nil {
		return fmt.Errorf("fetching %s items: %w", r.cfg.Datatype, waitErr)
	}
	if listErr != nil {
		return fmt.Errorf("enumerating %s: %w", r.cfg.Datatype, listErr)
	}
	return nil
}

// tombstones are the live items state knows about that the source no longer has. They are only
// computed for a full, untruncated run: an enumeration that stopped early cannot tell "absent"
// from "not reached", and a partial run was never looking.
func (r *runner) tombstones() []string {
	if r.cfg.Scope != artifact.ScopeFull || r.truncated {
		return nil
	}
	var out []string
	for id := range r.prior {
		if !r.seen[strings.ToLower(id)] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
