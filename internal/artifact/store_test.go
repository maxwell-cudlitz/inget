// Blob store tests: content addressing, deduplication, size cap, integrity.
package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestPutBlobDeduplicates proves identical content is written once, which is what makes an
// incremental fetch cheap, and that the reported digest addresses the uncompressed bytes.
func TestPutBlobDeduplicates(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()
	content := []byte("# inget\n\nGeneric ingestion, enrichment, and embedding pipeline.\n")

	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	digest, written, err := store.PutBlob(ctx, content)
	if err != nil {
		t.Fatalf("first PutBlob: %v", err)
	}
	if digest != want {
		t.Errorf("digest = %s, want the SHA-256 of the uncompressed content %s", digest, want)
	}
	if !written {
		t.Error("first PutBlob reported no write")
	}

	stat, err := os.Stat(objectPath(t, store, blobKey(digest)))
	if err != nil {
		t.Fatalf("stat blob: %v", err)
	}
	first := stat.ModTime()

	digest2, written2, err := store.PutBlob(ctx, content)
	if err != nil {
		t.Fatalf("second PutBlob: %v", err)
	}
	if digest2 != digest {
		t.Errorf("second digest = %s, want %s", digest2, digest)
	}
	if written2 {
		t.Error("second PutBlob reported a write, want the existing blob reused")
	}
	if stat, err = os.Stat(objectPath(t, store, blobKey(digest))); err != nil {
		t.Fatalf("re-stat blob: %v", err)
	} else if !stat.ModTime().Equal(first) {
		t.Error("blob was rewritten; existence check did not skip it")
	}

	got, err := store.GetBlob(ctx, digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("GetBlob returned %q, want %q", got, content)
	}
}

// TestHasBlob covers both answers plus a malformed digest, which must be refused rather
// than turned into a key.
func TestHasBlob(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	digest, _, err := store.PutBlob(ctx, []byte("present"))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	present, err := store.HasBlob(ctx, digest)
	if err != nil {
		t.Fatalf("HasBlob: %v", err)
	}
	if !present {
		t.Error("HasBlob = false for a stored blob")
	}

	absent, err := store.HasBlob(ctx, strings.Repeat("0", digestHexLen))
	if err != nil {
		t.Fatalf("HasBlob on an absent digest: %v", err)
	}
	if absent {
		t.Error("HasBlob = true for a digest never stored")
	}

	for _, bad := range []string{"", "abc", strings.Repeat("A", digestHexLen), "../../etc/passwd"} {
		if _, err := store.HasBlob(ctx, bad); err == nil {
			t.Errorf("HasBlob(%q) succeeded, want a rejected digest", bad)
		}
	}
}

// TestPutBlobTooLarge proves the cap is reported as a distinguishable condition, because
// the producer's response is to mark the fragment truncated rather than to fail the run.
func TestPutBlobTooLarge(t *testing.T) {
	store := newStore(t, 0) // helper configures BlobMaxBytes = 4096
	ctx := t.Context()

	_, _, err := store.PutBlob(ctx, bytes.Repeat([]byte("x"), 4097))
	if !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("PutBlob over the cap: %v, want ErrBlobTooLarge", err)
	}
	if _, _, err := store.PutBlob(ctx, bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Errorf("PutBlob at exactly the cap: %v, want success", err)
	}
}

// TestGetBlobDetectsCorruption proves stored content is verified against the digest that
// addressed it, so silent bit rot fails the run instead of poisoning an embedding.
func TestGetBlobDetectsCorruption(t *testing.T) {
	store := newStore(t, 0)
	ctx := t.Context()

	digest, _, err := store.PutBlob(ctx, []byte("original content"))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if err := os.WriteFile(objectPath(t, store, blobKey(digest)), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tampering: %v", err)
	}
	if _, err := store.GetBlob(ctx, digest); err == nil {
		t.Error("GetBlob accepted content that does not match its digest")
	}
}

// TestGetBlobMissing proves a referenced but absent blob is a distinguishable ErrNotFound,
// which is how the enrichment pipeline will tell a collected blob from a broken store.
func TestGetBlobMissing(t *testing.T) {
	store := newStore(t, 0)
	if _, err := store.GetBlob(t.Context(), strings.Repeat("a", digestHexLen)); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBlob on an absent blob: %v, want ErrNotFound", err)
	}
}

// TestUncompressedStoreReadsBack proves compression: none round-trips, and that a store
// written one way reads back either way — the digest, not the setting, decides.
func TestUncompressedStoreReadsBack(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()
	content := []byte("plain bytes, no frame")

	plain, err := Open(ctx, Options{URL: fileURL(dir), Compression: CompressionNone})
	if err != nil {
		t.Fatalf("Open uncompressed: %v", err)
	}
	digest, _, err := plain.PutBlob(ctx, content)
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	stored, err := os.ReadFile(objectPath(t, plain, blobKey(digest)))
	if err != nil {
		t.Fatalf("reading stored blob: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Errorf("stored bytes = %q, want the content verbatim", stored)
	}
	if err := plain.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A zstd store reading a store written without compression: the sniff decides.
	compressed, err := Open(ctx, Options{URL: fileURL(dir), Compression: CompressionZstd})
	if err != nil {
		t.Fatalf("Open compressed: %v", err)
	}
	defer func() {
		if err := compressed.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	got, err := compressed.GetBlob(ctx, digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("GetBlob = %q, want %q", got, content)
	}
}

// TestOpenCreatesDirectory proves a file:// store works against a path that does not exist
// yet, which is the state of every fresh checkout.
func TestOpenCreatesDirectory(t *testing.T) {
	dir := t.TempDir() + "/nested/.inget/artifacts"
	store, err := Open(t.Context(), Options{URL: fileURL(dir)})
	if err != nil {
		t.Fatalf("Open on a missing directory: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("store directory was not created: %v", err)
	}
}

// TestOpenRejectsBadOptions covers the two ways Options cannot be satisfied.
func TestOpenRejectsBadOptions(t *testing.T) {
	if _, err := Open(t.Context(), Options{}); err == nil {
		t.Error("Open with no URL succeeded, want an error")
	}
	if _, err := Open(t.Context(), Options{URL: fileURL(t.TempDir()), Compression: "gzip"}); err == nil {
		t.Error("Open with an unknown compression succeeded, want an error")
	}
}
