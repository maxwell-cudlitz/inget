// Tests for the deterministic composer.
//
// The composed document's bytes are part of every level-2 cache key, so ordering, the
// separator, the header and truncation are all behaviour rather than formatting: a change
// to any of them regenerates every view of every datatype. These tests pin them, and pin
// that identical entries in different input orders compose identically.
package delta

import (
	"strings"
	"testing"
)

func TestComposeTierOrder(t *testing.T) {
	entries := []ComposeEntry{
		{Key: "b", Tier: 2, Content: "content-b"},
		{Key: "a", Tier: 1, Content: "content-a"},
		{Key: "c", Tier: 1, Content: "content-c"},
	}
	got := Compose(entries, "tier", 0)

	// Tier 1 first (a before c by key), then tier 2.
	want := "## a\ncontent-a\n---\n## c\ncontent-c\n---\n## b\ncontent-b"
	if got.Text != want {
		t.Errorf("tier order:\ngot  %q\nwant %q", got.Text, want)
	}
	if !strings.HasPrefix(got.Hash, "sha256:") {
		t.Errorf("hash = %q, want sha256: prefix", got.Hash)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false")
	}
}

func TestComposePathOrder(t *testing.T) {
	entries := []ComposeEntry{
		{Key: "z/file", Tier: 1, Content: "content-z"},
		{Key: "a/file", Tier: 3, Content: "content-a"},
		{Key: "m/file", Tier: 2, Content: "content-m"},
	}
	got := Compose(entries, "path", 0)

	// Sorted by key ascending, tier ignored.
	want := "## a/file\ncontent-a\n---\n## m/file\ncontent-m\n---\n## z/file\ncontent-z"
	if got.Text != want {
		t.Errorf("path order:\ngot  %q\nwant %q", got.Text, want)
	}
}

func TestComposeDeterministic(t *testing.T) {
	entries1 := []ComposeEntry{
		{Key: "b", Tier: 1, Content: "B"},
		{Key: "a", Tier: 1, Content: "A"},
	}
	entries2 := []ComposeEntry{
		{Key: "a", Tier: 1, Content: "A"},
		{Key: "b", Tier: 1, Content: "B"},
	}
	c1 := Compose(entries1, "tier", 0)
	c2 := Compose(entries2, "tier", 0)

	if c1.Hash != c2.Hash {
		t.Errorf("same entries in different input order produced different hashes:\n  %s\n  %s", c1.Hash, c2.Hash)
	}
}

// A fragment whose content moved to another path must not reuse the old view: the key is
// part of the composed document, so the hash changes with it.
func TestComposeHashChangesWithKey(t *testing.T) {
	before := Compose([]ComposeEntry{{Key: "old/path.go", Tier: 1, Content: "same"}}, "tier", 0)
	after := Compose([]ComposeEntry{{Key: "new/path.go", Tier: 1, Content: "same"}}, "tier", 0)

	if before.Hash == after.Hash {
		t.Error("renaming a fragment did not change the composed hash")
	}
}

func TestComposeMaxChars(t *testing.T) {
	entries := []ComposeEntry{
		{Key: "a", Tier: 1, Content: "hello world"},
	}
	// "## a\nhello world" is 16 runes; clip to the first 9.
	got := Compose(entries, "path", 9)

	if got.Text != "## a\nhell" {
		t.Errorf("truncation: got %q, want %q", got.Text, "## a\nhell")
	}
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	if got.OriginalChars != 16 {
		t.Errorf("OriginalChars = %d, want 16", got.OriginalChars)
	}
}

func TestComposeMaxCharsNotReached(t *testing.T) {
	got := Compose([]ComposeEntry{{Key: "a", Tier: 1, Content: "short"}}, "path", 1000)

	if got.Truncated {
		t.Error("Truncated = true for a document under the limit")
	}
	if got.OriginalChars != len("## a\nshort") {
		t.Errorf("OriginalChars = %d, want %d", got.OriginalChars, len("## a\nshort"))
	}
}

func TestComposeMaxCharsUnicode(t *testing.T) {
	// Each emoji is one rune but multiple bytes; the limit counts runes.
	entries := []ComposeEntry{
		{Key: "a", Tier: 1, Content: "🎉🎊🎈🎁🎂"},
	}
	got := Compose(entries, "path", 8)

	if got.Text != "## a\n🎉🎊🎈" {
		t.Errorf("unicode truncation: got %q, want %q", got.Text, "## a\n🎉🎊🎈")
	}
	if got.OriginalChars != 10 {
		t.Errorf("OriginalChars = %d, want 10", got.OriginalChars)
	}
}

func TestComposeEmpty(t *testing.T) {
	got := Compose(nil, "tier", 0)
	if got.Text != "" {
		t.Errorf("empty input: got %q, want empty", got.Text)
	}
	if !strings.HasPrefix(got.Hash, "sha256:") {
		t.Errorf("hash of empty = %q, want sha256: prefix", got.Hash)
	}
	if got.OriginalChars != 0 {
		t.Errorf("OriginalChars = %d, want 0", got.OriginalChars)
	}
}
