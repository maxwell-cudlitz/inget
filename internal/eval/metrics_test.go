// Unit tests for the five metrics, over vectors built at known angles.
//
// Two-dimensional vectors at explicit angles make every expectation exact: the cosine of two
// of them is the cosine of the angle between them, so a threshold case can be constructed
// rather than discovered.
package eval

import (
	"math"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/config"
)

// at returns the unit vector at the given angle in degrees.
func at(deg float64) []float32 {
	rad := deg * math.Pi / 180
	return []float32{float32(math.Cos(rad)), float32(math.Sin(rad))}
}

// vecs builds one vector per angle, assigning them to items by the layout given: each element
// is one item's angles.
func vecs(items ...[]float64) []vector {
	var out []vector
	for i, angles := range items {
		for j, deg := range angles {
			out = append(out, vector{item: i, view: string(rune('a' + j)), v: at(deg)})
		}
	}
	return out
}

// corpusOf builds a corpus whose items carry the given view names, for the metrics that read
// text rather than vectors.
func corpusOf(views []string, items ...map[string]string) corpus {
	c := corpus{live: len(items), views: views}
	for i, texts := range items {
		c.viewed = append(c.viewed, string(rune('0'+i)))
		c.items = append(c.items, item{ID: string(rune('0' + i)), Views: texts})
	}
	return c
}

func TestSelfRetrieval(t *testing.T) {
	cases := []struct {
		name    string
		vectors []vector
		score   float64
		sample  int
		skipped bool
	}{
		{
			// Each item's two views are 10 degrees apart and the items are 90 apart, so every
			// view's nearest neighbour is its sibling.
			name:    "views of one item retrieve each other",
			vectors: vecs([]float64{0, 10}, []float64{90, 100}),
			score:   1,
			sample:  4,
		},
		{
			// Interleaved: every view's nearest neighbour belongs to the other item.
			name:    "views nearer another item are misses",
			vectors: vecs([]float64{0, 90}, []float64{5, 95}),
			score:   0,
			sample:  4,
		},
		{
			// One item is coherent, the other is not: 0 and 180 are as far apart as vectors get.
			name:    "one coherent item out of two",
			vectors: vecs([]float64{0, 3}, []float64{90, 270}),
			score:   0.5,
			sample:  4,
		},
		{
			name:    "single-view items are not scorable",
			vectors: vecs([]float64{0}, []float64{90}),
			skipped: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := selfRetrieval(selfHits(c.vectors), config.DefaultSelfRetrieval)
			if c.skipped {
				if got.Skipped == "" {
					t.Fatalf("self_retrieval = %v over %d, want it skipped", got.Score, got.Sample)
				}
				if got.Breached() {
					t.Error("a skipped metric reported a breach")
				}
				return
			}
			if got.Score != c.score || got.Sample != c.sample {
				t.Errorf("self_retrieval = %v over %d, want %v over %d",
					got.Score, got.Sample, c.score, c.sample)
			}
		})
	}
}

func TestDistinctivenessMetrics(t *testing.T) {
	// item 0: two views 90 degrees apart. item 1: two identical views. item 2: one view.
	vectors := vecs([]float64{0, 90}, []float64{45, 45}, []float64{180})
	pairs := pairwise(corpusOf(nil, nil, nil, nil), vectors)

	cross := distinctiveness(pairs, config.DefaultDistinctiveness)
	if cross.Sample != 8 {
		t.Errorf("distinctiveness sampled %d cross-item pairs, want 8", cross.Sample)
	}
	if !cross.Pass {
		t.Errorf("distinctiveness = %v, want at least %v", cross.Score, cross.Threshold)
	}

	// Item 0's views are one full unit apart, item 1's are identical, so the mean of the two
	// item means is 0.5. Item 2 has no pair and does not count.
	within := viewDistinctiveness(pairs, config.DefaultViewDistinctiveness)
	if within.Score != 0.5 || within.Sample != 2 {
		t.Errorf("view_distinctiveness = %v over %d, want 0.5 over 2", within.Score, within.Sample)
	}
}

func TestViewDistinctivenessSkipsWhenNoItemHasTwoViews(t *testing.T) {
	pairs := pairwise(corpusOf(nil, nil, nil), vecs([]float64{0}, []float64{90}))
	got := viewDistinctiveness(pairs, config.DefaultViewDistinctiveness)
	if got.Skipped == "" || got.Breached() {
		t.Errorf("view_distinctiveness = %+v, want it skipped and not a breach", got)
	}
}

func TestViewCoverage(t *testing.T) {
	views := []string{"role", "stack"}
	cases := []struct {
		name    string
		corpus  corpus
		score   float64
		sample  int
		skipped bool
	}{
		{
			name: "every configured view has text",
			corpus: corpusOf(views,
				map[string]string{"role": "a", "stack": "b"},
				map[string]string{"role": "c", "stack": "d"}),
			score:  1,
			sample: 4,
		},
		{
			name: "one view of one item is missing",
			corpus: corpusOf(views,
				map[string]string{"role": "a"},
				map[string]string{"role": "c", "stack": "d"}),
			score:  0.75,
			sample: 4,
		},
		{
			name: "a view not in the configured list does not count",
			corpus: corpusOf(views,
				map[string]string{"role": "a", "retired": "b"},
				map[string]string{"role": "c", "stack": "d"}),
			score:  0.75,
			sample: 4,
		},
		{
			name:    "a datatype with no configured views is not scorable",
			corpus:  corpusOf(nil, map[string]string{"role": "a"}),
			skipped: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := viewCoverage(c.corpus, config.DefaultViewCoverage)
			if c.skipped {
				if got.Skipped == "" {
					t.Fatalf("view_coverage = %+v, want it skipped", got)
				}
				return
			}
			if got.Score != c.score || got.Sample != c.sample {
				t.Errorf("view_coverage = %v over %d, want %v over %d",
					got.Score, got.Sample, c.score, c.sample)
			}
		})
	}
}

func TestMetadataTop3(t *testing.T) {
	// Four items, one view each, 30 degrees apart.
	vectors := vecs([]float64{0}, []float64{30}, []float64{60}, []float64{90})
	c := corpusOf(nil, nil, nil, nil, nil)

	t.Run("a query outside the top three misses", func(t *testing.T) {
		queries := make([][]float32, 4)
		queries[0] = at(89) // ranks item 3, 2 then 1; item 0 is fourth
		queries[1] = at(31) // ranks item 1 first
		got := metadataTop3(c, vectors, queries, config.DefaultMetadataTop3)
		if got.Score != 0.5 || got.Sample != 2 {
			t.Errorf("metadata_top3 = %v over %d, want 0.5 over 2", got.Score, got.Sample)
		}
	})

	t.Run("items without a query are not scored", func(t *testing.T) {
		got := metadataTop3(c, vectors, make([][]float32, 4), config.DefaultMetadataTop3)
		if got.Skipped == "" || got.Breached() {
			t.Errorf("metadata_top3 = %+v, want it skipped and not a breach", got)
		}
	})
}

func TestVerdictIgnoresSkippedMetrics(t *testing.T) {
	passing := Metric{Name: "a", Score: 1, Threshold: 0.5, Pass: true}
	skippedMetric := Metric{Name: "b", Threshold: 0.5, Skipped: "no evidence"}
	failing := Metric{Name: "c", Score: 0.1, Threshold: 0.5}

	if got := verdict([]Metric{passing, skippedMetric}); got != StatusOK {
		t.Errorf("verdict = %q with a skipped metric, want %q", got, StatusOK)
	}
	if got := verdict([]Metric{passing, skippedMetric, failing}); got != StatusBreach {
		t.Errorf("verdict = %q with a failing metric, want %q", got, StatusBreach)
	}
}
