// Read-only cache preflight for rebuilding repository views from existing file summaries.
//
// A cache-only pass checks its complete selected work set before starting the artifact run.
// Exact fragment fingerprints and the current or explicitly allowed historical signature
// decide hits. The generation path also refuses misses, so concurrent
// cache eviction cannot turn a view rebuild into paid fragment summarization.
package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/maxwell-cudlitz/inget/internal/delta"
)

// preflightCachedFragments verifies every derivation processItem will need for this pass.
// It uses HasDerivation to avoid touching cache retention timestamps during a plan.
func preflightCachedFragments(ctx context.Context, deps Deps, result *reconcileResult, allowed []string) error {
	if !deps.Config.FragmentEnricher || deps.FragEnricher == nil {
		return nil // raw fragments are composed directly; no fragment generator exists
	}
	sig := deps.FragEnricher.Signature()
	seen := make(map[string]bool)
	missing := 0
	var examples []string
	for _, itemID := range result.plan.WorkItems {
		rec, ok := result.records[itemID]
		if !ok {
			return fmt.Errorf("preflighting cached summaries: selected item %s is absent from artifact %s",
				itemID, result.plan.ArtifactRunID)
		}
		for _, frag := range rec.Fragments {
			if frag.Blob == "" {
				continue // withheld or empty content never needs a derivation
			}
			key := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, sig)
			if seen[key] {
				continue
			}
			seen[key] = true
			_, hit, err := cachedFragmentKey(ctx, deps.State, frag, sig, allowed)
			if err != nil {
				return fmt.Errorf("preflighting cached summary for %s/%s fragment %s: %w",
					deps.Config.Name, itemID, frag.Key, err)
			}
			if !hit {
				missing++
				if len(examples) < 3 {
					examples = append(examples, itemID+":"+frag.Key)
				}
			}
		}
	}
	if missing > 0 {
		return fmt.Errorf("--cached-fragments-only: %d unique fragment summaries are missing for %s under the current fragment signature or explicitly allowed historical signatures; examples: %s; no model calls were made for this artifact run",
			missing, deps.Config.Name, strings.Join(examples, ", "))
	}
	return nil
}
