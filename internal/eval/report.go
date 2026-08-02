// The report `inget eval` prints, and the verdict that decides its exit status.
//
// Reporting is per datatype and per view, never one aggregate figure: with the llm enricher
// the embedder sees clean prose and is rarely the bottleneck, while with passthrough it does
// the semantic work, so an average over both hides the case that needs attention (D14).
package eval

import "slices"

// Report statuses.
const (
	// StatusOK means every applicable metric reached its threshold.
	StatusOK = "ok"
	// StatusBreach means at least one applicable metric fell below its threshold.
	StatusBreach = "breach"
	// StatusUnscorable means the corpus could not be scored at all. It is not a pass.
	StatusUnscorable = "unscorable"
)

// Report is one datatype's evaluation.
//
// The three item counts are what separate a quality problem from a pipeline that has not
// finished: LiveItems is what state knows about, ViewedItems is how many of those have any
// stored view text, and SampledItems is how many of those this run scored.
type Report struct {
	Datatype     string       `json:"datatype"`
	Embedder     string       `json:"embedder"`
	Dims         int          `json:"dims"`
	Status       string       `json:"status"`
	Reason       string       `json:"reason,omitempty"`
	LiveItems    int          `json:"live_items"`
	ViewedItems  int          `json:"viewed_items"`
	SampledItems int          `json:"sampled_items"`
	ViewVectors  int          `json:"view_vectors"`
	Metrics      []Metric     `json:"metrics,omitempty"`
	Views        []ViewReport `json:"views,omitempty"`
}

// Passed reports whether the datatype cleared the gate. An unscorable corpus does not.
func (r Report) Passed() bool { return r.Status == StatusOK }

// Metric is one scored measure. Sample is the denominator the score was computed over, so a
// reader can tell 4 out of 5 from 800 out of 1000. Skipped, when set, explains why the metric
// had no evidence; such a metric is neither a pass nor a breach.
type Metric struct {
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Threshold float64 `json:"threshold"`
	Sample    int     `json:"sample"`
	Pass      bool    `json:"pass"`
	Skipped   string  `json:"skipped,omitempty"`
}

// Breached reports whether the metric was scored and fell short.
func (m Metric) Breached() bool { return m.Skipped == "" && !m.Pass }

// ViewReport is the per-view breakdown. The thresholds are not applied here: one weak view
// out of eight is a prompt to improve, not a reason to fail a datatype, and which of the two
// it is depends on the view. MeanChars is included because an unexpectedly short view is the
// usual first symptom of a prompt that has started refusing or truncating.
type ViewReport struct {
	Name          string  `json:"name"`
	Vectors       int     `json:"vectors"`
	Coverage      float64 `json:"coverage"`
	SelfRetrieval float64 `json:"self_retrieval"`
	MeanChars     int     `json:"mean_chars"`
}

// verdict turns the metric set into the datatype's status.
func verdict(metrics []Metric) string {
	if slices.ContainsFunc(metrics, Metric.Breached) {
		return StatusBreach
	}
	return StatusOK
}

// perView breaks coverage, nearest-neighbour outcome and text length down by view, in the
// order configuration declares them.
func perView(c corpus, vecs []vector, hits []hit) []ViewReport {
	reports := make([]ViewReport, 0, len(c.views))
	for _, name := range c.views {
		reports = append(reports, viewReport(c, vecs, hits, name))
	}
	return reports
}

// viewReport scores one view across the sample.
func viewReport(c corpus, vecs []vector, hits []hit, name string) ViewReport {
	r := ViewReport{Name: name}

	chars := 0
	for _, it := range c.items {
		if text := it.Views[name]; text != "" {
			r.Vectors++
			chars += len([]rune(text))
		}
	}
	if r.Vectors > 0 {
		r.MeanChars = chars / r.Vectors
		r.Coverage = round(float64(r.Vectors) / float64(len(c.items)))
	}

	var scorable, same int
	for i, v := range vecs {
		if v.view != name || !hits[i].scorable {
			continue
		}
		scorable++
		if hits[i].same {
			same++
		}
	}
	if scorable > 0 {
		r.SelfRetrieval = round(float64(same) / float64(scorable))
	}
	return r
}
