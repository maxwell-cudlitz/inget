// Cache key derivation for the three processing levels.
//
// Each level's key is a content-addressable hash of its inputs:
//
//   - Level 1 (fragment enrichment): hash of the fragment fingerprint and enricher signature.
//   - Level 2 (view composition): hash of the scoped composed hash and the view prompt signature.
//   - Level 3 (embedding): hash of the actual view text.
//
// Hashing combines inputs by concatenation with a NUL separator, which is safe because
// SHA-256 inputs on both sides are hex-encoded (no NUL bytes possible).
package delta

import (
	"crypto/sha256"
	"encoding/hex"
)

// FragmentCacheKey derives the Level 1 cache key from a fragment's fingerprint and the
// enricher signature under which it would be processed.
func FragmentCacheKey(fragmentFingerprint, enricherSignature string) string {
	return hashWithDomain("L1", fragmentFingerprint, enricherSignature)
}

// ViewInputHash derives the Level 2 cache key from the scoped composed hash (the hash
// of the concatenated enriched fragments in scope) and the view prompt signature.
func ViewInputHash(scopedComposedHash, viewPromptSignature string) string {
	return hashWithDomain("L2", scopedComposedHash, viewPromptSignature)
}

// EmbeddingHash derives the Level 3 cache key from the actual view text that would be
// embedded.
func EmbeddingHash(viewText string) string {
	return hashWithDomain("L3", viewText)
}

// hashWithDomain hashes a domain tag followed by the given parts, separated by NUL
// bytes. The domain tag ensures that different levels never collide even when given
// identical input strings.
func hashWithDomain(domain string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
