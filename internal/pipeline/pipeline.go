// Package pipeline orchestrates the enrichment cascade for one datatype per run.
//
// A pipeline run:
//  1. Acquires a per-datatype exclusive lock.
//  2. Opens the latest artifact run and reconciles it against state.
//  3. Enqueues changed items as work.
//  4. Spawns a bounded worker pool that claims and processes items.
//  5. Checkpoints each item in one transaction, after its upsert has returned.
//  6. Handles SIGTERM: stops new claims, drains in-flight work, marks the run interrupted.
//
// All per-item processing happens within workers. The pipeline itself coordinates
// lifecycle, concurrency, and signal handling.
package pipeline

import (
	"context"

	"golang.org/x/sync/singleflight"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/destination"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/enrich/refs"
	"github.com/maxwell-cudlitz/inget/internal/model"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// Deps holds the resolved dependencies a pipeline run needs. The caller constructs it
// from configuration; the pipeline itself does not read config.
type Deps struct {
	State        state.Store
	Destinations map[string]destination.Destination // name → sink
	Embedder     model.Embedder
	Enrichers    map[string]enrich.Enricher  // view name → enricher
	FragEnricher *enrich.FragmentLLMEnricher // nil when fragment enrichment is disabled
	Refs         *refs.Set                   // nil when the datatype declares no references
	Config       DatatypeConfig
}

// DatatypeConfig is the pipeline-relevant subset of config.Datatype, avoiding a direct
// config import in the hot path.
type DatatypeConfig struct {
	Name             string
	Source           string
	Destinations     []string
	Views            []config.View
	ComposeOrder     string
	ComposeMaxChars  int
	FragmentEnricher bool
	MetadataFields   map[string]string
}

// RunConfig controls pipeline execution behaviour.
type RunConfig struct {
	Binary                   string           // binary name for the run record
	Concurrency              int              // generator permits and worker slots; from models.generator.concurrency
	ConfigHash               string           // for resumption matching
	DryRun                   bool             // plan mode: compute work and estimate cost, execute nothing
	Only                     []string         // restrict the work set to these item IDs; empty means all
	Limit                    int              // cap the work set; 0 means uncapped
	RebuildOnSignatureChange bool             // opt in to processing every live item after an enricher signature change
	CachedFragmentsOnly      bool             // require selected fragment summaries in cache before view generation
	IndexedOnly              bool             // restrict selected work to items with successful persisted checkpoints
	CachedFragmentSignatures []string         // explicit ordered historical cache signatures, usable only in cache-only mode
	Pricing                  Pricing          // what plan mode multiplies its token estimate by
	ViewPricing              *Pricing         // optional separate view role rates and output budget; nil uses Pricing
	EstimateProfile          *EstimateProfile // optional measured stage model, used only by plan
	// MaxReferenceDepth and MaxCascadePerRun bound the reference invalidation cascade
	// (enrich.*, D12). They are run settings rather than datatype settings because a cascade
	// crosses datatypes: the record that changed and the item that referenced it are
	// processed by different runs.
	MaxReferenceDepth int
	MaxCascadePerRun  int
	indexedItems      map[string]string // initial successful item fingerprints for --indexed-only; nil means no indexed items
}

// Pricing turns an estimated token count into money for plan mode. Zero prices report a
// zero cost, which is the honest answer for a locally served model.
type Pricing struct {
	PerMTokIn       float64
	PerMTokOut      float64
	MaxOutputTokens int // per-call output budget, used as the estimate's upper bound
}

// Stats accumulates per-run counters, reported at completion and stored on the run row.
type Stats struct {
	ItemsProcessed     int `json:"items_processed"`
	ItemsFailed        int `json:"items_failed"`
	FragmentsEnrich    int `json:"fragments_enriched"`
	ViewsGenerated     int `json:"views_generated"`
	ViewsSkipped       int `json:"views_skipped"`
	EmbeddingsStored   int `json:"embeddings_stored"`
	TombstonesApplied  int `json:"tombstones_applied"`
	ReferencesResolved int `json:"references_resolved"`
}

// Plan is what a run would do: the reconciled delta, the work set, and what it is
// estimated to cost. Run mode returns the same value it acted on, so `inget plan` and
// `inget run` cannot disagree about the work; only RunID is added once a run exists.
type Plan struct {
	Datatype      string `json:"datatype"`
	ArtifactRunID string `json:"artifact_run_id"`
	RunID         string `json:"run_id,omitempty"`
	TotalItems    int    `json:"total_items"`
	Added         int    `json:"added"`
	Modified      int    `json:"modified"`
	Deleted       int    `json:"deleted"`
	Unchanged     int    `json:"unchanged"`
	// Invalidated is the number of items entering the work set because a referenced record
	// changed, and Deferred the number the per-run cap left for the next run (D12).
	Invalidated int      `json:"invalidated"`
	Deferred    int      `json:"deferred"`
	WorkItems   []string `json:"work_items"`
	Tombstones  []string `json:"tombstones"`
	Resuming    bool     `json:"resuming"`
	// PendingArtifactRuns is how many committed artifact runs this datatype still owes work
	// to, this one included. More than one means a producer committed several runs since the
	// last clean pass — one CI job per item, typically — and `inget run` will drain them all
	// while this estimate covers only the run named in ArtifactRunID.
	PendingArtifactRuns int `json:"pending_artifact_runs"`
	// Restricted reports that --only or --limit narrowed the work set, so this pass cannot
	// speak for the whole artifact run: no tombstones are applied and the artifact
	// high-water mark does not advance.
	Restricted bool     `json:"restricted"`
	Estimate   Estimate `json:"estimate"`
}

// execution is the per-run state every worker needs: dependencies, the artifact run being
// read, the run's identity, its generator budget and its counters. Stage functions take it
// instead of six parameters each.
type execution struct {
	deps                     Deps
	arts                     *artifact.Store
	runID                    string
	gen                      *limiter            // shared bound on concurrent generator calls
	derive                   *singleflight.Group // collapses concurrent derivations of one fragment
	concurrency              int                 // goroutines per fan-out stage
	fragSig                  string              // fragment enricher signature; "" when disabled
	forceViews               bool                // bypass level-1 view skips for an opted-in signature rebuild
	cacheOnly                bool                // refuse fragment generation, including after preflight cache eviction
	cachedFragmentSignatures []string            // explicit historical cache signatures; current signature is always preferred
	refDepth                 int                 // enrich.max_reference_depth; bounds the cascade
	// changed is the items this run picked up because their own content moved, as opposed to
	// the ones a reference invalidated. It decides whether processing an item cascades: an
	// item that republished nothing has nothing to tell its referrers.
	changed map[string]bool
	stats   *statsCollector
}

// fragmentEnrichment reports whether per-fragment derivation is configured and available.
func (e *execution) fragmentEnrichment() bool {
	return e.fragSig != "" && e.deps.FragEnricher != nil
}

// contextKey is an unexported type for context values in this package.
type contextKey struct{}

// shutdownKey tags the context with a shutdown signal channel.
var shutdownKey = contextKey{}

// WithShutdown attaches a shutdown notification channel to the context. Workers check
// this to stop claiming new work.
func WithShutdown(ctx context.Context, ch <-chan struct{}) context.Context {
	return context.WithValue(ctx, shutdownKey, ch)
}

// ShuttingDown reports whether a shutdown has been signalled on ctx. It is exported because
// `inget reindex` observes the same channel through the same context: both are long passes over
// a work queue that must stop claiming and drain rather than die mid-item.
func ShuttingDown(ctx context.Context) bool {
	ch, ok := ctx.Value(shutdownKey).(<-chan struct{})
	if !ok {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
