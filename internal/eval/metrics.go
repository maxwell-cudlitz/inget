// The five metrics of D14, scored over one re-embedded sample.
//
// Every metric reports its own denominator, and a denominator of zero is reported as
// skipped rather than as a score: "no evidence" and "perfect" are different answers, and a
// gate that conflates them passes silently on an empty corpus.
package eval

import (
	"math"

	"github.com/maxwellcudlitz/inget/internal/config"
)

// Metric names, as they appear in the report and in the config threshold keys.
const (
	MetricSelfRetrieval       = "self_retrieval"
	MetricDistinctiveness     = "distinctiveness"
	MetricViewCoverage        = "view_coverage"
	MetricViewDistinctiveness = "view_distinctiveness"
	MetricMetadataTop3        = "metadata_top3"
)

// score computes every metric in the order D14 lists them.
func score(c corpus, vecs []vector, hits []hit, queries [][]float32, t config.Thresholds) []Metric {
	pairs := pairwise(c, vecs)
	return []Metric{
		selfRetrieval(hits, t.SelfRetrieval),
		distinctiveness(pairs, t.Distinctiveness),
		viewCoverage(c, t.ViewCoverage),
		viewDistinctiveness(pairs, t.ViewDistinctiveness),
		metadataTop3(c, vecs, queries, t.MetadataTop3),
	}
}

// hit is one vector's nearest-neighbour outcome.
type hit struct {
	scorable bool // the vector's item has another view to be retrieved by
	same     bool // the nearest other vector belongs to the same item
}

// selfHits computes the nearest-neighbour outcome of every vector once, so that the
// aggregate metric and the per-view breakdown cannot disagree about it.
//
// The vector itself is excluded from its own ranking. Including it would make the metric
// trivially perfect — a query identical to a document always ranks that document first — so
// what is measured is whether an item's views retrieve *each other* before they retrieve
// another item's. An item with a single view has nothing to be retrieved by and is not
// scored.
func selfHits(vecs []vector) []hit {
	perItem := map[int]int{}
	for _, v := range vecs {
		perItem[v.item]++
	}

	hits := make([]hit, len(vecs))
	for i, v := range vecs {
		if perItem[v.item] < 2 {
			continue
		}
		hits[i].scorable = true
		if best := topK(vecs, v.v, i, 1); len(best) == 1 {
			hits[i].same = vecs[best[0]].item == v.item
		}
	}
	return hits
}

// selfRetrieval is the fraction of scorable views whose nearest neighbour is their own item.
func selfRetrieval(hits []hit, threshold float64) Metric {
	var scorable, same int
	for _, h := range hits {
		if h.scorable {
			scorable++
			if h.same {
				same++
			}
		}
	}
	if scorable == 0 {
		return skipped(MetricSelfRetrieval, threshold,
			"every sampled item has a single view, so no view can retrieve its item by another")
	}
	return ratio(MetricSelfRetrieval, same, scorable, threshold)
}

// pairStats holds the cosine distances of one pass over every vector pair: cross-item
// distances aggregated globally, same-item distances aggregated per item.
type pairStats struct {
	crossSum   float64
	crossPairs int
	itemSum    []float64
	itemPairs  []int
}

// pairwise walks every unordered pair once and splits the distances by whether the two
// vectors belong to the same item. Both distinctiveness metrics read from the result, so the
// quadratic pass happens once.
func pairwise(c corpus, vecs []vector) pairStats {
	p := pairStats{itemSum: make([]float64, len(c.items)), itemPairs: make([]int, len(c.items))}
	for i := range vecs {
		for j := i + 1; j < len(vecs); j++ {
			d := distance(vecs[i].v, vecs[j].v)
			if vecs[i].item == vecs[j].item {
				p.itemSum[vecs[i].item] += d
				p.itemPairs[vecs[i].item]++
				continue
			}
			p.crossSum += d
			p.crossPairs++
		}
	}
	return p
}

// distinctiveness is the mean cosine distance between views of different items. A collapsed
// embedding space — every text mapping to nearly the same vector — shows up here and nowhere
// else, because ranking metrics stay high while every score converges.
func distinctiveness(p pairStats, threshold float64) Metric {
	if p.crossPairs == 0 {
		return skipped(MetricDistinctiveness, threshold, "the sample has no pair of distinct items")
	}
	return mean(MetricDistinctiveness, p.crossSum/float64(p.crossPairs), p.crossPairs, threshold)
}

// viewDistinctiveness is the mean, over items, of the mean distance between that item's own
// views. Averaging per item first keeps an item with many views from dominating an item with
// few, which is what "between views of one item" means.
func viewDistinctiveness(p pairStats, threshold float64) Metric {
	var sum float64
	var items int
	for i, pairs := range p.itemPairs {
		if pairs == 0 {
			continue
		}
		sum += p.itemSum[i] / float64(pairs)
		items++
	}
	if items == 0 {
		return skipped(MetricViewDistinctiveness, threshold,
			"no sampled item has two views to compare")
	}
	return mean(MetricViewDistinctiveness, sum/float64(items), items, threshold)
}

// viewCoverage is the fraction of configured views that produced non-empty text across the
// sample. It is the one metric that reads the configured view list rather than the vectors:
// a view whose prompt returned nothing has no row in state, so only configuration knows it
// was expected.
func viewCoverage(c corpus, threshold float64) Metric {
	if len(c.views) == 0 {
		return skipped(MetricViewCoverage, threshold, "the datatype configures no views")
	}
	expected := len(c.items) * len(c.views)
	present := 0
	for _, it := range c.items {
		for _, name := range c.views {
			if it.Views[name] != "" {
				present++
			}
		}
	}
	return ratio(MetricViewCoverage, present, expected, threshold)
}

// metadataTop3 is the fraction of items a query built from their own metadata retrieves in
// the top three view vectors. It is the closest thing here to how a user searches: the query
// text never appears in any view, so unlike self-retrieval it cannot be satisfied by lexical
// overlap with the document.
func metadataTop3(c corpus, vecs []vector, queries [][]float32, threshold float64) Metric {
	var scorable, found int
	for i := range c.items {
		if queries[i] == nil {
			continue
		}
		scorable++
		for _, at := range topK(vecs, queries[i], -1, 3) {
			if vecs[at].item == i {
				found++
				break
			}
		}
	}
	if scorable == 0 {
		return skipped(MetricMetadataTop3, threshold,
			"no sampled item has metadata a query can be built from")
	}
	return ratio(MetricMetadataTop3, found, scorable, threshold)
}

// ratio builds a metric from a hit count over a sample size.
func ratio(name string, hits, of int, threshold float64) Metric {
	score := float64(hits) / float64(of)
	return Metric{
		Name:      name,
		Score:     round(score),
		Threshold: threshold,
		Sample:    of,
		Pass:      score >= threshold,
	}
}

// mean builds a metric from an averaged distance over a sample size.
func mean(name string, score float64, of int, threshold float64) Metric {
	return Metric{
		Name:      name,
		Threshold: threshold,
		Score:     round(score),
		Sample:    of,
		Pass:      score >= threshold,
	}
}

// skipped builds a metric that had no evidence. It neither passes nor fails: the datatype's
// status carries the fact that something was not scored.
func skipped(name string, threshold float64, reason string) Metric {
	return Metric{Name: name, Threshold: threshold, Skipped: reason}
}

// round trims a score to four decimals, which is more precision than any threshold uses and
// keeps the JSON report free of float noise. The comparison against the threshold happens
// before rounding, so a score is never reported as passing a bound it did not reach.
func round(v float64) float64 {
	return math.Round(v*1e4) / 1e4
}
