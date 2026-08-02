// Key extraction cases: where a reference key is found and how it is narrowed.
//
// The narrowing cases are the ones that matter in practice. A Monday column holds a URL and a
// github/repo item is keyed "owner/name", so a reference that cannot narrow is a reference
// that resolves nothing — silently, since an unresolved referent is not an error.
package refs

import (
	"context"
	"strings"
	"testing"
)

// stubInput builds an Input whose fragment content is served from a map.
func stubInput(metadata, fragments map[string]string) Input {
	keys := make([]string, 0, len(fragments))
	for k := range fragments {
		keys = append(keys, k)
	}
	return Input{
		Datatype:  "monday/item",
		ItemID:    "42",
		Metadata:  metadata,
		Fragments: keys,
		Content: func(_ context.Context, key string) (string, error) {
			return fragments[key], nil
		},
	}
}

func TestKeyExtraction(t *testing.T) {
	const repoURL = `{"url":"https://github.com/acme/thing.git"}`
	githubRegex := `github\.com/([^/"?#]+/[^/"?#.]+)`

	cases := []struct {
		name     string
		keyFrom  string
		keyRegex string
		want     []string
	}{
		{
			name:    "a metadata field is read directly",
			keyFrom: "metadata:linked_repo",
			want:    []string{"acme/direct"},
		},
		{
			name:    "a fragment glob reads the fragment's content",
			keyFrom: "column:ticket_id",
			want:    []string{"TICKET-7"},
		},
		{
			name:     "key_regex narrows a URL to the referent's item ID",
			keyFrom:  "column:repo_url",
			keyRegex: githubRegex,
			want:     []string{"acme/thing"},
		},
		{
			name:    "a glob matching several fragments yields sorted, deduplicated keys",
			keyFrom: "column:person*",
			want:    []string{"alice", "bob"},
		},
		{
			name:     "content the regex does not match yields no key",
			keyFrom:  "column:ticket_id",
			keyRegex: githubRegex,
			want:     nil,
		},
		{
			name:    "an empty column is skipped rather than resolved to an empty key",
			keyFrom: "column:empty",
			want:    nil,
		},
		{
			name:    "a glob matching nothing yields no key",
			keyFrom: "column:absent",
			want:    nil,
		},
	}

	in := stubInput(
		map[string]string{"linked_repo": "acme/direct"},
		map[string]string{
			"column:repo_url":  repoURL,
			"column:ticket_id": "TICKET-7",
			"column:person_a":  "bob",
			"column:person_b":  "alice",
			"column:person_c":  "bob\n",
			"column:empty":     "   ",
		},
	)

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			k, err := newKeyExtractor(tt.keyFrom, tt.keyRegex)
			if err != nil {
				t.Fatalf("newKeyExtractor: %v", err)
			}
			got, err := k.extract(t.Context(), in)
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("extract = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKeyExtractorRejectsBadPatterns(t *testing.T) {
	cases := []struct {
		name     string
		keyFrom  string
		keyRegex string
	}{
		{name: "empty key_from", keyFrom: ""},
		{name: "uncompilable regex", keyFrom: "column:x", keyRegex: `([a-z`},
		{name: "no capture group", keyFrom: "column:x", keyRegex: `github\.com`},
		{name: "two capture groups", keyFrom: "column:x", keyRegex: `(a)/(b)`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newKeyExtractor(tt.keyFrom, tt.keyRegex); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
