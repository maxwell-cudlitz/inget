// Comparison of PostgreSQL extension version strings.
//
// pg_extension.extversion is whatever the extension's control file says — "0.8.0", "0.8",
// occasionally a suffix like "0.8.0-dev". Comparing those as strings puts "0.10" before
// "0.8", which is the wrong answer for exactly the check that needs it, so the numeric
// prefix of each dot-separated part is compared instead.
package destination

import "strings"

// compareVersions returns -1, 0 or 1 as a orders before, equal to, or after b. Parts that
// are absent count as zero, so "0.8" equals "0.8.0", and a part with no leading digits
// counts as zero rather than failing: a version this cannot parse should not be the reason
// a run does not start.
func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := versionPart(left, i), versionPart(right, i)
		if l != r {
			if l < r {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionPart reads the leading integer of one dot-separated part.
func versionPart(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n := 0
	for _, c := range parts[i] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
