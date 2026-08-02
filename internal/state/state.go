// Package state persists every level of the invalidation cascade: item fingerprints,
// fragment fingerprints, cached derivations, view input and embedding hashes, reference
// edges, enricher signatures, run history and per-item work checkpoints.
//
// Nothing durable lives on local disk (D13). One Store interface has two drivers:
// postgres, the default, and sqlite for offline development and tests. Both are reached
// through database/sql — postgres via pgx's stdlib adapter over a pgxpool, sqlite via
// modernc.org/sqlite — so the statements are written once and only three things vary by
// dialect: placeholder syntax, the presence of SKIP LOCKED, and how a lock is held. Those
// three live in dialect.go; every other file in this package is dialect-agnostic.
//
// Concurrency is guarded by Lock, which a run takes per datatype before it claims any
// work. Progress is checkpointed per item in the work table, so an interrupted run resumes
// where it stopped instead of restarting and paying for the same tokens twice.
//
// Timestamps are written by the database (DEFAULT CURRENT_TIMESTAMP) and no method returns
// one. That is deliberate: SQLite has no timestamp type, so any timestamp crossing this
// boundary would need per-dialect encoding for no current caller's benefit. `inget state
// show` is where reading them starts to matter, and it can add typed accessors then.
package state

import "context"

// Store is the persistence surface both binaries share. Every method is safe for
// concurrent use; callers are expected to hold Lock for the datatype they mutate.
type Store interface {
	// Migrate brings the schema up to the embedded migration set. It is idempotent.
	Migrate(ctx context.Context) error

	// Close releases the connection pool. A held Lock is not released by Close; release
	// it first.
	Close() error

	// Lock takes an exclusive, process-scoped lock on key, conventionally a datatype
	// name. It does not block: a lock already held returns ErrLocked, which is how a
	// CronJob that overlapped its predecessor exits cleanly instead of duplicating work.
	Lock(ctx context.Context, key string) (release func() error, err error)

	// ForceUnlock removes a lock no live process holds, reporting whether it removed one.
	// It is the escape hatch behind `inget state unlock`, and it only ever has work to do
	// on sqlite: a postgres advisory lock dies with its session, so a held one belongs to a
	// running process and is reported as ErrLocked rather than taken away from it.
	ForceUnlock(ctx context.Context, key string) (bool, error)

	// ItemFingerprints returns item ID to level-0 fingerprint for every live item of a
	// datatype. Tombstoned items are absent, so an item that reappears reconciles as
	// added.
	ItemFingerprints(ctx context.Context, datatype string) (map[string]string, error)

	// Item returns one live item's persisted state. The bool reports whether it was
	// found; a tombstoned item is reported as not found.
	Item(ctx context.Context, datatype, itemID string) (Item, bool, error)

	// PutItem upserts an item and clears any tombstone on it.
	PutItem(ctx context.Context, datatype string, it Item) error

	// Tombstone marks items deleted without removing their rows, so a subsequent run can
	// still see what was collected and why. Only full-scope fetches issue tombstones.
	Tombstone(ctx context.Context, datatype string, ids []string) error

	// Fragments returns the persisted fragment set for one item, keyed by fragment key.
	Fragments(ctx context.Context, datatype, itemID string) (map[string]FragmentState, error)

	// PutFragments persists an item's complete fragment set as of this run. Fragments
	// absent from f have their missing_runs counter incremented, which is what makes
	// their derivations collectable after retention.missing_runs runs.
	PutFragments(ctx context.Context, datatype, itemID string, f []FragmentState) error

	// Derivation returns a cached fragment derivation and whether it was found, touching
	// its last-hit timestamp.
	Derivation(ctx context.Context, cacheKey string) (string, bool, error)

	// HasDerivation reports whether a derivation is cached without touching its last-hit
	// timestamp, which is what `inget plan` needs: estimating a run's cost must not change
	// which entries a later gc considers cold.
	HasDerivation(ctx context.Context, cacheKey string) (bool, error)

	// PutDerivation caches one fragment derivation under its cache key.
	PutDerivation(ctx context.Context, d Derivation) error

	// ViewState returns the persisted views of one item, keyed by view name.
	ViewState(ctx context.Context, datatype, itemID string) (map[string]ViewState, error)

	// ViewedItems returns the IDs of every item of a datatype that has at least one stored
	// view, sorted. It is what `inget eval` samples from: an item with no view text has
	// nothing to score, and finding those by asking per item would be one query per item
	// in the corpus.
	ViewedItems(ctx context.Context, datatype string) ([]string, error)

	// PutViewState upserts one view of one item.
	PutViewState(ctx context.Context, datatype, itemID string, v ViewState) error

	// PutRefs replaces the complete reference edge set of one item. Edges omitted from
	// the call are removed, so a reference deleted from config stops invalidating. It also
	// clears the item's staleness marks: a rewritten edge set is a re-resolved one.
	PutRefs(ctx context.Context, from ItemKey, edges []RefEdge) error

	// Refs returns one item's outgoing reference edges, carrying the digest each was last
	// resolved to and any staleness mark on it.
	Refs(ctx context.Context, from ItemKey) ([]RefEdge, error)

	// ReferencedBy returns the items whose references point at a key: the reverse edges
	// the invalidation cascade walks when a referent changes (D12).
	ReferencedBy(ctx context.Context, kind, key string) ([]ItemKey, error)

	// MarkRefsStale marks every edge pointing at a key as needing re-resolution at the
	// given cascade depth, and reports how many it marked. A mark already shallower than
	// depth is left alone, so the bound a cycle terminates on cannot be reset by a longer
	// path reaching the same record.
	MarkRefsStale(ctx context.Context, kind, key string, depth int) (int, error)

	// StaleReferrers returns the items of a datatype carrying a staleness mark, with the
	// shallowest depth marked on each, sorted by item ID. Marks survive until the item
	// re-resolves, which is what lets a capped run defer the remainder to the next one.
	StaleReferrers(ctx context.Context, datatype string) ([]StaleReferrer, error)

	// Signature returns the last recorded signature for a scope, or "" when the scope has
	// never been recorded. Absence is not an error: every scope is new once.
	Signature(ctx context.Context, scope string) (string, error)

	// PutSignature records the current signature for a scope.
	PutSignature(ctx context.Context, scope, signature string) error

	// StartRun records a run as running.
	StartRun(ctx context.Context, r Run) error

	// FinishRun closes a run with a terminal status and its statistics.
	FinishRun(ctx context.Context, runID, status string, stats any) error

	// ResumableRun returns the newest unfinished run matching binary, datatype and
	// config hash, so a restarted process adopts its work queue instead of enqueueing a
	// second copy. A changed config hash deliberately does not match: the work set that
	// run computed may no longer be the right one.
	ResumableRun(ctx context.Context, binary, datatype, configHash string) (string, bool, error)

	// RunStatus returns a run's recorded status and whether the run exists. Without it the
	// lifecycle FinishRun writes would be unobservable to any caller, and "the pod was
	// evicted, so the run is interrupted" is a claim worth being able to check.
	RunStatus(ctx context.Context, runID string) (string, bool, error)

	// EnqueueWork adds items to a run's queue. Already-queued items are left untouched,
	// so re-enqueueing on resume preserves what is already done.
	EnqueueWork(ctx context.Context, runID, datatype string, ids []string) error

	// ClaimWork claims up to n pending items for exclusive processing and returns their
	// IDs in ascending order. Concurrent callers never receive the same item.
	ClaimWork(ctx context.Context, runID, datatype string, n int) ([]string, error)

	// ResetClaims returns claimed-but-uncompleted items to pending and reports how many.
	// A run calls it after taking Lock: anything still claimed at that point belonged to
	// a process that died, because the lock guarantees no live process holds it.
	ResetClaims(ctx context.Context, runID, datatype string) (int, error)

	// CompleteWork marks one item done, or failed with cause when cause is non-nil.
	CompleteWork(ctx context.Context, runID, datatype, itemID string, cause error) error

	// CheckpointItem writes an item's level-0, level-1, level-2 and level-3 guards and
	// marks its work row done, in one transaction. It is how a pipeline run completes an
	// item: the guards claim that generation, embedding and upsert already happened, so
	// they must not become visible unless the work row does too.
	CheckpointItem(ctx context.Context, datatype string, cp Checkpoint) error

	// LiveBlobRefs returns every blob digest a live fragment row points at, across every
	// datatype. It is half of garbage collection's retention predicate: a blob is
	// collectable only when no retained artifact run and no live fragment references it.
	LiveBlobRefs(ctx context.Context) (map[string]struct{}, error)

	// CollectDerivations deletes the cached derivations of fragments that have been absent
	// for more than missingRuns runs, and reports how many it deleted. It is the one path
	// that discards work already paid for, which is why the window is configuration
	// (retention.missing_runs) rather than a constant.
	CollectDerivations(ctx context.Context, missingRuns int) (int, error)

	// Counts returns how many rows each cascade level holds for one datatype, for
	// `inget state show`.
	Counts(ctx context.Context, datatype string) (Counts, error)

	// RecentRuns returns the newest runs, most recent first, optionally restricted to one
	// datatype. It is the only read that returns a timestamp; see inspect.go.
	RecentRuns(ctx context.Context, datatype string, limit int) ([]RunRecord, error)
}
