// Snapshot the successful checkpoint IDs that an explicit index rebuild may revisit.
package pipeline

import (
	"context"
	"fmt"
)

// loadIndexedSelection fixes the allowed IDs before a plan or run selects work. Item
// fingerprints are written only after generation, embedding and destination upsert succeed,
// so cached file summaries alone do not qualify an item as previously indexed.
func loadIndexedSelection(ctx context.Context, deps Deps, rc *RunConfig) error {
	if !rc.IndexedOnly {
		return nil
	}
	fingerprints, err := deps.State.ItemFingerprints(ctx, deps.Config.Name)
	if err != nil {
		return fmt.Errorf("loading previously indexed %s items: %w", deps.Config.Name, err)
	}
	rc.indexedItems = fingerprints
	return nil
}
