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
	composed, hash := Compose(entries, "tier", 0)

	// Tier 1 first (a before c by key), then tier 2.
	want := "content-a\n---\ncontent-c\n---\ncontent-b"
	if composed != want {
		t.Errorf("tier order:\ngot  %q\nwant %q", composed, want)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Errorf("hash = %q, want sha256: prefix", hash)
	}
}

func TestComposePathOrder(t *testing.T) {
	entries := []ComposeEntry{
		{Key: "z/file", Tier: 1, Content: "content-z"},
		{Key: "a/file", Tier: 3, Content: "content-a"},
		{Key: "m/file", Tier: 2, Content: "content-m"},
	}
	composed, _ := Compose(entries, "path", 0)

	// Sorted by key ascending, tier ignored.
	want := "content-a\n---\ncontent-m\n---\ncontent-z"
	if composed != want {
		t.Errorf("path order:\ngot  %q\nwant %q", composed, want)
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
	_, hash1 := Compose(entries1, "tier", 0)
	_, hash2 := Compose(entries2, "tier", 0)

	if hash1 != hash2 {
		t.Errorf("same entries in different input order produced different hashes:\n  %s\n  %s", hash1, hash2)
	}
}

func TestComposeMaxChars(t *testing.T) {
	entries := []ComposeEntry{
		{Key: "a", Tier: 1, Content: "hello world"},
	}
	composed, _ := Compose(entries, "path", 5)

	if composed != "hello" {
		t.Errorf("truncation: got %q, want %q", composed, "hello")
	}
}

func TestComposeMaxCharsUnicode(t *testing.T) {
	// Each emoji is one rune but multiple bytes.
	entries := []ComposeEntry{
		{Key: "a", Tier: 1, Content: "🎉🎊🎈🎁🎂"},
	}
	composed, _ := Compose(entries, "path", 3)

	if composed != "🎉🎊🎈" {
		t.Errorf("unicode truncation: got %q, want %q", composed, "🎉🎊🎈")
	}
}

func TestComposeEmpty(t *testing.T) {
	composed, hash := Compose(nil, "tier", 0)
	if composed != "" {
		t.Errorf("empty input: got %q, want empty", composed)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Errorf("hash of empty = %q, want sha256: prefix", hash)
	}
}
