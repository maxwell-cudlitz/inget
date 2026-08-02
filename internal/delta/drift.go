// Drift measurement using normalised Levenshtein distance.
//
// Drift quantifies how much two strings differ on a 0.0–1.0 scale. It is used to decide
// whether a cached enrichment or view result should be regenerated even though the
// structural cache key hasn't changed — for example, when an upstream model produces
// subtly different output for the same input.
package delta

import (
	"unicode/utf8"

	"github.com/agnivade/levenshtein"
)

// Drift returns the normalised Levenshtein distance between a and b: the edit distance
// divided by the rune length of the longer string. If both strings are empty, drift is 0.
//
// The denominator counts runes rather than bytes because ComputeDistance measures its
// distance in runes. Dividing a rune distance by a byte length understates drift for
// every non-ASCII string — a one-character change in CJK text would score a third of its
// true value — which silently suppresses the re-embed that drift_threshold exists to
// trigger (D4).
func Drift(a, b string) float64 {
	if a == b {
		return 0.0
	}
	longest := max(utf8.RuneCountInString(a), utf8.RuneCountInString(b))
	if longest == 0 {
		return 0.0
	}
	return float64(levenshtein.ComputeDistance(a, b)) / float64(longest)
}

// DriftExceedsThreshold returns true when the normalised Levenshtein distance between a
// and b is strictly greater than threshold. Drift equal to the threshold does not exceed
// it: the operator chose that value as the bound of acceptable change.
func DriftExceedsThreshold(a, b string, threshold float64) bool {
	return Drift(a, b) > threshold
}
