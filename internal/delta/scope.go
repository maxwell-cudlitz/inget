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

// ViewSkippable returns true when no changed key matches any glob in dependsOn,
// meaning the view's inputs are unchanged and it can skip reprocessing.
func ViewSkippable(fragmentKeys []string, changedKeys []string, dependsOn []string) bool {
	// Build a set of changed keys for fast lookup.
	changed := make(map[string]struct{}, len(changedKeys))
	for _, k := range changedKeys {
		changed[k] = struct{}{}
	}

	// Check whether any fragment key that matches the dependency globs is also changed.
	for _, key := range fragmentKeys {
		if _, isChanged := changed[key]; !isChanged {
			continue
		}
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
