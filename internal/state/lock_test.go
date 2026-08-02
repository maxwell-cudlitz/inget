// Conformance cases for locking: one run per datatype, whichever driver is underneath.
package state

import (
	"errors"
	"testing"
)

// lock takes a lock and fails the test if it cannot.
func lock(t *testing.T, s Store, key string) func() error {
	t.Helper()
	release, err := s.Lock(t.Context(), key)
	if err != nil {
		t.Fatalf("Lock %s: %v", key, err)
	}
	return release
}

var lockCases = []storeCase{
	{
		name: "a second lock on the same key fails while the first is held",
		run: func(t *testing.T, h harness) {
			release := lock(t, h.Store, testDatatype)

			// A second process, not a second call on the same store: the advisory lock is
			// held by a session, so contention only means anything across connections.
			other := h.Open(t)
			if _, err := other.Lock(t.Context(), testDatatype); !errors.Is(err, ErrLocked) {
				t.Fatalf("second Lock error = %v, want ErrLocked", err)
			}
			if err := release(); err != nil {
				t.Fatalf("release: %v", err)
			}
		},
	},
	{
		name: "a released lock can be taken again",
		run: func(t *testing.T, h harness) {
			release := lock(t, h.Store, testDatatype)
			if err := release(); err != nil {
				t.Fatalf("release: %v", err)
			}
			other := h.Open(t)
			secondRelease := lock(t, other, testDatatype)
			if err := secondRelease(); err != nil {
				t.Errorf("release after re-locking: %v", err)
			}
		},
	},
	{
		name: "locks on different keys do not contend",
		run: func(t *testing.T, h harness) {
			releaseFirst := lock(t, h.Store, testDatatype)
			other := h.Open(t)
			releaseSecond := lock(t, other, "monday/item")

			for _, release := range []func() error{releaseFirst, releaseSecond} {
				if err := release(); err != nil {
					t.Errorf("release: %v", err)
				}
			}
		},
	},
	{
		name: "an empty lock key is rejected",
		run: func(t *testing.T, h harness) {
			if _, err := h.Lock(t.Context(), ""); err == nil {
				t.Error("Lock(\"\"): want an error")
			}
		},
	},
}
