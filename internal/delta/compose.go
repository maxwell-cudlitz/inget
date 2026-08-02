// Deterministic fragment composer.
//
// Compose concatenates enriched fragments into a single document for view input. The
// order is controlled by the caller ("tier" or "path"), each entry is labelled with its
// fragment key, and the result is truncated at a character limit. A SHA-256 hash of the
// composed string is returned alongside it, feeding the Level 2 cache key.
package delta

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// separator is placed between entries in the composed output.
	separator = "\n---\n"
	// keyPrefix labels each entry with the fragment key it came from.
	keyPrefix = "## "
)

// ComposeEntry is one fragment ready for composition.
type ComposeEntry struct {
	Key     string
	Tier    int
	Content string
}

// Composition is the result of composing one view's fragments.
type Composition struct {
	Text          string // the composed document, after any truncation
	Hash          string // SHA-256 of Text; contributes to the level-2 input hash
	Truncated     bool   // whether maxChars dropped content
	OriginalChars int    // rune count before truncation
}

// Compose concatenates entries in deterministic order and returns the composed document
// with its hash. The order parameter selects the sort:
//   - "tier": Tier ascending, then Key ascending within a tier.
//   - "path" (or any other value): Key ascending.
//
// Config validation restricts compose.order to those two values, so the fallback only
// applies to programmatic callers.
//
// Each entry is prefixed with "## <key>" so the model can attribute content to a path
// and so renaming a fragment changes the composed hash — content that moved is content
// whose views should regenerate.
//
// The document is truncated at maxChars runes; maxChars <= 0 disables truncation.
// Truncation is reported rather than silent: the caller records it on the item and logs
// it, because a view generated from a clipped document is a quality signal, not a
// detail.
func Compose(entries []ComposeEntry, order string, maxChars int) Composition {
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
		b.WriteString(keyPrefix)
		b.WriteString(e.Key)
		b.WriteString("\n")
		b.WriteString(e.Content)
	}

	text := b.String()
	c := Composition{OriginalChars: utf8.RuneCountInString(text)}

	if maxChars > 0 && c.OriginalChars > maxChars {
		text = string([]rune(text)[:maxChars])
		c.Truncated = true
	}

	digest := sha256.Sum256([]byte(text))
	c.Text = text
	c.Hash = "sha256:" + hex.EncodeToString(digest[:])
	return c
}
