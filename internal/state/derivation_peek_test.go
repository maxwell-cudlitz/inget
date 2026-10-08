// Peeked derivations return real cached output while preserving cache-retention timestamps.
package state

import (
	"testing"
	"time"
)

var peekDerivationCases = []storeCase{
	{
		name: "peeking an uncached derivation is a miss",
		run: func(t *testing.T, h harness) {
			output, found, err := h.PeekDerivation(t.Context(), "sha256:missing")
			if err != nil || found || output != "" {
				t.Fatalf("PeekDerivation = %q, %v, %v; want empty miss", output, found, err)
			}
		},
	},
	{
		name: "peeking cached output does not refresh last hit",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			const key = "sha256:peek"
			if err := h.PutDerivation(ctx, derivation(key, "sig", "日本語 summary")); err != nil {
				t.Fatal(err)
			}
			s, ok := h.Store.(*store)
			if !ok {
				t.Fatalf("store = %T, want SQL store", h.Store)
			}
			if _, err := s.exec(ctx, "UPDATE derivations SET last_hit_at = ? WHERE cache_key = ?",
				"2000-01-01 00:00:00", key); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				output, found, err := h.PeekDerivation(ctx, key)
				if err != nil || !found || output != "日本語 summary" {
					t.Fatalf("PeekDerivation = %q, %v, %v", output, found, err)
				}
			}
			var hit any
			found, err := s.get(ctx, "SELECT last_hit_at FROM derivations WHERE cache_key = ?", []any{key}, &hit)
			if err != nil || !found {
				t.Fatalf("last hit query: found=%v err=%v", found, err)
			}
			when, err := asTime(hit)
			if err != nil {
				t.Fatal(err)
			}
			if want := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC); !when.Equal(want) {
				t.Errorf("last hit after peek = %s, want %s", when, want)
			}
		},
	},
}
