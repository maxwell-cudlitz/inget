// Record validation tests: the per-item half of the consumer obligations.
package artifact

import (
	"strings"
	"testing"
	"time"
)

func TestRecordValidate(t *testing.T) {
	valid := func() *Record {
		r := testRecord(1)
		r.SchemaVersion = SchemaVersion
		r.Datatype = testDatatype
		return r
	}
	tests := []struct {
		name    string
		mutate  func(*Record)
		wantErr string
	}{
		{name: "valid"},
		{
			// Empty means "unknown", which the consumer handles by treating the item as
			// changed. It is not a reason to reject the record.
			name:   "empty fingerprint is allowed",
			mutate: func(r *Record) { r.Fingerprint = "" },
		},
		{
			name:    "unknown schema version",
			mutate:  func(r *Record) { r.SchemaVersion = 0 },
			wantErr: "unsupported schema_version",
		},
		{
			name:    "missing item id",
			mutate:  func(r *Record) { r.ItemID = "" },
			wantErr: "item_id is required",
		},
		{
			name:    "missing datatype",
			mutate:  func(r *Record) { r.Datatype = "" },
			wantErr: "datatype is required",
		},
		{
			name:    "missing fetched_at",
			mutate:  func(r *Record) { r.FetchedAt = time.Time{} },
			wantErr: "fetched_at is required",
		},
		{
			name:    "fragment count below fragments carried",
			mutate:  func(r *Record) { r.FragmentCount = 0 },
			wantErr: "fragment_count",
		},
		{
			name:    "fragment without a key",
			mutate:  func(r *Record) { r.Fragments[0].Key = "" },
			wantErr: "key is required",
		},
		{
			name: "duplicate fragment keys",
			mutate: func(r *Record) {
				r.Fragments = append(r.Fragments, r.Fragments[0])
				r.FragmentCount = 2
			},
			wantErr: "duplicate",
		},
		{
			name:    "fragment blob is not a digest",
			mutate:  func(r *Record) { r.Fragments[0].Blob = "deadbeef" },
			wantErr: "blob",
		},
		{
			name: "fragment blob digest passes",
			mutate: func(r *Record) {
				r.Fragments[0].Blob = strings.Repeat("ab", digestHexLen/2)
			},
		},
		{
			name:    "negative fragment size",
			mutate:  func(r *Record) { r.Fragments[0].Bytes = -1 },
			wantErr: "bytes",
		},
		{
			name: "no fragments is valid",
			mutate: func(r *Record) {
				r.Fragments = nil
				r.FragmentCount = 0
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := valid()
			if tt.mutate != nil {
				tt.mutate(r)
			}
			assertError(t, "Record.Validate", r.Validate(), tt.wantErr)
		})
	}
}
