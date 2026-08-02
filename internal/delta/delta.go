// Package delta implements the invalidation cascade engine.
//
// When new artifact data arrives, the delta engine determines which fragments changed,
// which remained the same, and which were removed. It then derives cache keys for the
// three processing levels (fragment enrichment, view composition, and embedding) so that
// downstream stages can skip work whose inputs haven't changed.
//
// The engine also provides drift measurement (normalised Levenshtein distance) to detect
// when regenerating a cached result might be worthwhile even though inputs haven't
// structurally changed, and glob-based scoping to limit view recomputation to only the
// views whose dependency patterns match changed fragments.
package delta
