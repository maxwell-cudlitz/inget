// Drift measurement using normalised Levenshtein distance.
//
// Drift quantifies how much two strings differ on a 0.0–1.0 scale. It is used to decide
// whether a cached enrichment or view result should be regenerated even though the
// structural cache key hasn't changed — for example, when an upstream model produces
// subtly different output for the same input.
package delta

import (
	"github.com/agnivade/levenshtein"
)

// Drift returns the normalised Levenshtein distance between a and b: the edit distance
// divided by the length of the longer string. If both strings are empty, drift is 0.
func Drift(a, b string) float64 {
	if a == b {
		return 0.0
	}
	dist := levenshtein.ComputeDistance(a, b)
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 0.0
	}
	return float64(dist) / float64(maxLen)
}

// DriftExceedsThreshold returns true when the normalised Levenshtein distance between a
// and b is strictly greater than threshold.
func DriftExceedsThreshold(a, b string, threshold float64) bool {
	return Drift(a, b) > threshold
}
