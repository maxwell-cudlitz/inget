// Enricher signature builder.
//
// A signature uniquely identifies the enricher configuration for cache invalidation.
// Every field that can influence enricher output is a struct field, enforcing completeness
// at compile time (D2). The encoding follows the same length-prefixed sorted convention
// used in internal/config/hash.go: each field contributes a 32-bit big-endian length
// followed by its bytes, and fields are written in sorted key order so the digest is
// independent of struct declaration order.
package delta

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
)

// SignatureInput holds every parameter that can affect enricher output.
type SignatureInput struct {
	ModelID         string
	PromptBytes     []byte
	MaxInputChars   int
	MaxOutputTokens int
	SchemaVersion   int
	EnricherOptions map[string]string
}

// BuildSignature returns a deterministic SHA-256 hash over all fields of s, using
// length-prefixed sorted encoding.
func BuildSignature(s SignatureInput) string {
	// Build key=value pairs for all fields so we can sort them.
	pairs := []kv{
		{key: "max_input_chars", value: strconv.Itoa(s.MaxInputChars)},
		{key: "max_output_tokens", value: strconv.Itoa(s.MaxOutputTokens)},
		{key: "model_id", value: s.ModelID},
		{key: "prompt_bytes", value: string(s.PromptBytes)},
		{key: "schema_version", value: strconv.Itoa(s.SchemaVersion)},
	}

	// Enricher options are sorted and prefixed to keep them distinct from top-level keys.
	if len(s.EnricherOptions) > 0 {
		keys := make([]string, 0, len(s.EnricherOptions))
		for k := range s.EnricherOptions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pairs = append(pairs, kv{key: "opt." + k, value: s.EnricherOptions[k]})
		}
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })

	var buf []byte
	for _, p := range pairs {
		buf = appendPrefixed(buf, p.key)
		buf = appendPrefixed(buf, p.value)
	}

	digest := sha256.Sum256(buf)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// kv is a key/value pair used during signature encoding.
type kv struct {
	key   string
	value string
}

// appendPrefixed writes a length-prefixed string to dst.
func appendPrefixed(dst []byte, s string) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}
