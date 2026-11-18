// Query plans allocate a bounded candidate pool across selected datatypes/views.
// Every scope shares the one query embedding created by runQuery.
package main

import (
	"fmt"

	"github.com/maxwell-cudlitz/inget/internal/config"
)

const maxQueryCandidates = 1000

type queryScope struct {
	datatype config.Datatype
	view     string
	limit    int
}

type queryPlan struct {
	scopes         []queryScope
	candidateLimit int
	limitReached   bool
}

func validateQueryViews(datatypes []config.Datatype, opts queryOptions) error {
	if opts.view != "" && len(opts.views) != 0 {
		return fmt.Errorf("--view and --views are mutually exclusive")
	}
	seen := map[string]bool{}
	for _, view := range opts.views {
		if view == "" || seen[view] {
			return fmt.Errorf("--views must contain distinct nonempty declared view names")
		}
		seen[view] = true
	}
	for _, dt := range datatypes {
		if err := checkQueryView(dt, opts.view); err != nil {
			return err
		}
		for _, view := range opts.views {
			if err := checkQueryView(dt, view); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateQuerySelection(datatypes []config.Datatype, opts queryOptions) error {
	if err := validateQueryViews(datatypes, opts); err != nil {
		return err
	}
	if opts.groupBy != "" && opts.groupBy != "item" {
		return fmt.Errorf("--group-by = %q, want item", opts.groupBy)
	}
	if opts.candidateLimit < 0 || opts.candidateLimit > maxQueryCandidates {
		return fmt.Errorf("--candidate-limit must be between 0 (automatic) and %d", maxQueryCandidates)
	}
	if opts.maxOutputBytes < 0 || opts.maxOutputBytes > 64<<20 {
		return fmt.Errorf("--max-output-bytes must be between 0 and %d", 64<<20)
	}
	if opts.maxOutputBytes > 0 && (opts.groupBy != "item" || !opts.asJSON) {
		return fmt.Errorf("--max-output-bytes requires --group-by item and --json")
	}
	return nil
}

func makeQueryPlan(datatypes []config.Datatype, opts queryOptions, retrievalLimit int) (queryPlan, error) {
	plan := queryPlan{}
	multiplier := 0
	for _, dt := range datatypes {
		views := opts.views
		if len(views) == 0 {
			views = []string{opts.view}
		}
		for _, view := range views {
			plan.scopes = append(plan.scopes, queryScope{datatype: dt, view: view, limit: retrievalLimit})
		}
		if opts.view != "" {
			multiplier++
		} else if len(opts.views) > 0 {
			multiplier += len(opts.views)
		} else {
			multiplier += max(1, len(viewNames(dt)))
		}
	}
	bounded := opts.groupBy != "" || len(opts.views) > 0 || opts.candidateLimit > 0
	if !bounded {
		return plan, nil // Preserve the original view-level CLI behavior.
	}
	if opts.limit > maxQueryCandidates || len(plan.scopes) > maxQueryCandidates {
		return queryPlan{}, fmt.Errorf("bounded queries support at most %d results and datatype/view scopes", maxQueryCandidates)
	}
	budget := opts.candidateLimit
	if budget == 0 {
		// Clamp before multiplication, so a large operator limit cannot overflow.
		budget = maxQueryCandidates
		if retrievalLimit > 0 && multiplier > 0 && retrievalLimit <= maxQueryCandidates/multiplier {
			budget = retrievalLimit * multiplier
		}
	}
	if budget < opts.limit || budget < len(plan.scopes) {
		return queryPlan{}, fmt.Errorf("candidate budget %d is smaller than --limit or the selected datatype/view scope count", budget)
	}
	plan.candidateLimit = budget
	for i := range plan.scopes {
		plan.scopes[i].limit = budget / len(plan.scopes)
		if i < budget%len(plan.scopes) {
			plan.scopes[i].limit++
		}
	}
	return plan, nil
}
