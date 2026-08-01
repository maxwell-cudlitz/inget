// Configuration hashing for the artifact manifest.
//
// Serialization is length-prefixed sorted key/value pairs, not canonical JSON (D15). Each
// pair contributes a 32-bit big-endian length followed by the bytes of the path, then the
// same for a type-tagged rendering of the value. Length prefixing makes the encoding
// unambiguous — no separator can appear inside a field — and the type tag keeps the
// string "1" distinct from the integer 1. Floats are rendered in Go's exact hexadecimal
// form, so no decimal rounding decision can move a hash.
//
// Values are hashed after decoding, from the typed struct rather than from viper's raw
// settings map. That matters: an environment override arrives as a string, so hashing raw
// settings would make `INGET_ARTIFACTS__BLOB_MAX_BYTES=1048576` a different config from
// the identical value in YAML.
//
// Two hashes reach the manifest (docs/artifact-envelope.md):
//
//   - DomainHash covers a source's resolved `domain` block. A change means the enumerated
//     item set may differ for reasons unrelated to source data.
//   - ConfigHash covers the effective subtree that governs producing artifacts for one
//     source and datatype: the whole source entry, the whole datatype entry, and the
//     artifact limits that decide how items are split into fragments and blobs.
package config

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// DomainHash returns the hash of the named source's resolved domain block.
func (c *Config) DomainHash(source string) (string, error) {
	src, ok := c.Source(source)
	if !ok {
		return "", fmt.Errorf("hashing domain: no source named %q", source)
	}
	return sum(flatten("domain", reflect.ValueOf(src.Domain))), nil
}

// ConfigHash returns the hash of the effective config subtree for one source and
// datatype. It fails when either name is unknown, or when the datatype does not read the
// named source, since a manifest pairing them would be meaningless.
func (c *Config) ConfigHash(source, datatype string) (string, error) {
	src, ok := c.Source(source)
	if !ok {
		return "", fmt.Errorf("hashing config: no source named %q", source)
	}
	dt, ok := c.Datatype(datatype)
	if !ok {
		return "", fmt.Errorf("hashing config: no datatype named %q", datatype)
	}
	if dt.Source != source {
		return "", fmt.Errorf("hashing config: datatype %q reads source %q, not %q", datatype, dt.Source, source)
	}

	leaves := flatten("source", reflect.ValueOf(*src))
	leaves = append(leaves, flatten("datatype", reflect.ValueOf(*dt))...)
	leaves = append(leaves, flatten("artifacts", reflect.ValueOf(fragmentation{
		ShardTargetBytes:    c.Artifacts.ShardTargetBytes,
		BlobMaxBytes:        c.Artifacts.BlobMaxBytes,
		MaxFragmentsPerItem: c.Artifacts.MaxFragmentsPerItem,
		Compression:         c.Artifacts.Compression,
	}))...)
	return sum(leaves), nil
}

// fragmentation is the subset of the artifacts block that changes what a fetch produces.
// The store URL is excluded on purpose: moving the bucket does not change the content.
type fragmentation struct {
	ShardTargetBytes    int64  `mapstructure:"shard_target_bytes"`
	BlobMaxBytes        int64  `mapstructure:"blob_max_bytes"`
	MaxFragmentsPerItem int    `mapstructure:"max_fragments_per_item"`
	Compression         string `mapstructure:"compression"`
}

// sum sorts leaves by path and returns the prefixed SHA-256 of their length-prefixed
// encoding. Sorting is what makes the digest independent of key order in the source YAML.
func sum(leaves []leaf) string {
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].path < leaves[j].path })

	var buf []byte
	for _, l := range leaves {
		buf = appendField(buf, l.path)
		buf = appendField(buf, encodeValue(l.value))
	}
	digest := sha256.Sum256(buf)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// appendField writes one length-prefixed field.
func appendField(dst []byte, s string) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}

// encodeValue renders a scalar with a one-letter type tag. Floats use strconv's 'x'
// format, which is exact: every float64 has one representation and no digits are lost.
func encodeValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Bool:
		return "b:" + strconv.FormatBool(v.Bool())
	case reflect.String:
		return "s:" + v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "i:" + strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "u:" + strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return "f:" + strconv.FormatFloat(v.Float(), 'x', -1, 64)
	default:
		// flatten only emits the kinds above; this keeps the switch total.
		return "?:" + fmt.Sprint(v.Interface())
	}
}
