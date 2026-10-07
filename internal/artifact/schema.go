// Package artifact implements docs/artifact-envelope.md, the wire contract between
// inget-fetch (producer) and inget (consumer).
//
// The envelope is versioned independently of either binary because it is the only
// coupling between them, so this package owns the version constant, the on-disk layout,
// the commit protocol and the consumer obligations. Nothing here knows how items are
// fetched or enriched.
//
//	schema.go    the types and their JSON encoding
//	validate.go  the consumer obligations, enforced
//	path.go      the object key layout
//	runid.go     monotonic ULID run identifiers
//	codec.go     zstd framing, shared by blobs and shards
//	store.go     the content-addressed blob store
//	writer.go    shard rolling and the commit protocol
//	reader.go    run resolution, integrity checks and record streaming
//
// Types are plain data with JSON tags: the manifest and the shards are files other
// programs may read, so the tags are part of the contract and renaming one is a
// schema change.
package artifact

import "time"

// SchemaVersion is the envelope version this build produces and the only version it
// accepts. A consumer rejects anything else rather than guessing at field meanings; see
// Manifest.Validate and ErrUnsupportedSchema.
const SchemaVersion = 1

// Scope declares whether a run enumerated the whole configured domain. It is the field
// that makes one pipeline safe for both scheduled full syncs and event-driven partial
// updates (D6): full enumeration permits explicit tombstones for absent source items;
// partial enumeration permits none. Records may omit unchanged or failed items in either.
type Scope string

// The two scopes. Any other value is invalid.
const (
	ScopeFull    Scope = "full"
	ScopePartial Scope = "partial"
)

// Manifest describes one committed run. It is written second-to-last, immediately
// before the zero-byte _COMMIT marker that makes the run visible.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	CreatedAt     time.Time `json:"created_at"`
	Producer      string    `json:"producer"` // name/version of the writing binary
	Source        string    `json:"source"`
	Datatype      string    `json:"datatype"`
	Scope         Scope     `json:"scope"`

	// DomainHash covers the resolved domain config: a change means the enumerated item
	// set may differ for reasons unrelated to source data. ConfigHash covers the
	// effective subtree that governs how items become fragments and blobs. Both are
	// produced by internal/config and carried opaquely.
	DomainHash string `json:"domain_hash"`
	ConfigHash string `json:"config_hash"`

	RecordShards []Shard `json:"record_shards"`
	Counts       Counts  `json:"counts"`

	// Tombstones are item IDs the consumer should delete. Only a full, untruncated run
	// may carry them; see ProcessTombstones.
	Tombstones []string `json:"tombstones"`

	// Truncated reports that enumeration stopped early — a --limit, an exhausted
	// budget, a fatal pagination error. It degrades a full run to partial semantics
	// for deletion purposes, because an interrupted enumeration cannot distinguish
	// "absent" from "not reached".
	Truncated bool `json:"truncated"`

	// Warnings are non-fatal per-item problems, one human-readable line each.
	Warnings []string `json:"warnings"`
}

// Shard is one record file. SHA256 is over the compressed bytes as stored, so a
// consumer can verify integrity without decompressing first.
type Shard struct {
	Path              string `json:"path"` // relative to the run directory
	Records           int    `json:"records"`
	BytesCompressed   int64  `json:"bytes_compressed"`
	BytesUncompressed int64  `json:"bytes_uncompressed"`
	SHA256            string `json:"sha256"`
}

// Counts is observability only; nothing reads it for correctness. The writer fills in
// what it observes, and the producer supplies the rest through CommitInfo.
type Counts struct {
	Items                 int `json:"items"`
	ItemsSkippedUnchanged int `json:"items_skipped_unchanged"`
	Fragments             int `json:"fragments"`
	BlobsWritten          int `json:"blobs_written"`
	BlobsReused           int `json:"blobs_reused"`
}

// Record is one item, one JSON object per line of a shard. It repeats datatype and
// schema_version from the manifest so that a shard is self-describing.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	Datatype      string `json:"datatype"`
	ItemID        string `json:"item_id"` // stable source identity, unique per datatype

	// Fingerprint is the level-0 change token: pushed_at, updated_at or an ETag.
	// Compared for inequality only, never parsed or ordered. Empty means "unknown",
	// which the consumer must treat as changed.
	Fingerprint string    `json:"fingerprint"`
	FetchedAt   time.Time `json:"fetched_at"`

	// Metadata is a flat string map that becomes vector metadata and prompt template
	// variables. It is untrusted third-party input on its way into an LLM prompt.
	Metadata map[string]string `json:"metadata"`

	// Fragments may be empty. A datatype with no substructure emits exactly one
	// fragment covering the whole item.
	Fragments []Fragment `json:"fragments"`

	// FragmentCount is the total discovered before any cap, so a consumer can tell a
	// small item from a capped one; FragmentTruncated says the cap applied.
	FragmentCount     int  `json:"fragment_count"`
	FragmentTruncated bool `json:"fragment_truncated"`
}

// Fragment is one addressable piece of an item: a file, a column value, an update.
type Fragment struct {
	// Key is stable within the item: cmd/root.go, column:status, update:8891, or
	// path#0 for a sub-file split.
	Key string `json:"key"`

	// Fingerprint is the level-1 change token — a git blob SHA, a hash of raw column
	// JSON, or a content SHA-256. Inequality comparison only, as above.
	Fingerprint string `json:"fingerprint"`

	// Blob is the SHA-256 of the uncompressed content in the blob store, absent when
	// content was not retained.
	Blob  string `json:"blob,omitempty"`
	Bytes int64  `json:"bytes"` // uncompressed size

	// Tier is composition priority: 0 docs, 1 entrypoints, 2 config, 3 source,
	// 4 other.
	Tier int    `json:"tier"`
	MIME string `json:"mime,omitempty"`

	// Truncated reports that content exceeded artifacts.blob_max_bytes and so no blob
	// was stored.
	Truncated bool `json:"truncated"`

	// Meta carries datatype-specific extras such as column_title or column_type.
	Meta map[string]string `json:"meta,omitempty"`
}

// ProcessTombstones reports whether the consumer may act on Tombstones. Deletions are
// only inferable from a run that enumerated everything and finished doing so.
func (m *Manifest) ProcessTombstones() bool {
	return m.Scope == ScopeFull && !m.Truncated
}
