// Recorded signatures, which is how a mass invalidation is detected before it happens.
//
// A scope is a string like "enricher:github/repo:file". Comparing the stored signature with
// the current one tells `inget plan` that a prompt or model change is about to regenerate
// every item, so the cost is reported before anything is spent (D2).
package state

import (
	"context"
	"fmt"
)

const (
	signatureSelectSQL = `SELECT signature FROM signatures WHERE scope = ?`

	signatureUpsertSQL = `
INSERT INTO signatures (scope, signature) VALUES (?, ?)
ON CONFLICT (scope) DO UPDATE SET
    signature  = excluded.signature,
    updated_at = CURRENT_TIMESTAMP`
)

// Signature implements Store.
func (s *store) Signature(ctx context.Context, scope string) (string, error) {
	var signature string
	found, err := s.get(ctx, signatureSelectSQL, []any{scope}, &signature)
	if err != nil {
		return "", fmt.Errorf("reading signature of scope %s: %w", scope, err)
	}
	if !found {
		return "", nil
	}
	return signature, nil
}

// PutSignature implements Store.
func (s *store) PutSignature(ctx context.Context, scope, signature string) error {
	if scope == "" || signature == "" {
		return fmt.Errorf("signature needs a scope and a value, got %q and %q", scope, signature)
	}
	if _, err := s.exec(ctx, signatureUpsertSQL, scope, signature); err != nil {
		return fmt.Errorf("writing signature of scope %s: %w", scope, err)
	}
	return nil
}
