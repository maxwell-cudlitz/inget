// Conformance cases for the collection reads behind `inget state gc`, plus the sqlite-only
// force-unlock path.
package state

import (
	"errors"
	"os"
	"testing"
)

// putFragmentSet writes an item's complete fragment set, which is what advances the
// missing-runs counter of anything left out.
func putFragmentSet(t *testing.T, s Store, itemID string, fragments ...FragmentState) {
	t.Helper()
	if err := s.PutFragments(t.Context(), testDatatype, itemID, fragments); err != nil {
		t.Fatalf("PutFragments %s: %v", itemID, err)
	}
}

// fragment builds a fragment with a blob reference.
func fragment(key, blobRef string) FragmentState {
	return FragmentState{Key: key, Fingerprint: "fp:" + key, BlobRef: blobRef, SizeBytes: 10}
}

var gcCases = []storeCase{
	{
		name: "live blob refs are the distinct digests fragments point at",
		run: func(t *testing.T, h harness) {
			seedItem(t, h.Store, testItem, "fp-1")
			seedItem(t, h.Store, "acme/other", "fp-2")
			// The same content in two items is one blob, and a truncated fragment has no
			// blob at all: both are the cases a naive count would get wrong.
			putFragmentSet(t, h.Store, testItem, fragment("README.md", "sha-a"), fragment("go.mod", "sha-b"))
			putFragmentSet(t, h.Store, "acme/other", fragment("README.md", "sha-a"), fragment("huge.bin", ""))

			refs, err := h.LiveBlobRefs(t.Context())
			if err != nil {
				t.Fatalf("LiveBlobRefs: %v", err)
			}
			if len(refs) != 2 {
				t.Fatalf("LiveBlobRefs = %v, want exactly sha-a and sha-b", refs)
			}
			for _, want := range []string{"sha-a", "sha-b"} {
				if _, ok := refs[want]; !ok {
					t.Errorf("LiveBlobRefs is missing %s", want)
				}
			}
		},
	},
	{
		name: "derivations of long-missing fragments are collected and others are not",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h.Store, testItem, "fp-1")
			putFragmentSet(t, h.Store, testItem, fragment("README.md", "sha-a"), fragment("gone.md", "sha-b"))
			for _, d := range []Derivation{
				{CacheKey: "key:kept", Datatype: testDatatype, ItemID: testItem, FragKey: "README.md",
					Signature: "sig", Output: "kept"},
				{CacheKey: "key:collected", Datatype: testDatatype, ItemID: testItem, FragKey: "gone.md",
					Signature: "sig", Output: "collected"},
			} {
				if err := h.PutDerivation(ctx, d); err != nil {
					t.Fatalf("PutDerivation %s: %v", d.CacheKey, err)
				}
			}

			// Four runs that no longer carry gone.md put its missing_runs at 4, one past a
			// retention.missing_runs of 3.
			for range 4 {
				putFragmentSet(t, h.Store, testItem, fragment("README.md", "sha-a"))
			}

			collected, err := h.CollectDerivations(ctx, 3)
			if err != nil {
				t.Fatalf("CollectDerivations: %v", err)
			}
			if collected != 1 {
				t.Errorf("CollectDerivations = %d, want 1", collected)
			}
			for key, want := range map[string]bool{"key:kept": true, "key:collected": false} {
				has, err := h.HasDerivation(ctx, key)
				if err != nil {
					t.Fatalf("HasDerivation %s: %v", key, err)
				}
				if has != want {
					t.Errorf("HasDerivation(%s) = %v, want %v", key, has, want)
				}
			}
		},
	},
	{
		name: "a fragment inside the window keeps its derivation",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			seedItem(t, h.Store, testItem, "fp-1")
			putFragmentSet(t, h.Store, testItem, fragment("README.md", "sha-a"), fragment("gone.md", "sha-b"))
			if err := h.PutDerivation(ctx, derivation("key:a", "sig", "output")); err != nil {
				t.Fatalf("PutDerivation: %v", err)
			}
			putFragmentSet(t, h.Store, testItem, fragment("README.md", "sha-a"))

			collected, err := h.CollectDerivations(ctx, 3)
			if err != nil {
				t.Fatalf("CollectDerivations: %v", err)
			}
			if collected != 0 {
				t.Errorf("CollectDerivations = %d after one absence, want 0", collected)
			}
		},
	},
	{
		name: "collecting with a non-positive window is rejected",
		run: func(t *testing.T, h harness) {
			if _, err := h.CollectDerivations(t.Context(), 0); err == nil {
				t.Error("CollectDerivations(0): want an error")
			}
		},
	},
	{
		name: "force-unlocking an unlocked key removes nothing and is not an error",
		run: func(t *testing.T, h harness) {
			removed, err := h.ForceUnlock(t.Context(), testDatatype)
			if err != nil {
				t.Fatalf("ForceUnlock: %v", err)
			}
			if removed {
				t.Error("ForceUnlock reported removing a lock that was never taken")
			}
			// Whatever it did, the key must still be lockable afterwards.
			release := lock(t, h.Store, testDatatype)
			if err := release(); err != nil {
				t.Errorf("release: %v", err)
			}
		},
	},
	{
		name: "force-unlocking an empty key is rejected",
		run: func(t *testing.T, h harness) {
			if _, err := h.ForceUnlock(t.Context(), ""); err == nil {
				t.Error("ForceUnlock(\"\"): want an error")
			}
		},
	},
}

// TestForceUnlockRemovesAStaleSQLiteLock covers the case the conformance suite cannot state
// once for both drivers, because the drivers disagree about whether it can happen at all.
//
// A killed process leaves the sqlite mutex row behind, and nothing but this removes it. On
// postgres the equivalent state does not exist: the lock is session-scoped, so a lock that is
// still held is held by something alive, and ForceUnlock reports ErrLocked rather than taking
// it away — which is the second assertion here, against the same held lock.
func TestForceUnlockRemovesAStaleSQLiteLock(t *testing.T) {
	h := sqliteHarness(t)
	holder := h.Open(t)
	if _, err := holder.Lock(t.Context(), testDatatype); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	removed, err := h.ForceUnlock(t.Context(), testDatatype)
	if err != nil {
		t.Fatalf("ForceUnlock: %v", err)
	}
	if !removed {
		t.Fatal("ForceUnlock removed nothing, want the abandoned lock row removed")
	}
	release := lock(t, h.Store, testDatatype)
	if err := release(); err != nil {
		t.Errorf("release: %v", err)
	}
}

// TestForceUnlockRefusesALiveAdvisoryLock is the postgres half of the statement above. It runs
// only where a scratch database is configured, because the behaviour is the server's.
func TestForceUnlockRefusesALiveAdvisoryLock(t *testing.T) {
	if os.Getenv(envTestPostgres) == "" {
		t.Skipf("%s is not set; the advisory-lock half of force-unlock needs postgres", envTestPostgres)
	}
	h := postgresHarness(t)
	holder := h.Open(t)
	release, err := holder.Lock(t.Context(), testDatatype)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if _, err := h.ForceUnlock(t.Context(), testDatatype); !errors.Is(err, ErrLocked) {
		t.Errorf("ForceUnlock error = %v, want ErrLocked: an advisory lock is not stealable", err)
	}
	// Released before the harness closes: an advisory lock pins its connection, and Close waits
	// for it, which is the documented contract and would otherwise hang this test.
	if err := release(); err != nil {
		t.Errorf("release: %v", err)
	}
}
