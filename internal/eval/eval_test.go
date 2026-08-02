// End-to-end behaviour of the harness against a real state store.
//
// The gate case is TestDegradedViewsBreachThresholds: the same items, the same embedder, and
// the only difference is that every view says the same generic thing. If that does not fail,
// nothing about `inget eval` is worth running.
package eval

import (
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/model"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// metric returns the named metric from a report.
func metric(t *testing.T, r Report, name string) Metric {
	t.Helper()
	for _, m := range r.Metrics {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("report has no %s metric: %+v", name, r.Metrics)
	return Metric{}
}

func TestHealthyCorpusPassesEveryThreshold(t *testing.T) {
	store := openStore(t)
	seed(t, store, healthyCorpus())

	report, err := Run(t.Context(), store, &lexicalEmbedder{dims: 64}, options())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !report.Passed() {
		t.Errorf("status = %q, want %q; metrics: %+v", report.Status, StatusOK, report.Metrics)
	}
	if report.LiveItems != 4 || report.ViewedItems != 4 || report.SampledItems != 4 {
		t.Errorf("item counts = live %d, viewed %d, sampled %d; want 4, 4, 4",
			report.LiveItems, report.ViewedItems, report.SampledItems)
	}
	if report.ViewVectors != 12 {
		t.Errorf("ViewVectors = %d, want 12", report.ViewVectors)
	}
	for _, m := range report.Metrics {
		if m.Skipped != "" {
			t.Errorf("metric %s was skipped: %s", m.Name, m.Skipped)
		}
	}
	if got := metric(t, report, MetricViewCoverage); got.Score != 1 {
		t.Errorf("view_coverage = %v, want 1", got.Score)
	}
}

func TestDegradedViewsBreachThresholds(t *testing.T) {
	store := openStore(t)
	seed(t, store, degradedCorpus())

	report, err := Run(t.Context(), store, &lexicalEmbedder{dims: 64}, options())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Status != StatusBreach {
		t.Fatalf("status = %q, want %q; metrics: %+v", report.Status, StatusBreach, report.Metrics)
	}
	// Every view is the same text, so both distance metrics collapse to zero and no view can
	// retrieve its own item ahead of an identical view of another item.
	for _, name := range []string{MetricDistinctiveness, MetricViewDistinctiveness} {
		if got := metric(t, report, name); !got.Breached() {
			t.Errorf("%s = %v over %d, want a breach of %v", name, got.Score, got.Sample, got.Threshold)
		}
	}
	// Coverage is still perfect: the text is present, it is only worthless. That is the point
	// of scoring five metrics rather than counting rows.
	if got := metric(t, report, MetricViewCoverage); !got.Pass {
		t.Errorf("view_coverage = %v, want a pass: every configured view has text", got.Score)
	}
}

func TestMissingViewTextLowersCoverage(t *testing.T) {
	store := openStore(t)
	items := healthyCorpus()
	// Two items produced no operations view, as a prompt that has started refusing leaves it.
	delete(items[0].views, "operations")
	delete(items[1].views, "operations")
	seed(t, store, items)

	report, err := Run(t.Context(), store, &lexicalEmbedder{dims: 64}, options())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	coverage := metric(t, report, MetricViewCoverage)
	if want := round(10.0 / 12.0); coverage.Score != want {
		t.Errorf("view_coverage = %v, want %v", coverage.Score, want)
	}
	if coverage.Pass {
		t.Errorf("view_coverage %v passed the %v threshold", coverage.Score, coverage.Threshold)
	}
	if report.Status != StatusBreach {
		t.Errorf("status = %q, want %q", report.Status, StatusBreach)
	}
	for _, v := range report.Views {
		if v.Name == "operations" && v.Vectors != 2 {
			t.Errorf("operations view has %d vectors, want 2", v.Vectors)
		}
	}
}

func TestUnscorableCorpora(t *testing.T) {
	cases := []struct {
		name   string
		seed   func(t *testing.T, store state.Store)
		reason string
	}{
		{
			name:   "no items at all",
			seed:   func(*testing.T, state.Store) {},
			reason: "no items in state",
		},
		{
			name: "items but no views",
			seed: func(t *testing.T, store state.Store) {
				items := healthyCorpus()
				for i := range items {
					items[i].views = nil
				}
				seed(t, store, items)
			},
			reason: "stored view text",
		},
		{
			name: "one item with views",
			seed: func(t *testing.T, store state.Store) {
				seed(t, store, healthyCorpus()[:1])
			},
			reason: "needs at least 2",
		},
		{
			name: "views under names configuration no longer declares",
			seed: func(t *testing.T, store state.Store) {
				items := healthyCorpus()
				for i := range items {
					items[i].views = map[string]string{"retired": "text that no configured view claims"}
				}
				seed(t, store, items)
			},
			reason: "named differently",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := openStore(t)
			c.seed(t, store)

			report, err := Run(t.Context(), store, &lexicalEmbedder{dims: 64}, options())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if report.Status != StatusUnscorable || report.Passed() {
				t.Fatalf("status = %q, want %q and not a pass", report.Status, StatusUnscorable)
			}
			if !strings.Contains(report.Reason, c.reason) {
				t.Errorf("reason = %q, want it to mention %q", report.Reason, c.reason)
			}
			if len(report.Metrics) != 0 {
				t.Errorf("an unscorable corpus reported %d metrics, want none", len(report.Metrics))
			}
		})
	}
}

// TestSampleIsStableAndBounded asserts the property the --embedder A/B depends on: two runs
// over the same corpus score the same items, and a sample smaller than the corpus is still
// drawn from across it rather than from one alphabetical end.
func TestSampleIsStableAndBounded(t *testing.T) {
	store := openStore(t)
	seed(t, store, healthyCorpus())

	opts := options()
	opts.SampleSize = 3

	first, err := Run(t.Context(), store, &lexicalEmbedder{dims: 64}, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	second, err := Run(t.Context(), store, &lexicalEmbedder{dims: 64}, opts)
	if err != nil {
		t.Fatalf("Run again: %v", err)
	}

	if first.SampledItems != 3 || first.ViewedItems != 4 {
		t.Errorf("sampled %d of %d viewed items, want 3 of 4", first.SampledItems, first.ViewedItems)
	}
	if first.Status != second.Status {
		t.Fatalf("two runs disagreed: %q then %q", first.Status, second.Status)
	}
	for i, m := range first.Metrics {
		if m != second.Metrics[i] {
			t.Errorf("metric %s differed between runs: %+v then %+v", m.Name, m, second.Metrics[i])
		}
	}
}

// TestHarnessNeverGenerates is the mechanical statement of D14: eval takes an Embedder and no
// Generator, so a generator handed to the process cannot be reached. The counter proves the
// embedder is the only model the harness calls, and how many texts it saw.
func TestHarnessEmbedsEveryViewAndQueryOnce(t *testing.T) {
	store := openStore(t)
	seed(t, store, healthyCorpus())

	emb := model.NewFakeEmbedder("fake", 32)
	if _, err := Run(t.Context(), store, emb, options()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Twelve view texts plus one metadata query per item.
	if got := emb.Embedded(); got != 16 {
		t.Errorf("embedded %d texts, want 16 (12 views + 4 metadata queries)", got)
	}
}
