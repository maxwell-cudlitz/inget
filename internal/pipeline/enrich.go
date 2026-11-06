// Stage 4: per-fragment derivation, and the compose inputs it produces.
//
// Derivation is the expensive stage — one generator call per changed fragment — so it runs
// concurrently, bounded by the run's shared generator permits rather than by a per-item
// limit that would multiply with the item pool.
//
// There is no explicit check for which delta category a fragment falls in. The level-1
// cache key is the fragment key, its fingerprint and the enricher signature, so an
// unchanged fragment hits the cache and a changed one cannot: the category is implied by
// the key, and testing it separately would be a second source of truth about the same
// question.
package pipeline

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// enrichFragments returns the content each fragment contributes to composition, keyed by
// fragment key: the derived text when fragment enrichment is enabled, the raw blob when it
// is not.
func enrichFragments(ctx context.Context, ex *execution, rec *artifact.Record) (map[string]string, error) {
	var (
		mu       sync.Mutex
		enriched = make(map[string]string, len(rec.Fragments))
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(ex.concurrency)
	for _, frag := range rec.Fragments {
		g.Go(func() error {
			content, err := deriveFragment(gctx, ex, rec.ItemID, frag)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			enriched[frag.Key] = content
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("deriving %d fragments of %s/%s: %w",
			len(rec.Fragments), ex.deps.Config.Name, rec.ItemID, err)
	}
	return enriched, nil
}

// deriveFragment resolves one fragment to the text that represents it, consulting the
// derivation cache before reading the blob and before spending a generator call.
func deriveFragment(ctx context.Context, ex *execution, itemID string, frag artifact.Fragment) (string, error) {
	if !ex.fragmentEnrichment() {
		return loadFragmentContent(ctx, ex.arts, frag)
	}
	if frag.Blob == "" {
		// Nothing was stored for it: a detected secret, a file over the split budget, or an
		// empty file. Deriving it would pay for a prompt whose data section is blank.
		return "", nil
	}

	cacheKey := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, ex.fragSig)
	cached, found, err := readCachedFragment(ctx, ex, frag)
	if err != nil {
		return "", fmt.Errorf("reading the derivation cache for fragment %s: %w", frag.Key, err)
	}
	if found {
		return cached, nil
	}

	// Collapse concurrent derivations of the same fragment. Two items in one run holding the
	// same file at the same path is ordinary — a shared LICENSE, an identical CI workflow, a
	// vendored dependency — and the persisted cache cannot help while both are in flight, so
	// without this each of them pays for the other's tokens.
	derived, err, _ := ex.derive.Do(cacheKey, func() (any, error) {
		// The first miss may predate another worker's completed derivation. A completed
		// singleflight call is forgotten, so recheck durable state before spending again.
		cached, found, err := readCachedFragment(ctx, ex, frag)
		if err != nil {
			return nil, fmt.Errorf("rechecking the derivation cache: %w", err)
		}
		if found {
			return cached, nil
		}
		return generateDerivation(ctx, ex, itemID, frag, cacheKey)
	})
	if err != nil {
		return "", fmt.Errorf("fragment %s: %w", frag.Key, err)
	}
	text, ok := derived.(string)
	if !ok {
		return "", fmt.Errorf("fragment %s: derivation returned %T", frag.Key, derived)
	}
	return text, nil
}

// generateDerivation reads a fragment's content, derives it, and caches the result.
func generateDerivation(ctx context.Context, ex *execution, itemID string, frag artifact.Fragment, cacheKey string) (string, error) {
	if ex.cacheOnly {
		return "", fmt.Errorf("--cached-fragments-only: matching cached summary is missing for %s/%s fragment %s; fragment generation is disabled",
			ex.deps.Config.Name, itemID, frag.Key)
	}
	cfg := ex.deps.Config
	content, err := loadFragmentContent(ctx, ex.arts, frag)
	if err != nil {
		return "", err
	}

	if err := ex.gen.acquire(ctx); err != nil {
		return "", err
	}
	ingestDetail(ctx, cfg.Name, itemID, "enrichment", frag.Key)
	derived, err := ex.deps.FragEnricher.Enrich(ctx, enrich.FragmentTemplateData{
		Key:     frag.Key,
		Content: content,
	})
	ex.gen.release()
	if err != nil {
		return "", err
	}
	ex.stats.addFragmentsEnriched(1)
	ingestProgress(ctx, cfg.Name, "count", itemID, "fragments", 0, 1)

	deriv := state.Derivation{
		CacheKey:  cacheKey,
		Datatype:  cfg.Name,
		ItemID:    itemID,
		FragKey:   frag.Key,
		Signature: ex.fragSig,
		Output:    derived,
	}
	if err := ex.deps.State.PutDerivation(ctx, deriv); err != nil {
		return "", fmt.Errorf("caching the derivation: %w", err)
	}
	return derived, nil
}

// loadFragmentContent fetches raw fragment content from the blob store.
func loadFragmentContent(ctx context.Context, arts *artifact.Store, frag artifact.Fragment) (string, error) {
	if frag.Blob == "" {
		return "", nil // truncated or empty fragment
	}
	data, err := arts.GetBlob(ctx, frag.Blob)
	if err != nil {
		return "", fmt.Errorf("loading blob for fragment %s: %w", frag.Key, err)
	}
	return string(data), nil
}

// buildComposeEntries assembles composition inputs, dropping fragments that contributed no
// text. An empty entry would still print its "## key" heading, which is a heading that
// promises content the document does not have.
func buildComposeEntries(rec *artifact.Record, enriched map[string]string) []delta.ComposeEntry {
	entries := make([]delta.ComposeEntry, 0, len(rec.Fragments))
	for _, frag := range rec.Fragments {
		content := enriched[frag.Key]
		if content == "" {
			continue
		}
		entries = append(entries, delta.ComposeEntry{
			Key:     frag.Key,
			Tier:    frag.Tier,
			Content: content,
		})
	}
	return entries
}

// buildFragmentStates converts artifact fragments to state fragment records.
func buildFragmentStates(rec *artifact.Record) []state.FragmentState {
	states := make([]state.FragmentState, 0, len(rec.Fragments))
	for _, frag := range rec.Fragments {
		states = append(states, state.FragmentState{
			Key:         frag.Key,
			Fingerprint: frag.Fingerprint,
			BlobRef:     frag.Blob,
			SizeBytes:   frag.Bytes,
			Tier:        frag.Tier,
		})
	}
	return states
}
