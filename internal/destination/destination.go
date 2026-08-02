// Package destination writes view vectors to a vector store and reads them back.
//
// A destination is one physical sink: a pgvector table today, a Qdrant collection later.
// Drivers register themselves with Register and are reached only through the Destination
// interface, so adding one does not touch the pipeline.
//
// Three decisions from docs/feature-design.md shape this package:
//
//   - D7. A destination table is bound to exactly one embedder. AssertModel records the
//     binding on first use and refuses anything that disagrees with it, because vectors
//     from two models occupy different spaces and comparing them produces rankings that
//     look ordinary and mean nothing. The binding is checked again on every write, so a
//     caller that never called AssertModel cannot write at all.
//   - D8. The vector column is halfvec at the embedder's effective width. Both the width
//     and the storage type come from configuration, so the schema is rendered per
//     destination rather than fixed.
//   - D9. Every view of every datatype lives in one table, discriminated by columns and
//     served by one HNSW index, with iterative index scans keeping a filtered search
//     recall-correct. No view needs DDL, so views are added from config alone.
package destination

import "context"

// Granularity values, matching the destinations[].granularity vocabulary in config.
const (
	GranularityItem     = "item"
	GranularityFragment = "fragment"
)

// Destination is the sink surface the pipeline writes through. Implementations are safe
// for concurrent use.
type Destination interface {
	// Migrate brings the destination schema up to the embedded migration set. It is
	// idempotent.
	Migrate(ctx context.Context) error

	// Close releases the connection pool.
	Close() error

	// AssertModel binds the destination to one embedder, or verifies that it is already
	// bound to this one (D7). A disagreement is an error naming the remedy; it is never
	// resolved by overwriting the binding, because the rows already written were produced
	// by the other model.
	//
	// It must be called before any write. Upsert and BulkLoad check every row against the
	// binding it establishes, so forgetting the call fails loudly rather than silently
	// mixing vector spaces.
	AssertModel(ctx context.Context, model string, dims int, signature string) error

	// RebindModel replaces the binding and reports whether it changed. It is the one thing
	// AssertModel refuses to do, and `inget reindex` is the only caller: a destination whose
	// embedder changed accepts no write at all until the registry is rebound, so rebinding is
	// what breaks that deadlock. Every row still carrying the previous model is stale from the
	// moment it returns, which is why the reindex pass that follows is not optional.
	RebindModel(ctx context.Context, model string, dims int, signature string) (bool, error)

	// PruneStaleVectors deletes the rows whose model, dimensions or signature disagree with
	// the current binding, optionally restricted to one datatype, and reports how many it
	// deleted. After a reindex has rewritten everything state knows about, what remains is
	// what state does not: views removed from configuration and items deleted while the old
	// model was bound. Leaving them would mix two vector spaces in one index, which is the
	// outcome D7 exists to prevent.
	PruneStaleVectors(ctx context.Context, datatype string) (int, error)

	// Upsert writes rows, replacing any that already exist. This is the incremental path:
	// the cascade only reaches it for views that materially changed.
	Upsert(ctx context.Context, rows []Row) error

	// BulkLoad writes rows through a staging table and a single set-based upsert. This is
	// the cold-load and reindex path; for a handful of rows Upsert is cheaper.
	BulkLoad(ctx context.Context, rows []Row) error

	// DeleteItem removes every view of one item, which is what a tombstone becomes once it
	// reaches a destination.
	DeleteItem(ctx context.Context, datatype, itemID string) error

	// Search returns the nearest neighbours of a query vector, optionally restricted to
	// one view.
	Search(ctx context.Context, q SearchQuery) ([]SearchResult, error)
}

// Row is one vector and the provenance that makes D7 detectable after the fact.
//
// ID is derived from the row's identity rather than supplied: see RowID. Model, Dims and
// Signature must match the destination's binding, so they are fields rather than
// parameters — a batch cannot be half from one embedder.
type Row struct {
	ID          string
	Datatype    string
	ItemID      string
	ViewName    string
	Granularity string // "" is read as item
	FragKey     string // required when Granularity is fragment
	Text        string
	Embedding   []float32
	Model       string
	Dims        int
	Signature   string
	Metadata    map[string]string
	RelatedKeys []string
}

// SearchQuery is one nearest-neighbour lookup. An empty ViewName searches every view.
type SearchQuery struct {
	Embedding []float32
	Datatype  string
	ViewName  string
	EFSearch  int // 0 uses the destination's configured value
	Limit     int
}

// SearchResult is one hit. Score is cosine similarity in [-1, 1], so larger is closer;
// the distance operator's output is inverted here so that callers never have to remember
// which direction they are sorting.
type SearchResult struct {
	ID       string
	ItemID   string
	ViewName string
	Text     string
	Metadata map[string]string
	Score    float64
}
