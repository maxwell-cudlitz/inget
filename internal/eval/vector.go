// Vector arithmetic for the metrics: cosine similarity and ranking.
//
// The embedder contract is unit-length vectors, but nothing here assumes it: cosine divides
// by both norms, so an embedder that returns un-normalized vectors produces correct scores
// rather than quietly wrong ones.
package eval

import "math"

// vector is one embedded view, carrying which sampled item and which view it came from.
type vector struct {
	item int // index into corpus.items
	view string
	v    []float32
}

// cosine returns the cosine similarity of a and b, in [-1, 1]. Mismatched widths and zero
// vectors return 0: neither can be ranked, and neither is an error the caller can act on.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// distance returns cosine distance, which is what the two distinctiveness metrics average.
func distance(a, b []float32) float64 {
	return 1 - cosine(a, b)
}

// topK returns the indices of the k vectors closest to q, best first, skipping the index
// skip (pass -1 to skip nothing).
//
// It is a full scan with an insertion into a k-sized list, which is the right shape here:
// k is 1 or 3 and the pool is one sample, so a heap or an index would be more code for no
// measurable difference. Ties keep the earlier vector, and vector order is deterministic,
// so a corpus containing two identical texts still scores the same way on every run.
func topK(pool []vector, q []float32, skip, k int) []int {
	if k <= 0 {
		return nil
	}
	best := make([]int, 0, k)
	scores := make([]float64, 0, k)

	for i := range pool {
		if i == skip {
			continue
		}
		s := cosine(q, pool[i].v)
		at := len(scores)
		for at > 0 && s > scores[at-1] {
			at--
		}
		if at >= k {
			continue
		}
		best = insertAt(best, at, i, k)
		scores = insertAt(scores, at, s, k)
	}
	return best
}

// insertAt inserts v at index at, keeping the slice no longer than max.
func insertAt[T any](s []T, at int, v T, max int) []T {
	if len(s) < max {
		s = append(s, v)
	}
	copy(s[at+1:], s[at:])
	s[at] = v
	return s
}
