// Fragment reconciliation — compares cached state against incoming fragments to produce
// a Delta describing what changed.
//
// The algorithm is a single-pass set comparison: incoming fragments are indexed by key,
// then each cached entry is checked against the index. Keys found in both sets with the
// same fingerprint are Unchanged; those with a different fingerprint are Modified. Keys
// in cached but absent from incoming are Deleted, and keys in incoming but absent from
// cached are Added.
package delta

import "sort"

// IncomingFragment represents a single fragment from the current artifact run.
type IncomingFragment struct {
	Key         string
	Fingerprint string
}

// Delta describes the difference between the cached fragment set and the incoming one.
type Delta struct {
	Added     []string // fragment keys new in this run
	Modified  []string // fragment keys whose fingerprint changed
	Unchanged []string // fragment keys with same fingerprint
	Deleted   []string // fragment keys absent from incoming
}

// Reconcile compares the cached fragment fingerprints against the incoming set and
// returns a Delta partitioning every key into exactly one category.
func Reconcile(cached map[string]string, incoming []IncomingFragment) Delta {
	var d Delta

	// Index incoming by key for O(1) lookup.
	inMap := make(map[string]string, len(incoming))
	for _, f := range incoming {
		inMap[f.Key] = f.Fingerprint
	}

	// Walk cached entries: classify as unchanged, modified or deleted.
	for key, cachedFP := range cached {
		inFP, present := inMap[key]
		switch {
		case !present:
			d.Deleted = append(d.Deleted, key)
		case inFP == cachedFP:
			d.Unchanged = append(d.Unchanged, key)
		default:
			d.Modified = append(d.Modified, key)
		}
	}

	// Walk incoming entries: anything not in cached is added.
	for _, f := range incoming {
		if _, present := cached[f.Key]; !present {
			d.Added = append(d.Added, f.Key)
		}
	}

	// Sort all slices for deterministic output.
	sort.Strings(d.Added)
	sort.Strings(d.Modified)
	sort.Strings(d.Unchanged)
	sort.Strings(d.Deleted)

	return d
}
