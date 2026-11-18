// Grouped query output retains matched-view evidence independently of optional
// LLM reranking. It is generic over datatype/item identity and declared views.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// hit is a native view result plus datatype. Grouping adds evidence fields;
// legacy ungrouped JSON omits them and retains its original array contract.
type hit struct {
	Datatype          string            `json:"datatype"`
	ItemID            string            `json:"item_id"`
	ViewName          string            `json:"view"`
	Score             float64           `json:"score"`
	Text              string            `json:"text"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	Rank              int               `json:"rank,omitempty"`
	MatchedViews      []string          `json:"matched_views,omitempty"`
	Evidence          []queryEvidence   `json:"evidence,omitempty"`
	EvidenceTruncated bool              `json:"evidence_truncated,omitempty"`
}

type queryEvidence struct {
	View  string  `json:"view"`
	Score float64 `json:"score"`
	Text  string  `json:"text"`
}

type groupedQueryOutput struct {
	Results               []hit `json:"results"`
	CandidateCount        int   `json:"candidate_count"`
	CandidateLimit        int   `json:"candidate_limit"`
	CandidateLimitReached bool  `json:"candidate_limit_reached"`
	RequestedLimit        int   `json:"requested_limit"`
	ReturnedItems         int   `json:"returned_items"`
	OutputTruncated       bool  `json:"output_truncated"`
}

func groupQueryEvidence(hits []hit) []hit {
	type itemKey struct{ datatype, id string }
	byItem := make(map[itemKey]map[string]hit)
	for _, h := range hits {
		key := itemKey{h.Datatype, h.ItemID}
		views := byItem[key]
		if views == nil {
			views = make(map[string]hit)
			byItem[key] = views
		}
		current, exists := views[h.ViewName]
		if !exists || h.Score > current.Score || h.Score == current.Score && h.Text < current.Text {
			views[h.ViewName] = h
		}
	}
	grouped := make([]hit, 0, len(byItem))
	for _, views := range byItem {
		ordered := make([]hit, 0, len(views))
		for _, h := range views {
			ordered = append(ordered, h)
		}
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].Score != ordered[j].Score {
				return ordered[i].Score > ordered[j].Score
			}
			return ordered[i].ViewName < ordered[j].ViewName
		})
		best := ordered[0]
		best.MatchedViews = make([]string, 0, len(ordered))
		best.Evidence = make([]queryEvidence, 0, len(ordered))
		for _, h := range ordered {
			best.MatchedViews = append(best.MatchedViews, h.ViewName)
			best.Evidence = append(best.Evidence, queryEvidence{View: h.ViewName, Score: h.Score, Text: h.Text})
		}
		grouped = append(grouped, best)
	}
	sort.Slice(grouped, func(i, j int) bool {
		if grouped[i].Score != grouped[j].Score {
			return grouped[i].Score > grouped[j].Score
		}
		if grouped[i].Datatype != grouped[j].Datatype {
			return grouped[i].Datatype < grouped[j].Datatype
		}
		return grouped[i].ItemID < grouped[j].ItemID
	})
	return grouped
}

func printGroupedQuery(out io.Writer, report groupedQueryOutput, maxBytes int) error {
	if report.Results == nil {
		report.Results = []hit{}
	}
	for {
		report.ReturnedItems = len(report.Results)
		data, err := json.Marshal(report)
		if err != nil {
			return fmt.Errorf("marshaling grouped query: %w", err)
		}
		if maxBytes == 0 || len(data) < maxBytes { // Include the final newline.
			if _, err := fmt.Fprintln(out, string(data)); err != nil {
				return fmt.Errorf("writing grouped query: %w", err)
			}
			return nil
		}
		if len(report.Results) == 0 {
			return fmt.Errorf("grouped query metadata exceeds --max-output-bytes")
		}
		report.OutputTruncated = true
		last := len(report.Results) - 1
		if len(report.Results[last].Evidence) > 1 {
			report.Results[last].Evidence = report.Results[last].Evidence[:len(report.Results[last].Evidence)-1]
			report.Results[last].EvidenceTruncated = true
			continue
		}
		if len(report.Results) == 1 {
			return fmt.Errorf("first grouped result exceeds --max-output-bytes")
		}
		report.Results = report.Results[:last]
	}
}
