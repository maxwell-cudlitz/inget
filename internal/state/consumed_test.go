// Conformance cases for the artifact high-water mark.
package state

import "testing"

// Artifact run IDs are ULIDs, so lexical order is chronological order. These fixtures keep
// that property, which is what the monotonicity guard relies on.
const (
	firstArtifact  = "01K1QZ8Q0000000000000000A1"
	secondArtifact = "01K1QZ8Q0000000000000000A2"
)

var consumedCases = []storeCase{
	{
		name: "an unconsumed datatype reads back empty without an error",
		run: func(t *testing.T, h harness) {
			got, err := h.ConsumedArtifactRun(t.Context(), testDatatype)
			if err != nil {
				t.Fatalf("ConsumedArtifactRun: %v", err)
			}
			if got != "" {
				t.Errorf("ConsumedArtifactRun = %q, want empty", got)
			}
		},
	},
	{
		name: "the mark reads back what was recorded",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutConsumedArtifactRun(ctx, testDatatype, firstArtifact); err != nil {
				t.Fatalf("PutConsumedArtifactRun: %v", err)
			}
			got, err := h.ConsumedArtifactRun(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ConsumedArtifactRun: %v", err)
			}
			if got != firstArtifact {
				t.Errorf("ConsumedArtifactRun = %q, want %q", got, firstArtifact)
			}
		},
	},
	{
		name: "the mark advances to a newer run",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			for _, runID := range []string{firstArtifact, secondArtifact} {
				if err := h.PutConsumedArtifactRun(ctx, testDatatype, runID); err != nil {
					t.Fatalf("PutConsumedArtifactRun %s: %v", runID, err)
				}
			}
			got, err := h.ConsumedArtifactRun(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ConsumedArtifactRun: %v", err)
			}
			if got != secondArtifact {
				t.Errorf("ConsumedArtifactRun = %q, want %q", got, secondArtifact)
			}
		},
	},
	{
		// Rewinding would replay runs already consumed, so an older or equal identifier is
		// ignored rather than written.
		name: "the mark never moves backwards",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutConsumedArtifactRun(ctx, testDatatype, secondArtifact); err != nil {
				t.Fatalf("PutConsumedArtifactRun: %v", err)
			}
			if err := h.PutConsumedArtifactRun(ctx, testDatatype, firstArtifact); err != nil {
				t.Fatalf("PutConsumedArtifactRun with an older run: %v", err)
			}
			got, err := h.ConsumedArtifactRun(ctx, testDatatype)
			if err != nil {
				t.Fatalf("ConsumedArtifactRun: %v", err)
			}
			if got != secondArtifact {
				t.Errorf("ConsumedArtifactRun = %q, want %q", got, secondArtifact)
			}
		},
	},
	{
		name: "the mark is per datatype",
		run: func(t *testing.T, h harness) {
			ctx := t.Context()
			if err := h.PutConsumedArtifactRun(ctx, testDatatype, firstArtifact); err != nil {
				t.Fatalf("PutConsumedArtifactRun: %v", err)
			}
			got, err := h.ConsumedArtifactRun(ctx, "monday/item")
			if err != nil {
				t.Fatalf("ConsumedArtifactRun: %v", err)
			}
			if got != "" {
				t.Errorf("ConsumedArtifactRun of another datatype = %q, want empty", got)
			}
		},
	},
	{
		name: "a mark needs both a datatype and a run",
		run: func(t *testing.T, h harness) {
			if err := h.PutConsumedArtifactRun(t.Context(), testDatatype, ""); err == nil {
				t.Error("expected an error for an empty run id")
			}
			if err := h.PutConsumedArtifactRun(t.Context(), "", firstArtifact); err == nil {
				t.Error("expected an error for an empty datatype")
			}
		},
	},
}
