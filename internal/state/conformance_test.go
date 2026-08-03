// The conformance suite: one set of behavioural assertions, run against every driver.
//
// This is the point of the two-driver design. A case lives next to the code it exercises —
// itemCases in items_test.go and so on — and this file only decides what runs where. A
// behaviour that holds on sqlite and not on postgres is a bug in one of them, and the suite
// is where that shows up rather than in production.
package state

import (
	"slices"
	"testing"
)

// conformanceCases is every case, gathered from the per-concern files.
func conformanceCases() []storeCase {
	return slices.Concat(
		schemaCases,
		itemCases,
		fragmentCases,
		derivationCases,
		viewCases,
		refCases,
		signatureCases,
		consumedCases,
		runCases,
		workCases,
		checkpointCases,
		lockCases,
		gcCases,
		inspectCases,
	)
}

func TestConformance(t *testing.T) {
	cases := conformanceCases()
	for _, driver := range drivers(t) {
		t.Run(driver.name, func(t *testing.T) {
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					c.run(t, driver.newHarness(t))
				})
			}
		})
	}
}

// schemaCases assert the migration mechanics both drivers depend on.
var schemaCases = []storeCase{{
	name: "migrate is idempotent",
	run: func(t *testing.T, h harness) {
		// The harness already migrated once; a second and third pass must be no-ops
		// rather than an attempt to recreate existing tables.
		for i := range 2 {
			if err := h.Migrate(t.Context()); err != nil {
				t.Fatalf("Migrate pass %d: %v", i+2, err)
			}
		}
		if _, err := h.ItemFingerprints(t.Context(), testDatatype); err != nil {
			t.Fatalf("querying a migrated schema: %v", err)
		}
	},
}}
