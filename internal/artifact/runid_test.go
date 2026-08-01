// Run identifier tests. The property that matters is ordering: readers resolve "latest"
// by taking the largest identifier, so lexical order must follow creation order.
package artifact

import (
	"testing"
	"time"
)

// TestRunIDsSortChronologically generates a batch back to back — the case where the
// millisecond timestamp cannot break ties — and requires each to sort after the last.
func TestRunIDsSortChronologically(t *testing.T) {
	const n = 100
	previous := ""
	for i := range n {
		runID, err := NewRunID()
		if err != nil {
			t.Fatalf("NewRunID %d: %v", i, err)
		}
		if len(runID) != 26 {
			t.Fatalf("NewRunID = %q, want 26 characters", runID)
		}
		if runID <= previous {
			t.Fatalf("run id %d = %q does not sort after %q", i, runID, previous)
		}
		previous = runID
	}
}

// TestRunIDCarriesItsTime proves the timestamp survives a round trip, which is what makes
// retention windows expressible against directory names alone.
func TestRunIDCarriesItsTime(t *testing.T) {
	before := time.Now().Truncate(time.Millisecond)
	runID, err := NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	got, err := ParseRunID(runID)
	if err != nil {
		t.Fatalf("ParseRunID: %v", err)
	}
	if got.Before(before) || got.After(time.Now().Add(time.Second)) {
		t.Errorf("ParseRunID(%q) = %s, want a time at or after %s", runID, got, before)
	}
}

func TestParseRunIDRejects(t *testing.T) {
	tests := []struct {
		name  string
		runID string
	}{
		{"empty", ""},
		{"too short", "01K1AB2CDEF"},
		{"too long", validRunID + "Z"},
		// I, L, O and U are excluded from Crockford base32 to avoid transcription
		// ambiguity, so a directory containing them is not a run.
		{"ambiguous letters", "01K1AB2CDEFGHIKMNPQRSTVWXY"},
		// Decoding is case-insensitive, but a lowercase name would sort after every
		// canonical one and so resolve as "latest".
		{"non-canonical lowercase", "01k1ab2cdefghjkmnpqrstvwxy"},
		{"not an identifier at all", "latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseRunID(tt.runID); err == nil {
				t.Errorf("ParseRunID(%q) = nil, want an error", tt.runID)
			}
		})
	}
	if _, err := ParseRunID(validRunID); err != nil {
		t.Errorf("ParseRunID(%q) = %v, want nil", validRunID, err)
	}
}
