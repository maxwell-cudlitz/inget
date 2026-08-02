// Unit tests for cosine similarity and top-k ranking.
package eval

import (
	"slices"
	"testing"
)

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical vectors", []float32{1, 0}, []float32{1, 0}, 1},
		{"orthogonal vectors", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite vectors", []float32{1, 0}, []float32{-1, 0}, -1},
		{"magnitude is ignored", []float32{3, 0}, []float32{0.5, 0}, 1},
		{"mismatched widths are unrankable", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"a zero vector is unrankable", []float32{0, 0}, []float32{1, 0}, 0},
		{"empty inputs are unrankable", nil, nil, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cosine(c.a, c.b); got != c.want {
				t.Errorf("cosine = %v, want %v", got, c.want)
			}
		})
	}
}

func TestTopK(t *testing.T) {
	// Angles ascending, so proximity to a query at 0 degrees is the same as index order.
	pool := vecs([]float64{0}, []float64{20}, []float64{40}, []float64{60})

	cases := []struct {
		name string
		skip int
		k    int
		want []int
	}{
		{"best first", -1, 3, []int{0, 1, 2}},
		{"k of one", -1, 1, []int{0}},
		{"the skipped index is excluded", 0, 2, []int{1, 2}},
		{"k larger than the pool returns everything", -1, 10, []int{0, 1, 2, 3}},
		{"k of zero returns nothing", -1, 0, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := topK(pool, at(0), c.skip, c.k)
			if !slices.Equal(got, c.want) {
				t.Errorf("topK = %v, want %v", got, c.want)
			}
		})
	}
}

// TestTopKBreaksTiesByOrder pins the behaviour that makes a corpus containing two identical
// texts score the same on every run.
func TestTopKBreaksTiesByOrder(t *testing.T) {
	pool := vecs([]float64{0}, []float64{0}, []float64{0})
	for range 5 {
		if got := topK(pool, at(0), -1, 2); !slices.Equal(got, []int{0, 1}) {
			t.Fatalf("topK = %v, want [0 1] on every call", got)
		}
	}
}

func TestSampleIsDeterministicAndSorted(t *testing.T) {
	ids := []string{"e", "a", "d", "b", "c"}

	first := sample(ids, 3)
	if len(first) != 3 {
		t.Fatalf("sample returned %d ids, want 3", len(first))
	}
	if !slices.IsSorted(first) {
		t.Errorf("sample = %v, want it sorted for stable reporting", first)
	}
	// Digest order, not ID order: the sample is not simply the alphabetical head.
	if !slices.Equal(first, sample(slices.Clone(ids), 3)) {
		t.Errorf("sample is not stable across calls: %v", first)
	}
	if got := sample(ids, 0); !slices.Equal(got, []string{"a", "b", "c", "d", "e"}) {
		t.Errorf("sample with no bound = %v, want every id sorted", got)
	}
	if got := sample(ids, 99); len(got) != len(ids) {
		t.Errorf("sample larger than the corpus returned %d ids, want %d", len(got), len(ids))
	}
}
