// The per-fragment derivation cache: the level-1 guard's payload.
//
// A cache key is a hash over the fragment fingerprint and the enricher signature, so a
// changed fragment and a changed prompt are both misses (D2). This is the table that turns
// "one file changed in a 5000-file repository" into one LLM call instead of 5000.
package state

import (
	"context"
	"fmt"
)

const (
	// Reading and touching in one statement: the alternative is a SELECT plus an UPDATE
	// per cache hit, which doubles the round trips on the hottest path in the pipeline.
	derivationHitSQL = `
UPDATE derivations SET last_hit_at = CURRENT_TIMESTAMP WHERE cache_key = ? RETURNING output`

	derivationUpsertSQL = `
INSERT INTO derivations (cache_key, datatype, item_id, frag_key, signature, output)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (cache_key) DO UPDATE SET
    datatype    = excluded.datatype,
    item_id     = excluded.item_id,
    frag_key    = excluded.frag_key,
    signature   = excluded.signature,
    output      = excluded.output,
    last_hit_at = CURRENT_TIMESTAMP`
)

// Derivation implements Store.
func (s *store) Derivation(ctx context.Context, cacheKey string) (string, bool, error) {
	var output string
	found, err := s.get(ctx, derivationHitSQL, []any{cacheKey}, &output)
	if err != nil {
		return "", false, fmt.Errorf("reading derivation %s: %w", cacheKey, err)
	}
	return output, found, nil
}

// PutDerivation implements Store.
func (s *store) PutDerivation(ctx context.Context, d Derivation) error {
	if d.CacheKey == "" {
		return fmt.Errorf("derivation of %s/%s fragment %s has no cache key", d.Datatype, d.ItemID, d.FragKey)
	}
	_, err := s.exec(ctx, derivationUpsertSQL,
		d.CacheKey, d.Datatype, d.ItemID, d.FragKey, d.Signature, d.Output)
	if err != nil {
		return fmt.Errorf("writing derivation %s: %w", d.CacheKey, err)
	}
	return nil
}
