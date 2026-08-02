// Unit tests for the metadata-derived query.
package eval

import (
	"strings"
	"testing"
)

func TestMetadataQuery(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]string
		want     string
	}{
		{
			// Sorted by key: description, language, name, topics.
			name: "values join in key order, keys are not included",
			metadata: map[string]string{
				"name":        "inget",
				"description": "Enriches repositories into vectors",
				"topics":      "go,cli",
				"language":    "Go",
			},
			want: "Enriches repositories into vectors, Go, inget, go,cli",
		},
		{
			name:     "a url says where a record lives, not what it is about",
			metadata: map[string]string{"description": "a tool", "url": "https://github.com/acme/thing"},
			want:     "a tool",
		},
		{
			name:     "timestamps are excluded",
			metadata: map[string]string{"name": "thing", "updated_at": "2026-08-01T12:00:00Z", "created": "2026-07-04"},
			want:     "thing",
		},
		{
			name:     "bare numbers are record ids",
			metadata: map[string]string{"board_id": "1234567890", "name": "roadmap", "priority": "3.5"},
			want:     "roadmap",
		},
		{
			name:     "single characters and blanks contribute nothing",
			metadata: map[string]string{"a": "x", "name": "thing", "empty": "   "},
			want:     "thing",
		},
		{
			name:     "a repeated value appears once",
			metadata: map[string]string{"name": "thing", "title": "thing", "summary": "a thing"},
			want:     "thing, a thing",
		},
		{
			name:     "metadata with nothing semantic yields no query",
			metadata: map[string]string{"id": "42", "url": "https://example.com/x"},
			want:     "",
		},
		{
			name:     "no metadata yields no query",
			metadata: nil,
			want:     "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := metadataQuery(c.metadata); got != c.want {
				t.Errorf("metadataQuery = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMetadataQueryClipsAnOversizedValue(t *testing.T) {
	metadata := map[string]string{
		"a_description": strings.Repeat("word ", maxQueryChars),
		"z_name":        "thing",
	}
	got := metadataQuery(metadata)
	if runes := len([]rune(got)); runes > maxQueryChars {
		t.Errorf("metadataQuery returned %d runes, want at most %d", runes, maxQueryChars)
	}
	if !strings.HasPrefix(got, "word word") {
		t.Errorf("metadataQuery = %.20q, want the clipped description rather than nothing", got)
	}
	if strings.Contains(got, "thing") {
		t.Error("metadataQuery kept appending after the budget was spent")
	}
}
