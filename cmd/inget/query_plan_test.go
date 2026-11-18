// Query-plan tests cover generic view selection, global bounds and unchanged
// legacy behavior without opening a destination or contacting any provider.
package main

import (
	"reflect"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/config"
)

func queryPlanDatatypes() []config.Datatype {
	return []config.Datatype{
		{Name: "source/item", Views: []config.View{{Name: "purpose"}, {Name: "workflow"}}},
		{Name: "calendar/event", Views: []config.View{{Name: "purpose"}, {Name: "workflow"}}},
	}
}

func TestSelectedViewsUseGlobalCandidateBudget(t *testing.T) {
	opts := queryOptions{views: []string{"workflow", "purpose"}, groupBy: "item", limit: 5, candidateLimit: 21}
	datatypes := queryPlanDatatypes()
	if err := validateQuerySelection(datatypes, opts); err != nil {
		t.Fatal(err)
	}
	plan, err := makeQueryPlan(datatypes, opts, opts.limit)
	if err != nil {
		t.Fatal(err)
	}
	var views, types []string
	total := 0
	for _, scope := range plan.scopes {
		views = append(views, scope.view)
		types = append(types, scope.datatype.Name)
		total += scope.limit
		if scope.limit < 1 {
			t.Fatal("candidate allocation silently excluded a selected scope")
		}
	}
	if total != 21 || plan.candidateLimit != 21 || !reflect.DeepEqual(views, []string{"workflow", "purpose", "workflow", "purpose"}) || !reflect.DeepEqual(types, []string{"source/item", "source/item", "calendar/event", "calendar/event"}) {
		t.Fatalf("plan is not generic/global: %+v views=%v types=%v", plan, views, types)
	}
}

func TestGroupedOmittedViewUsesOneSearchPerDatatype(t *testing.T) {
	plan, err := makeQueryPlan(queryPlanDatatypes(), queryOptions{groupBy: "item", limit: 5}, 5)
	if err != nil || len(plan.scopes) != 2 || plan.candidateLimit != 20 {
		t.Fatalf("grouped plan=%+v err=%v", plan, err)
	}
	for _, scope := range plan.scopes {
		if scope.view != "" {
			t.Fatal("omitted-view query was silently narrowed or expanded into per-view calls")
		}
	}
	legacy, err := makeQueryPlan(queryPlanDatatypes(), queryOptions{view: "purpose", limit: 5000}, 5000)
	if err != nil || legacy.candidateLimit != 0 || len(legacy.scopes) != 2 || legacy.scopes[0].limit != 5000 {
		t.Fatal("legacy query defaults acquired a new global limit")
	}
}

func TestInvalidQueryScopesFailBeforeDestinationOrEmbedding(t *testing.T) {
	datatype := queryPlanDatatypes()
	for _, opts := range []queryOptions{
		{view: "purpose", views: []string{"workflow"}},
		{views: []string{"missing"}}, {views: []string{"purpose", "purpose"}}, {views: []string{""}},
		{groupBy: "repository"}, {candidateLimit: -1}, {candidateLimit: 1001},
		{maxOutputBytes: -1}, {maxOutputBytes: 64<<20 + 1},
		{maxOutputBytes: 1000}, {groupBy: "item", maxOutputBytes: 1000},
	} {
		if err := validateQuerySelection(datatype, opts); err == nil {
			t.Errorf("accepted invalid query options: %+v", opts)
		}
	}
	for _, opts := range []queryOptions{
		{groupBy: "item", limit: 1001},
		{views: []string{"purpose", "workflow"}, limit: 5, candidateLimit: 3},
		{groupBy: "item", limit: 10, candidateLimit: 9},
	} {
		if _, err := makeQueryPlan(datatype, opts, opts.limit); err == nil {
			t.Errorf("accepted insufficient/unbounded candidate budget: %+v", opts)
		}
	}
	datatype[1].Views = []config.View{{Name: "purpose"}}
	if err := validateQuerySelection(datatype, queryOptions{views: []string{"workflow"}}); err == nil {
		t.Fatal("a later datatype missing the view was not rejected")
	}
}

func TestAutomaticQueryBudgetClampsBeforeMultiplication(t *testing.T) {
	plan, err := makeQueryPlan(queryPlanDatatypes(), queryOptions{groupBy: "item", limit: 1000}, int(^uint(0)>>1))
	if err != nil || plan.candidateLimit != maxQueryCandidates {
		t.Fatalf("large configured retrieval pool overflowed automatic budget: %+v %v", plan, err)
	}
}
