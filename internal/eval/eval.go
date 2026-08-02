// Package eval implements the retrieval quality harness behind `inget eval` (D14).
//
// It samples items of a datatype, re-embeds the view text those items already have in
// state, and scores five metrics against configured thresholds. A breach is reported per
// datatype and per view and makes the command exit non-zero, which is what turns quality
// into a gate rather than an assumption.
//
// Three properties are deliberate and worth knowing before changing anything here:
//
//   - It never calls the generator. Views are read from state.views.text, which is by
//     definition the text each stored vector was produced from. Generation is not
//     reproducible on either candidate provider (D10), so regenerating per run would fold
//     that variance into every metric and confound the --embedder comparison this decision
//     exists to support.
//   - Retrieval is computed inside the sample, not against a destination. The sample is
//     re-embedded by the embedder under test, whose vectors do not belong in a destination
//     bound to another model (D7), and a quality gate should not require a reachable vector
//     database. The consequence is that scores compare across runs and across embedders
//     only at a fixed sample size.
//   - An unscorable sample is reported as unscorable. An empty corpus is not a pass: it
//     means `inget-fetch` and `inget run` have not populated the state this harness reads.
package eval

import (
	"context"
	"fmt"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/model"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// Store is the slice of state.Store this package needs, declared by the consumer so that a
// test can satisfy it without a database and so that the harness cannot mutate state.
type Store interface {
	ItemFingerprints(ctx context.Context, datatype string) (map[string]string, error)
	Item(ctx context.Context, datatype, itemID string) (state.Item, bool, error)
	ViewedItems(ctx context.Context, datatype string) ([]string, error)
	ViewState(ctx context.Context, datatype, itemID string) (map[string]state.ViewState, error)
}

// Options is one datatype's evaluation request. Views are the view names configuration
// declares, which is what makes coverage measurable: a view that produced nothing has no
// row in state, so only the configuration knows it was expected.
type Options struct {
	Datatype   string
	Views      []string
	SampleSize int
	Thresholds config.Thresholds
}

// Run evaluates one datatype and returns its report. It returns an error only when state
// or the embedder is unreachable; a failing corpus is a Report with a non-passing status,
// not an error, because the caller has to print the numbers either way.
func Run(ctx context.Context, st Store, emb model.Embedder, o Options) (Report, error) {
	report := Report{
		Datatype: o.Datatype,
		Embedder: emb.Model(),
		Dims:     emb.Dims(),
	}

	c, err := loadCorpus(ctx, st, o)
	if err != nil {
		return report, err
	}
	report.LiveItems = c.live
	report.ViewedItems = len(c.viewed)
	report.SampledItems = len(c.items)

	if reason := c.unscorable(); reason != "" {
		report.Status = StatusUnscorable
		report.Reason = reason
		return report, nil
	}

	vecs, err := embedViews(ctx, emb, c)
	if err != nil {
		return report, err
	}
	queries, err := embedQueries(ctx, emb, c)
	if err != nil {
		return report, err
	}
	report.ViewVectors = len(vecs)

	hits := selfHits(vecs)
	report.Metrics = score(c, vecs, hits, queries, o.Thresholds)
	report.Views = perView(c, vecs, hits)
	report.Status = verdict(report.Metrics)
	return report, nil
}

// embedViews embeds every non-empty view text in the sample, in a deterministic order, and
// returns one vector per view carrying the item it belongs to.
func embedViews(ctx context.Context, emb model.Embedder, c corpus) ([]vector, error) {
	texts := make([]string, 0, len(c.items)*2)
	vecs := make([]vector, 0, len(c.items)*2)
	for i, it := range c.items {
		for _, name := range it.viewNames() {
			texts = append(texts, it.Views[name])
			vecs = append(vecs, vector{item: i, view: name})
		}
	}

	embedded, err := emb.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embedding %d view texts: %w", len(texts), err)
	}
	if len(embedded) != len(texts) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d view texts", len(embedded), len(texts))
	}
	for i := range vecs {
		vecs[i].v = embedded[i]
	}
	return vecs, nil
}

// embedQueries embeds one metadata-derived query per sampled item. An item whose metadata
// yields no query contributes a nil vector, which the metric counts as unscorable rather
// than as a miss.
func embedQueries(ctx context.Context, emb model.Embedder, c corpus) ([][]float32, error) {
	texts := make([]string, 0, len(c.items))
	at := make([]int, 0, len(c.items))
	for i, it := range c.items {
		if q := metadataQuery(it.Metadata); q != "" {
			texts = append(texts, q)
			at = append(at, i)
		}
	}

	queries := make([][]float32, len(c.items))
	if len(texts) == 0 {
		return queries, nil
	}
	embedded, err := emb.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embedding %d metadata queries: %w", len(texts), err)
	}
	if len(embedded) != len(texts) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d metadata queries", len(embedded), len(texts))
	}
	for i, item := range at {
		queries[item] = embedded[i]
	}
	return queries, nil
}
