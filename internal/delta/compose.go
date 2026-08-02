// Deterministic fragment composer.
//
// Compose concatenates enriched fragments into a single string for view input. The order
// is controlled by the caller ("tier" or "path"), and the result is truncated at a
// character limit. A SHA-256 hash of the composed string is returned alongside it, used
// as part of the Level 2 cache key derivation.
package delta

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// separator is placed between entries in the composed output.
const separator = "\n---\n"

// ComposeEntry is one fragment ready for composition.
type ComposeEntry struct {
	Key     string
	Tier    int
	Content string
}

// Compose concatenates entries in deterministic order and returns the composed text with
// its SHA-256 hash. The order parameter selects the sort:
//   - "tier": sort by Tier ascending, then Key ascending within the same tier.
//   - "path" (or any other value): sort by Key ascending.
//
// The result is truncated at maxChars characters (rune count). If maxChars <= 0, no
// truncation is applied.
func Compose(entries []ComposeEntry, order string, maxChars int) (composed string, hash string) {
	sorted := make([]ComposeEntry, len(entries))
	copy(sorted, entries)

	switch order {
	case "tier":
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].Tier != sorted[j].Tier {
				return sorted[i].Tier < sorted[j].Tier
			}
			return sorted[i].Key < sorted[j].Key
		})
	default:
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Key < sorted[j].Key
		})
	}

	var b strings.Builder
	for i, e := range sorted {
		if i > 0 {
			b.WriteString(separator)
		}
		b.WriteString(e.Content)
	}

	composed = b.String()

	if maxChars > 0 {
		runes := []rune(composed)
		if len(runes) > maxChars {
			composed = string(runes[:maxChars])
		}
	}

	digest := sha256.Sum256([]byte(composed))
	hash = "sha256:" + hex.EncodeToString(digest[:])
	return composed, hash
}
