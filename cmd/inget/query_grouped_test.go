// Generic item grouping preserves evidence across views and datatype collisions.
// Bounded output truncates explicitly without turning too-large evidence into absence.
package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestQueryGroupingPreservesViewsAndDatatypeIdentity(t *testing.T) {
	grouped := groupQueryEvidence([]hit{
		{Datatype: "calendar/event", ItemID: "shared", ViewName: "purpose", Score: .9, Text: "event"},
		{Datatype: "source/item", ItemID: "shared", ViewName: "purpose", Score: .9, Text: "item"},
		{Datatype: "source/item", ItemID: "shared", ViewName: "workflow", Score: .8, Text: "workflow"},
		{Datatype: "source/item", ItemID: "shared", ViewName: "purpose", Score: .7, Text: "weak duplicate"},
		{Datatype: "source/item", ItemID: "second", ViewName: "purpose", Score: .6, Text: "second"},
	})
	if len(grouped) != 3 || grouped[0].Datatype != "calendar/event" || grouped[1].Datatype != "source/item" || grouped[1].Text != "item" {
		t.Fatalf("identity collision or nondeterministic representative: %+v", grouped)
	}
	if !reflect.DeepEqual(grouped[1].MatchedViews, []string{"purpose", "workflow"}) || len(grouped[1].Evidence) != 2 || grouped[1].Evidence[1].Text != "workflow" {
		t.Fatalf("contributing evidence was lost or duplicated: %+v", grouped[1])
	}
}

func TestGroupedQueryOutputHonorsByteBudgetAndTruthfulMetadata(t *testing.T) {
	groups := groupQueryEvidence([]hit{
		{Datatype: "source/item", ItemID: "first", ViewName: "purpose", Score: .9, Text: strings.Repeat("a", 1000)},
		{Datatype: "source/item", ItemID: "first", ViewName: "workflow", Score: .8, Text: strings.Repeat("b", 1000)},
		{Datatype: "source/item", ItemID: "second", ViewName: "purpose", Score: .7, Text: strings.Repeat("c", 1000)},
	})
	var out bytes.Buffer
	err := printGroupedQuery(&out, groupedQueryOutput{Results: groups, CandidateCount: 3, CandidateLimit: 3, CandidateLimitReached: true, RequestedLimit: 2}, 2800)
	if err != nil || out.Len() > 2800 {
		t.Fatalf("output limit ignored: bytes=%d err=%v", out.Len(), err)
	}
	var report groupedQueryOutput
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.OutputTruncated || report.ReturnedItems != 1 || report.CandidateCount != 3 || !report.CandidateLimitReached || !report.Results[0].EvidenceTruncated || len(report.Results[0].MatchedViews) != 2 || len(report.Results[0].Evidence) != 1 {
		t.Fatalf("truncated output hid retrieval/evidence limits: %+v", report)
	}
	out.Reset()
	if err := printGroupedQuery(&out, groupedQueryOutput{Results: groups[:1]}, 100); err == nil || out.Len() != 0 {
		t.Fatal("oversized first result became successful empty/partial stdout")
	}
}

func TestGroupedQueryEmptyAndLegacyJSONContracts(t *testing.T) {
	var out bytes.Buffer
	if err := printGroupedQuery(&out, groupedQueryOutput{RequestedLimit: 5, CandidateLimit: 35}, 1000); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report["results"] == nil || report["returned_items"] != float64(0) || report["output_truncated"] != false {
		t.Fatalf("empty grouped shape=%s err=%v", out.Bytes(), err)
	}
	data, err := json.Marshal(hit{Datatype: "source/item", ItemID: "old", ViewName: "purpose", Score: .4, Text: "legacy"})
	if err != nil || strings.Contains(string(data), "evidence") || strings.Contains(string(data), "matched_views") {
		t.Fatal("legacy ungrouped hit acquired new nonempty fields")
	}
}
