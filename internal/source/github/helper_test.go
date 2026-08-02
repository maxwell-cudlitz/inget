// Small assertions helpers shared by the connector tests. The fake API itself lives in fake_test.go.
package github

import (
	"testing"

	"github.com/maxwellcudlitz/inget/internal/source"
)

// collect drains a List call into a slice.
func collect(t *testing.T, conn *Connector, q source.ListQuery) []source.Ref {
	t.Helper()
	var refs []source.Ref
	err := conn.List(t.Context(), Datatype, q, func(ref source.Ref) error {
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	return refs
}

// refIDs returns the IDs of refs, for comparison against an expected set.
func refIDs(refs []source.Ref) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.ID)
	}
	return out
}

// fragmentKeys returns the keys of fragments in the order they were produced.
func fragmentKeys(frags []source.Fragment) []string {
	out := make([]string, 0, len(frags))
	for _, f := range frags {
		out = append(out, f.Key)
	}
	return out
}

// findFragment returns the fragment with a key, or fails the test.
func findFragment(t *testing.T, frags []source.Fragment, key string) source.Fragment {
	t.Helper()
	for _, f := range frags {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no fragment keyed %q; got %v", key, fragmentKeys(frags))
	return source.Fragment{}
}
