package delta

import (
	"strings"
	"testing"
)

func TestFragmentCacheKey(t *testing.T) {
	key := FragmentCacheKey("fp-abc123", "sha256:sig-def456")

	if !strings.HasPrefix(key, "sha256:") {
		t.Errorf("FragmentCacheKey = %q, want sha256: prefix", key)
	}
	// Same inputs produce the same key.
	if key2 := FragmentCacheKey("fp-abc123", "sha256:sig-def456"); key != key2 {
		t.Errorf("same inputs produced different keys:\n  %s\n  %s", key, key2)
	}
	// Different inputs produce different keys.
	if diff := FragmentCacheKey("fp-other", "sha256:sig-def456"); diff == key {
		t.Error("different fingerprint produced the same cache key")
	}
	if diff := FragmentCacheKey("fp-abc123", "sha256:sig-other"); diff == key {
		t.Error("different signature produced the same cache key")
	}
}

func TestViewInputHash(t *testing.T) {
	key := ViewInputHash("sha256:composed-hash", "sha256:prompt-sig")

	if !strings.HasPrefix(key, "sha256:") {
		t.Errorf("ViewInputHash = %q, want sha256: prefix", key)
	}
	if key2 := ViewInputHash("sha256:composed-hash", "sha256:prompt-sig"); key != key2 {
		t.Errorf("same inputs produced different keys")
	}
	if diff := ViewInputHash("sha256:other", "sha256:prompt-sig"); diff == key {
		t.Error("different composed hash produced the same view input hash")
	}
	if diff := ViewInputHash("sha256:composed-hash", "sha256:other"); diff == key {
		t.Error("different prompt signature produced the same view input hash")
	}
}

func TestEmbeddingHash(t *testing.T) {
	key := EmbeddingHash("the quick brown fox")

	if !strings.HasPrefix(key, "sha256:") {
		t.Errorf("EmbeddingHash = %q, want sha256: prefix", key)
	}
	if key2 := EmbeddingHash("the quick brown fox"); key != key2 {
		t.Errorf("same text produced different hashes")
	}
	if diff := EmbeddingHash("different text"); diff == key {
		t.Error("different text produced the same embedding hash")
	}
}

func TestCacheKeyLevelsDistinct(t *testing.T) {
	// Even with the same string inputs, different level functions should produce
	// different keys because they hash different structures.
	l1 := FragmentCacheKey("a", "b")
	l2 := ViewInputHash("a", "b")
	l3 := EmbeddingHash("a")

	if l1 == l2 {
		t.Error("Level 1 and Level 2 produced identical keys for same inputs")
	}
	if l1 == l3 || l2 == l3 {
		t.Error("Level 3 collided with another level")
	}
}
