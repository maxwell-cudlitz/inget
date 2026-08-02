// Page handling for the two shapes GitHub returns: a bare array from a repository listing,
// and an array wrapped in items from the search API.
//
// Both go through page, so the domain filters, the --since cutoff and the --limit counter are
// applied in exactly one place. --since is a stopping condition on a sorted listing and a
// filter on an unsorted one, which is the only difference between them.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// errListingDone ends one listing without ending enumeration. A sorted listing that has
// reached the --since cutoff has nothing newer left, but the next organisation might.
var errListingDone = errors.New("listing exhausted")

// paginateRepos walks a bare-array repository listing, which the API returns sorted by push
// time descending because orgRef asks it to.
func (e *enumerator) paginateRepos(ctx context.Context, ref string) error {
	err := e.conn.client.paginate(ctx, ref, func(body []byte) error {
		var page []repo
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decoding repository page: %w", err)
		}
		return e.page(ctx, page, true)
	})
	return endListing(err)
}

// paginateSearch walks a search result. The search API does not accept the sort this
// connector would need, so --since filters rather than stops.
func (e *enumerator) paginateSearch(ctx context.Context, ref string) error {
	err := e.conn.client.paginate(ctx, ref, func(body []byte) error {
		var page struct {
			Items []repo `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decoding search page: %w", err)
		}
		return e.page(ctx, page.Items, false)
	})
	return endListing(err)
}

// page filters and emits one page of repositories. sorted says the listing is ordered by push
// time descending, in which case the first repository older than the --since cutoff ends the
// listing rather than merely being skipped.
func (e *enumerator) page(ctx context.Context, repos []repo, sorted bool) error {
	for _, r := range repos {
		if e.stale(r) {
			if sorted {
				return errListingDone
			}
			continue
		}
		if err := e.filterAndEmit(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// endListing swallows the sentinel that ends one listing and passes everything else through,
// including source.ErrStopList, which ends enumeration entirely.
func endListing(err error) error {
	if errors.Is(err, errListingDone) {
		return nil
	}
	return err
}
