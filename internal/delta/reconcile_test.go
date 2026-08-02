package delta

import (
	"testing"
)

func TestReconcile(t *testing.T) {
	tests := []struct {
		name      string
		cached    map[string]string
		incoming  []IncomingFragment
		wantAdded []string
		wantMod   []string
		wantUnch  []string
		wantDel   []string
	}{
		{
			name:      "all added",
			cached:    map[string]string{},
			incoming:  []IncomingFragment{{Key: "a", Fingerprint: "fp1"}, {Key: "b", Fingerprint: "fp2"}},
			wantAdded: []string{"a", "b"},
		},
		{
			name:    "all deleted",
			cached:  map[string]string{"a": "fp1", "b": "fp2"},
			wantDel: []string{"a", "b"},
		},
		{
			name:     "all unchanged",
			cached:   map[string]string{"a": "fp1", "b": "fp2"},
			incoming: []IncomingFragment{{Key: "a", Fingerprint: "fp1"}, {Key: "b", Fingerprint: "fp2"}},
			wantUnch: []string{"a", "b"},
		},
		{
			name:   "all modified",
			cached: map[string]string{"a": "fp1", "b": "fp2"},
			incoming: []IncomingFragment{
				{Key: "a", Fingerprint: "fp1-new"},
				{Key: "b", Fingerprint: "fp2-new"},
			},
			wantMod: []string{"a", "b"},
		},
		{
			name:   "mixed",
			cached: map[string]string{"a": "fp1", "b": "fp2", "c": "fp3"},
			incoming: []IncomingFragment{
				{Key: "a", Fingerprint: "fp1"},     // unchanged
				{Key: "b", Fingerprint: "fp2-new"}, // modified
				{Key: "d", Fingerprint: "fp4"},     // added
			},
			wantAdded: []string{"d"},
			wantMod:   []string{"b"},
			wantUnch:  []string{"a"},
			wantDel:   []string{"c"},
		},
		{
			name: "both empty",
		},
		{
			name:      "nil cached",
			incoming:  []IncomingFragment{{Key: "x", Fingerprint: "fpx"}},
			wantAdded: []string{"x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Reconcile(tt.cached, tt.incoming)
			assertSlice(t, "Added", d.Added, tt.wantAdded)
			assertSlice(t, "Modified", d.Modified, tt.wantMod)
			assertSlice(t, "Unchanged", d.Unchanged, tt.wantUnch)
			assertSlice(t, "Deleted", d.Deleted, tt.wantDel)
		})
	}
}

func assertSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if len(got) != len(want) {
		t.Errorf("%s: got %v, want %v", label, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %q, want %q", label, i, got[i], want[i])
		}
	}
}
