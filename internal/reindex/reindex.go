// Package reindex rebuilds a datatype's vectors under the currently configured embedder.
//
// It exists because of one asymmetry in D7: a destination table is bound to exactly one
// embedder, and the write path refuses every row that disagrees with the binding. Change
// models.embedder and `inget run` cannot proceed — not for the changed items, not for anything —
// until the binding is replaced. Reindex is the only thing allowed to replace it, and having
// replaced it, it is responsible for making the index consistent again.
//
// What it does not do is generate. The text a view holds in state is by definition the text its
// stored vector was produced from, so an embedder change needs no generator, no artifacts and no
// tokens: read the text, embed it, write it back. A prompt or generator change is the other
// direction entirely — it changes what the text should say — and that is `inget run`'s cascade,
// which already regenerates a view whose enricher signature moved and already prices the work in
// `inget plan` before spending anything. Duplicating that here would be a second implementation
// of stages 5 to 11 with no artifacts to read.
//
// The pass is resumable for the same reason the pipeline is: it claims per-item work rows, writes
// each item's destination rows before its state rows, and adopts an unfinished run of its own
// binary rather than starting a second one. A re-embed of half a million views is not something
// to restart because a pod moved.
package reindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/maxwellcudlitz/inget/internal/destination"
	"github.com/maxwellcudlitz/inget/internal/model"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// Binary is the name reindex records on its run rows. It is distinct from "inget" so that a
// half-finished reindex is never adopted as a resumable pipeline run, and vice versa: the two
// compute different work sets from the same datatype.
const Binary = "inget-reindex"

// DefaultBatchSize is how many items one claim takes. Views of a batch are embedded in a single
// call, so the batch is the unit that amortizes round trips; it is small enough that an
// interruption loses very little and large enough that per-request latency disappears.
const DefaultBatchSize = 8

// State is the slice of state.Store this package needs, declared by the consumer.
type State interface {
	Lock(ctx context.Context, key string) (release func() error, err error)
	ViewedItems(ctx context.Context, datatype string) ([]string, error)
	Item(ctx context.Context, datatype, itemID string) (state.Item, bool, error)
	ViewState(ctx context.Context, datatype, itemID string) (map[string]state.ViewState, error)
	PutViewState(ctx context.Context, datatype, itemID string, v state.ViewState) error
	Refs(ctx context.Context, from state.ItemKey) ([]state.RefEdge, error)
	ResumableRun(ctx context.Context, binary, datatype, configHash string) (string, bool, error)
	StartRun(ctx context.Context, r state.Run) error
	FinishRun(ctx context.Context, runID, status string, stats any) error
	EnqueueWork(ctx context.Context, runID, datatype string, ids []string) error
	ClaimWork(ctx context.Context, runID, datatype string, n int) ([]string, error)
	ResetClaims(ctx context.Context, runID, datatype string) (int, error)
	CompleteWork(ctx context.Context, runID, datatype, itemID string, cause error) error
}

// Deps are the resolved dependencies of one pass. Destinations is keyed by name; Options names
// which of them to write and in what order.
type Deps struct {
	State        State
	Embedder     model.Embedder
	Destinations map[string]destination.Destination
}

// Options is one datatype's reindex request.
type Options struct {
	Datatype     string
	Destinations []string // names to write, in order; must all be present in Deps
	// Views restricts the pass to these view names. Empty means every view state holds, which
	// is also the only scope from which pruning is safe: pruning after a partial pass would
	// delete the rows of the views this pass deliberately did not rewrite.
	Views          []string
	MetadataFields map[string]string // the datatype's metadata_fields
	ConfigHash     string            // resumption key, as for a pipeline run
	BatchSize      int
	// Force re-embeds views that already carry the configured embedder. Without it a second
	// pass costs nothing, which is the property that makes reindex safe to retry; with it a
	// destination that lost rows can be refilled from state.
	Force bool
	// MetadataRefs says the datatype injects resolved reference values into metadata. Those
	// values are not persisted, so a rewritten row cannot carry them and the pass says so once
	// rather than silently narrowing what a metadata filter matches.
	MetadataRefs bool
}

// normalize fills in defaults and rejects what cannot work.
func (o Options) normalize(deps Deps) (Options, error) {
	if o.Datatype == "" {
		return o, errors.New("reindex needs a datatype")
	}
	if len(o.Destinations) == 0 {
		return o, fmt.Errorf("reindex of %s needs at least one destination", o.Datatype)
	}
	for _, name := range o.Destinations {
		if _, ok := deps.Destinations[name]; !ok {
			return o, fmt.Errorf("destination %q is not open", name)
		}
	}
	if deps.Embedder == nil {
		return o, errors.New("reindex needs an embedder")
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	return o, nil
}

// fullScope reports whether the pass covers every view state holds, which is what makes the
// prune that follows it safe.
func (o Options) fullScope() bool { return len(o.Views) == 0 }

// Report is what one pass did.
type Report struct {
	Datatype    string   `json:"datatype"`
	RunID       string   `json:"run_id"`
	Embedder    string   `json:"embedder"`
	Dims        int      `json:"dims"`
	Rebound     []string `json:"rebound_destinations"`
	Items       int      `json:"items_rewritten"`
	Unchanged   int      `json:"items_unchanged"`
	Failed      int      `json:"items_failed"`
	Vectors     int      `json:"vectors_written"`
	Pruned      int      `json:"vectors_pruned"`
	Resuming    bool     `json:"resuming"`
	Interrupted bool     `json:"interrupted"`
}

// Run rebinds every named destination, re-embeds the datatype's stored view text, and prunes
// whatever the new embedder never wrote.
func Run(ctx context.Context, deps Deps, o Options) (Report, error) {
	o, err := o.normalize(deps)
	if err != nil {
		return Report{}, err
	}
	emb := deps.Embedder
	report := Report{Datatype: o.Datatype, Embedder: emb.Model(), Dims: emb.Dims()}

	release, err := deps.State.Lock(ctx, o.Datatype)
	if err != nil {
		return report, fmt.Errorf("locking %s for reindex: %w", o.Datatype, err)
	}
	defer func() {
		if err := release(); err != nil {
			slog.WarnContext(ctx, "releasing the reindex lock", "datatype", o.Datatype, "error", err.Error())
		}
	}()

	if err := rebind(ctx, deps, o, &report); err != nil {
		return report, err
	}
	if o.MetadataRefs {
		slog.WarnContext(ctx, "reference-injected metadata cannot be rebuilt from state",
			"datatype", o.Datatype,
			"note", "rewritten rows carry item metadata and related_keys; the injected values return "+
				"when inget run next reprocesses the item")
	}

	items, err := deps.State.ViewedItems(ctx, o.Datatype)
	if err != nil {
		return report, err
	}
	if len(items) == 0 {
		// Nothing to rewrite, so nothing to prune against either: deleting every row of a
		// datatype because its state is empty is not a recovery, it is a second outage.
		slog.WarnContext(ctx, "no stored views to reindex",
			"datatype", o.Datatype, "note", "run inget run first; no rows were deleted")
		return report, nil
	}

	if err := runPass(ctx, deps, o, items, &report); err != nil {
		return report, err
	}
	if err := prune(ctx, deps, o, &report); err != nil {
		return report, err
	}
	return report, nil
}
