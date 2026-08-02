// Glob-based scoping for fragment dependency matching.
//
// Views declare a dependsOn list of glob patterns. The scope functions determine which
// fragment keys match those patterns and whether a view can skip reprocessing because
// none of its dependencies changed.
package delta

import (
	"github.com/bmatcuk/doublestar/v4"
)

// MatchingFragments returns the subset of fragmentKeys that match any glob pattern in
// dependsOn. Patterns use doublestar syntax: "**" matches across path separators.
func MatchingFragments(fragmentKeys []string, dependsOn []string) []string {
	var matched []string
	for _, key := range fragmentKeys {
		if matchesAny(key, dependsOn) {
			matched = append(matched, key)
		}
	}
	return matched
}

// ViewSkippable reports whether a view can skip reprocessing: true when none of the
// changed fragment keys matches any glob in dependsOn.
//
// changedKeys must carry deleted keys as well as added and modified ones. The changed
// set is tested directly rather than filtered against the current fragment set, because
// a deleted key is by definition absent from that set: filtering would report a view
// whose only changed dependency was deleted as skippable and leave it stale forever.
func ViewSkippable(changedKeys []string, dependsOn []string) bool {
	for _, key := range changedKeys {
		if matchesAny(key, dependsOn) {
			return false
		}
	}
	return true
}

// matchesAny returns true when key matches at least one of the patterns.
func matchesAny(key string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := doublestar.Match(p, key); ok {
			return true
		}
	}
	return false
}
