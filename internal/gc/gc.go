// Package gc implements the three-phase collection behind `inget state gc`, exactly in the
// order docs/artifact-envelope.md specifies: retired run directories, then unreferenced
// blobs, then the derivations of long-missing fragments.
//
// The order is the correctness argument. A blob is collectable only when no retained run and
// no live fragment row references it, so runs must go first — computing the predicate against
// directories that are about to disappear would keep their blobs alive for one more cycle.
// Derivations go last because they are the one thing here that costs money to rebuild, and a
// failure in either earlier phase should stop before reaching them.
//
// Collection holds both per-datatype locks for its whole duration: the pipeline's lock and the
// fetch lock. A blob is content-addressed and shared, so "unreferenced" is a global claim, and
// a fetch that has uploaded blobs but not yet committed its manifest would otherwise have them
// collected out from under it. The cost is that gc and a run cannot overlap, which is the same
// constraint every other writer here already accepts.
package gc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/maxwellcudlitz/inget/internal/artifact"
)

// State is the slice of state.Store this package needs, declared by the consumer so a test can
// satisfy it and so collection cannot reach anything else.
type State interface {
	Lock(ctx context.Context, key string) (release func() error, err error)
	LiveBlobRefs(ctx context.Context) (map[string]struct{}, error)
	CollectDerivations(ctx context.Context, missingRuns int) (int, error)
}

// Datatype is one configured datatype's identity in the artifact store: the source that
// fetched it and its own name.
type Datatype struct {
	Source string
	Name   string
}

// Options configures one collection. RunRetention and MissingRuns come from the retention
// config block; Now is injectable so a test can age a run without waiting for it.
type Options struct {
	Datatypes    []Datatype
	RunRetention time.Duration
	MissingRuns  int
	Now          time.Time
	DryRun       bool
}

// normalize fills in defaults and rejects what cannot work.
func (o Options) normalize() (Options, error) {
	if len(o.Datatypes) == 0 {
		return o, errors.New("collection needs at least one datatype")
	}
	if o.RunRetention <= 0 {
		return o, fmt.Errorf("retention.runs is %v, want a positive window", o.RunRetention)
	}
	if o.MissingRuns < 1 {
		return o, fmt.Errorf("retention.missing_runs is %d, want 1 or more", o.MissingRuns)
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	return o, nil
}

// Report is what one collection did, or in dry-run mode what it would have done.
type Report struct {
	DryRun             bool `json:"dry_run"`
	RunsDeleted        int  `json:"runs_deleted"`
	RunsRetained       int  `json:"runs_retained"`
	ObjectsDeleted     int  `json:"objects_deleted"`
	BlobsDeleted       int  `json:"blobs_deleted"`
	BlobsRetained      int  `json:"blobs_retained"`
	DerivationsDeleted int  `json:"derivations_deleted"`
}

// retained is one run directory the first phase kept, and therefore one whose blobs the second
// phase must treat as live. Committed says whether its records can be read at all.
type retained struct {
	Datatype  Datatype
	RunID     string
	Committed bool
}

// Collect runs the three phases against one artifact store and one state store.
//
// A dry run reads everything and deletes nothing, including in state: it is what an operator
// runs before letting a CronJob do this unattended.
func Collect(ctx context.Context, arts *artifact.Store, st State, o Options) (Report, error) {
	o, err := o.normalize()
	if err != nil {
		return Report{}, err
	}
	report := Report{DryRun: o.DryRun}

	unlock, err := lockAll(ctx, st, o.Datatypes)
	if err != nil {
		return report, err
	}
	defer unlock()

	kept, err := collectRuns(ctx, arts, o, &report)
	if err != nil {
		return report, err
	}
	if err := collectBlobs(ctx, arts, st, o, kept, &report); err != nil {
		return report, err
	}
	if err := collectDerivations(ctx, st, o, &report); err != nil {
		return report, err
	}
	return report, nil
}

// lockAll takes both locks of every datatype and returns one release for all of them. A lock
// already held aborts the collection: something is writing, and this is the one operation that
// must not race a writer.
func lockAll(ctx context.Context, st State, datatypes []Datatype) (func(), error) {
	var releases []func() error
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			if err := releases[i](); err != nil {
				slog.WarnContext(ctx, "releasing a collection lock", "error", err.Error())
			}
		}
	}
	for _, dt := range datatypes {
		// Both keys: "<datatype>" is what a pipeline run takes and "fetch:<datatype>" what a
		// fetch takes. Collection excludes both.
		for _, key := range []string{dt.Name, "fetch:" + dt.Name} {
			done, err := st.Lock(ctx, key)
			if err != nil {
				release()
				return nil, fmt.Errorf("locking %s for collection: %w", key, err)
			}
			releases = append(releases, done)
		}
	}
	return release, nil
}
