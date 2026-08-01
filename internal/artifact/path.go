// The object key layout from docs/artifact-envelope.md.
//
//	blobs/<sha[0:2]>/<sha[2:4]>/<sha>              immutable fragment content
//	runs/<source>/<datatype>/<run_id>/manifest.json
//	runs/<source>/<datatype>/<run_id>/records-00000.jsonl.zst
//	runs/<source>/<datatype>/<run_id>/_COMMIT
//
// Keys are always "/"-separated regardless of the backend: gocloud.dev/blob defines keys
// as slash-delimited strings and the filesystem driver translates them. Two levels of
// two-hex-character sharding keeps directory fanout manageable on filesystems and
// prefix-balanced on object stores.
//
// A datatype name contains a "/" (github/repo), which would otherwise add a directory
// level and make run listing ambiguous, so it is flattened to "_" in keys. The
// unflattened name stays in the manifest, which is what consumers read.
package artifact

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// Fixed names inside a run directory.
const (
	// ManifestName is written second-to-last.
	ManifestName = "manifest.json"
	// CommitName is the zero-byte marker written last. A run directory without it is
	// invisible to every consumer.
	CommitName = "_COMMIT"

	blobsPrefix = "blobs/"
	runsPrefix  = "runs/"

	// digestHexLen is the length of a hex-encoded SHA-256.
	digestHexLen = 64
)

// blobKey returns the store key for content with the given hex digest. The digest is
// assumed valid; callers reach this through checkDigest.
func blobKey(digest string) string {
	return blobsPrefix + digest[0:2] + "/" + digest[2:4] + "/" + digest
}

// runPrefix returns the key prefix of one run directory, trailing slash included.
func runPrefix(source, datatype, runID string) string {
	return datatypePrefix(source, datatype) + runID + "/"
}

// datatypePrefix returns the key prefix holding every run for one source and datatype.
func datatypePrefix(source, datatype string) string {
	return runsPrefix + source + "/" + flattenDatatype(datatype) + "/"
}

// flattenDatatype renders a datatype name as a single key segment: github/repo becomes
// github_repo.
func flattenDatatype(datatype string) string {
	return strings.ReplaceAll(datatype, "/", "_")
}

// shardName returns the file name of the index-th record shard. Five digits keep lexical
// and numeric order identical up to 100k shards, which at the default 128 MiB target is
// far past any run this pipeline will produce.
func shardName(index int, c Compression) string {
	return fmt.Sprintf("records-%05d.jsonl%s", index, c.extension())
}

// checkDigest rejects anything that is not a lowercase hex SHA-256. Digests are used
// directly as key segments, so a malformed one from an untrusted manifest could otherwise
// address a key outside the blob namespace.
func checkDigest(digest string) error {
	if len(digest) != digestHexLen {
		return fmt.Errorf("digest %q: want %d hex characters, got %d", digest, digestHexLen, len(digest))
	}
	if strings.ToLower(digest) != digest {
		return fmt.Errorf("digest %q: want lowercase hex", digest)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("digest %q: %w", digest, err)
	}
	return nil
}

// checkShardPath rejects a manifest shard path that is anything other than a plain file
// name inside the run directory. A manifest is data read from a store that may be shared,
// so a path of "../../etc/passwd" must not become a read.
func checkShardPath(path string) error {
	if path == "" || strings.ContainsAny(path, "/\\") || path == "." || path == ".." {
		return fmt.Errorf("shard path %q: want a file name inside the run directory", path)
	}
	return nil
}
