// Digests: how "did this reference's content change" is answered without re-reading history.
//
// Every digest here hashes a domain tag and then each part length-prefixed, the same encoding
// internal/config and internal/delta use, so that no concatenation of two values can equal
// another. An unresolved record digests to the empty string, which is deliberately the same
// value a staleness mark leaves behind: both mean "this item has not consumed a payload yet"
// and both must compare unequal to any resolved payload.
package refs

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"github.com/maxwell-cudlitz/inget/internal/state"
)

// indexStored groups persisted edges by reference name and reports the shallowest staleness
// mark on any of them, which is the cascade depth this item is being processed at.
func indexStored(stored []state.RefEdge) (map[string]map[string]string, int) {
	prior := map[string]map[string]string{}
	depth := 0
	for _, e := range stored {
		if prior[e.Name] == nil {
			prior[e.Name] = map[string]string{}
		}
		prior[e.Name][e.Key] = e.Fingerprint
		if e.StaleDepth > 0 && (depth == 0 || e.StaleDepth < depth) {
			depth = e.StaleDepth
		}
	}
	return prior, depth
}

// sameDigests reports whether two key-to-digest maps agree. A marked edge reads back with an
// empty digest, so a mark is a difference and forces the dependent views to regenerate.
func sameDigests(prior, current map[string]string) bool {
	if len(prior) != len(current) {
		return false
	}
	for key, digest := range current {
		if prior[key] != digest {
			return false
		}
	}
	return true
}

// digestRecord hashes one resolved record's payload.
func digestRecord(name, key string, rec Record) string {
	if !rec.resolved() {
		return ""
	}
	parts := []string{name, key}
	for _, field := range sortedKeys(rec.Fields) {
		parts = append(parts, field, rec.Fields[field])
	}
	return combine("ref", parts...)
}

// digestOf hashes a reference's whole key-to-digest map, which is one reference's
// contribution to the level-2 input hash of every view.
func digestOf(name string, current map[string]string) string {
	parts := []string{name}
	for _, key := range sortedKeys(current) {
		parts = append(parts, key, current[key])
	}
	return combine("refset", parts...)
}

// combine hashes a domain tag and length-prefixed parts, returning "" for no parts so that a
// datatype with no metadata-injected references contributes nothing to its views' input hash.
func combine(domain string, parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(domain))
	for _, p := range parts {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(p)))
		h.Write(length[:])
		h.Write([]byte(p))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// sortedKeys returns a map's keys in sorted order, which is what makes every digest here
// independent of map iteration order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
