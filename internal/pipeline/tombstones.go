// Tombstone application shared by ordinary and cache-only enrichment passes.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
)

// processTombstones deletes tombstoned items from state and destinations.
func processTombstones(ctx context.Context, deps Deps, tombstones []string, stats *statsCollector) error {
	if len(tombstones) == 0 {
		return nil
	}
	slog.InfoContext(ctx, "processing tombstones", "count", len(tombstones))

	if err := deps.State.Tombstone(ctx, deps.Config.Name, tombstones); err != nil {
		return fmt.Errorf("tombstoning items: %w", err)
	}
	for _, destName := range deps.Config.Destinations {
		dest, ok := deps.Destinations[destName]
		if !ok {
			return fmt.Errorf("destination %q not found in deps", destName)
		}
		for _, itemID := range tombstones {
			if err := dest.DeleteItem(ctx, deps.Config.Name, itemID); err != nil {
				return fmt.Errorf("deleting %s from %s: %w", itemID, destName, err)
			}
		}
	}
	stats.addTombstones(len(tombstones))
	return nil
}
