// An explicit rebuild can revisit indexed items even before an interrupted initial
// ingestion has recorded global signatures; default incremental runs remain unchanged.
package pipeline

import (
	"context"
	"fmt"
	"sort"
)

func includeUnrecordedSignatures(ctx context.Context, deps Deps, changed []string) ([]string, error) {
	indexed, err := deps.State.ItemFingerprints(ctx, deps.Config.Name)
	if err != nil {
		return nil, fmt.Errorf("checking indexed items for signature rebuild: %w", err)
	}
	if len(indexed) == 0 {
		return changed, nil
	}
	for scope := range currentSignatures(deps) {
		previous, err := deps.State.Signature(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("reading signature for explicit rebuild %s: %w", scope, err)
		}
		if previous == "" {
			changed = append(changed, scope)
		}
	}
	sort.Strings(changed)
	return changed, nil
}
