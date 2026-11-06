// Explicit historical derivation reuse for cache-only repository view rebuilds.
//
// Every candidate retains the same file path and fingerprint. An allowed old signature changes
// only which already-paid derivation can be read; it never writes historical output under the
// current signature or enables file generation.
package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// validateCachedFragmentSignatures keeps historical reuse an explicit cache-only operation.
func validateCachedFragmentSignatures(rc RunConfig) error {
	if len(rc.CachedFragmentSignatures) > 0 && !rc.CachedFragmentsOnly {
		return fmt.Errorf("--cached-fragment-signatures requires --cached-fragments-only")
	}
	for _, sig := range rc.CachedFragmentSignatures {
		if strings.TrimSpace(sig) == "" {
			return fmt.Errorf("--cached-fragment-signatures contains an empty signature")
		}
	}
	return nil
}

// fragmentCacheKeys preserves current-first, then caller-defined historical preference.
func fragmentCacheKeys(frag artifact.Fragment, current string, allowed []string) []string {
	sigs := append([]string{current}, allowed...)
	seen := make(map[string]bool, len(sigs))
	keys := make([]string, 0, len(sigs))
	for _, sig := range sigs {
		key := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, sig)
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

// cachedFragmentKey returns the actual matching key for read-only preflight and estimation.
func cachedFragmentKey(ctx context.Context, store state.Store, frag artifact.Fragment, current string, allowed []string) (string, bool, error) {
	for _, key := range fragmentCacheKeys(frag, current, allowed) {
		found, err := store.HasDerivation(ctx, key)
		if err != nil {
			return "", false, fmt.Errorf("checking cached fragment %s: %w", frag.Key, err)
		}
		if found {
			return key, true, nil
		}
	}
	return "", false, nil
}

// readCachedFragment uses the same preference as planning and preflight, touching only the
// matching original entry's retention timestamp. It never relabels the cached derivation.
func readCachedFragment(ctx context.Context, ex *execution, frag artifact.Fragment) (string, bool, error) {
	var allowed []string
	if ex.cacheOnly {
		allowed = ex.cachedFragmentSignatures
	}
	for _, key := range fragmentCacheKeys(frag, ex.fragSig, allowed) {
		text, found, err := ex.deps.State.Derivation(ctx, key)
		if err != nil {
			return "", false, fmt.Errorf("reading cached fragment %s: %w", frag.Key, err)
		}
		if found {
			return text, true, nil
		}
	}
	return "", false, nil
}
