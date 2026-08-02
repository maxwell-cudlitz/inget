// Construction of the metadata-derived query for the top-3 retrieval metric.
//
// The query has to look like something a person would type, and it has to be built without
// knowing which source produced the item: metadata is a flat string map whose keys differ per
// connector, so no field name can be hard-coded here. What is generic is which *values* carry
// meaning — a description and a topic list do, a URL, a timestamp and a numeric ID do not —
// so the filter is by value shape rather than by key name.
//
// Keys are excluded from the query text. They are identical across every item of a datatype,
// so including them would add the same tokens to every query and dilute the part that
// discriminates.
package eval

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxQueryChars bounds the query. Metadata is small by construction, but an item carrying a
// whole description as one field should not produce a query longer than the views it is
// searching for.
const maxQueryChars = 2000

// metadataQuery renders an item's metadata as one query string, or "" when nothing in it is
// semantically useful. An empty result is reported as an unscored item rather than as a miss:
// the harness cannot conclude anything about retrieval from a query it could not build.
func metadataQuery(metadata map[string]string) string {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var (
		parts []string
		seen  = map[string]bool{}
		room  = maxQueryChars
	)
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if !semantic(value) || seen[value] {
			continue
		}
		seen[value] = true

		// An oversized value is clipped rather than dropped: a long description is the most
		// informative field an item has, and losing it entirely would change what is being
		// measured more than losing its tail does.
		clipped := clip(value, room)
		if !semantic(clipped) {
			break
		}
		parts = append(parts, clipped)
		if room -= len([]rune(clipped)); room <= 0 {
			break
		}
	}
	return strings.Join(parts, ", ")
}

// clip truncates a value to at most max runes, never splitting one.
func clip(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// semantic reports whether a metadata value contributes meaning to a query.
//
// The four exclusions are the shapes that appear in every connector's metadata and describe
// where or when a record lives rather than what it is about: a URL, a timestamp, a bare
// number, and a single character.
func semantic(value string) bool {
	switch {
	case len([]rune(value)) < 2:
		return false
	case strings.Contains(value, "://"):
		return false
	case isTimestamp(value):
		return false
	case isNumber(value):
		return false
	}
	return true
}

// isTimestamp reports whether the value is a machine timestamp in one of the two layouts
// connectors emit.
func isTimestamp(value string) bool {
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// isNumber reports whether the value is a bare number, which is what a record ID looks like.
func isNumber(value string) bool {
	_, err := strconv.ParseFloat(value, 64)
	return err == nil
}
