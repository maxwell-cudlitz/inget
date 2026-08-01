# Artifact Envelope Specification — schema_version 1

The wire contract between `inget-fetch` (producer) and `inget` (consumer). It is
versioned independently of either binary because it is the only coupling between them.

A consumer MUST reject a `schema_version` it does not recognize. A producer MUST NOT
change the meaning of an existing field without incrementing `schema_version`. Adding
optional fields is backward compatible and does not require an increment.

## Layout

```
<artifacts.url>/
├── blobs/
│   └── <sha[0:2]>/<sha[2:4]>/<sha>            immutable fragment content, zstd
└── runs/
    └── <source>/<datatype>/<run_id>/
        ├── manifest.json
        ├── records-00000.jsonl.zst
        ├── records-00001.jsonl.zst
        └── _COMMIT                            zero bytes, written LAST
```

- `<datatype>` has its `/` replaced by `_` in the path (`github/repo` → `github_repo`).
- `<run_id>` is a ULID, so lexical ordering is chronological ordering.
- `<sha>` is the lowercase hex SHA-256 of the **uncompressed** fragment content.

## Commit protocol

1. Write all new blobs. Skip any blob where `HasBlob(sha)` is already true.
2. Write all record shards.
3. Write `manifest.json`.
4. Write `_COMMIT`.

A consumer MUST ignore any run directory without `_COMMIT`. Nothing is ever mutated or
deleted in place; garbage collection is a separate, explicit operation.

Rationale: this is Apache Iceberg's optimistic-commit pattern reduced to its minimum.
Readers only ever observe complete runs, so an interrupted fetch is inert rather than
corrupting. See https://iceberg.apache.org/spec/

## Blob store

Fragment content is content-addressed, so identical content is stored once regardless
of how many items or runs reference it. `inget-fetch` checks existence before writing,
which means a repository where one file changed uploads exactly one blob.

- Compression: zstd (`klauspost/compress/zstd`, pure Go).
- Two levels of two-hex-character sharding keeps directory fanout manageable on
  filesystem backends and prefix-balanced on object stores.
- Content larger than `artifacts.blob_max_bytes` is not stored; the fragment records
  `truncated: true` and carries no `blob`.

## manifest.json

```json
{
  "schema_version": 1,
  "run_id": "01K1AB2CDEFGHJKMNPQRSTVWXY",
  "created_at": "2026-07-31T20:41:07Z",
  "producer": "inget-fetch/0.1.0",
  "source": "github",
  "datatype": "github/repo",
  "scope": "full",
  "domain_hash": "sha256:9f2c…",
  "config_hash": "sha256:41ab…",
  "record_shards": [
    {
      "path": "records-00000.jsonl.zst",
      "records": 812,
      "bytes_compressed": 9418233,
      "bytes_uncompressed": 133214887,
      "sha256": "3d1e…"
    }
  ],
  "counts": {
    "items": 812,
    "items_skipped_unchanged": 7411,
    "fragments": 194322,
    "blobs_written": 1841,
    "blobs_reused": 192481
  },
  "tombstones": ["maxwellcudlitz/retired-repo"],
  "truncated": false,
  "warnings": [
    "maxwellcudlitz/monorepo: fragment count 4211 exceeded max_fragments_per_item=2000"
  ]
}
```

### Fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schema_version` | int | yes | Envelope version. Consumers reject unknown values. |
| `run_id` | string | yes | ULID of the producing run. |
| `created_at` | RFC 3339 | yes | Manifest write time. |
| `producer` | string | yes | `name/version` of the writing binary. |
| `source` | string | yes | Source name from config. |
| `datatype` | string | yes | Datatype name, e.g. `github/repo`. |
| `scope` | enum | yes | `full` or `partial`. **Load-bearing — see below.** |
| `domain_hash` | string | yes | SHA-256 of the resolved domain config. A change means the enumerated set may differ for reasons unrelated to source data. |
| `config_hash` | string | yes | SHA-256 of the effective config subtree for this source and datatype. |
| `record_shards` | array | yes | Ordered shards. `sha256` is over compressed bytes for integrity checking. |
| `counts` | object | yes | Observability; not consumed for correctness. |
| `tombstones` | []string | yes when `scope=full` | Item IDs present in state but absent from the source. MUST be empty when `scope=partial`. |
| `truncated` | bool | yes | True if enumeration stopped early (`--limit`, budget exhaustion, fatal pagination error). Suppresses tombstone processing even when `scope=full`. |
| `warnings` | []string | yes | Non-fatal per-item problems. |

### The `scope` field

`scope` is the field that makes one pipeline safe for both scheduled full syncs and
event-driven partial updates.

- `full` — the producer enumerated the entire configured domain. An item in state but
  absent from the records is deleted at the source, and appears in `tombstones`.
- `partial` — the producer processed a caller-supplied subset (`--only`,
  `--event-file`, `--since`). Absence implies **nothing**. `tombstones` MUST be empty
  and a consumer MUST NOT infer deletions.

Without this distinction, a webhook-triggered run carrying a single changed item would
be indistinguishable from "every other item was deleted", and the consumer would delete
the entire index.

`truncated: true` degrades a `full` run to partial semantics for deletion purposes,
because an interrupted enumeration cannot distinguish "absent" from "not reached".

## Record shards

One JSON object per line, zstd-compressed, targeting `artifacts.shard_target_bytes`
uncompressed (default 128 MiB) before rolling to the next shard.

Object storage penalizes many small objects: per-request latency and cost dominate and
listing becomes O(n). AWS S3 Tables compaction targets 512 MiB and refuses to go below
64 MiB for this reason. 128 MiB sits inside that band while keeping a single shard
cheap to re-read.
See https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-maintenance.html

```json
{
  "schema_version": 1,
  "datatype": "github/repo",
  "item_id": "maxwellcudlitz/inget",
  "fingerprint": "2026-07-31T18:04:11Z",
  "fetched_at": "2026-07-31T20:39:52Z",
  "metadata": {
    "name": "inget",
    "full_name": "maxwellcudlitz/inget",
    "description": "Generic ingestion, enrichment, and embedding pipeline",
    "language": "Go",
    "topics": "rag,embeddings,golang",
    "url": "https://github.com/maxwellcudlitz/inget",
    "default_branch": "main",
    "updated_at": "2026-07-31T18:04:11Z",
    "visibility": "public"
  },
  "fragments": [
    {
      "key": "README.md",
      "fingerprint": "b7e2f1c0a9…",
      "blob": "3f8a91c2…",
      "bytes": 4211,
      "tier": 0,
      "mime": "text/markdown",
      "truncated": false,
      "meta": {}
    },
    {
      "key": "internal/delta/reconcile.go",
      "fingerprint": "9c4d22ab…",
      "blob": "88bd0e17…",
      "bytes": 6104,
      "tier": 3,
      "mime": "text/x-go",
      "truncated": false,
      "meta": {}
    }
  ],
  "fragment_count": 5211,
  "fragment_truncated": false
}
```

### Record fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schema_version` | int | yes | Matches the manifest. |
| `datatype` | string | yes | Redundant with the manifest; makes a shard self-describing. |
| `item_id` | string | yes | Stable source identity. Unique within a datatype. |
| `fingerprint` | string | yes | Level-0 change token: `pushed_at`, `updated_at`, or ETag. Compared for inequality only, never ordered. |
| `fetched_at` | RFC 3339 | yes | When the producer read the item. |
| `metadata` | object | yes | Flat string map. Becomes vector metadata and template variables. No nesting, so it maps cleanly to `map[string]string` and JSONB. |
| `fragments` | array | yes | May be empty. A datatype with no substructure emits exactly one fragment covering the whole item. |
| `fragment_count` | int | yes | Total fragments discovered, before any cap. |
| `fragment_truncated` | bool | yes | True if `max_fragments_per_item` dropped some. |

### Fragment fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `key` | string | yes | Stable within the item: `cmd/root.go`, `column:status`, `update:8891`, `path#0` for sub-file splits. |
| `fingerprint` | string | yes | Level-1 change token. Git blob SHA, hash of raw column JSON, or content SHA-256. |
| `blob` | string | no | SHA-256 of uncompressed content in the blob store. Absent when content was not retained. |
| `bytes` | int | yes | Uncompressed size. |
| `tier` | int | yes | Composition priority: 0 docs, 1 entrypoints, 2 config, 3 source, 4 other. |
| `mime` | string | no | Best-effort content type. |
| `truncated` | bool | yes | True if content exceeded `blob_max_bytes`. |
| `meta` | object | no | Datatype-specific extras, e.g. `column_title`, `column_type`. |

## Fingerprint semantics

A fingerprint is an opaque change token. Consumers compare for **inequality only** and
never order or parse them. Consequences:

- Clock skew on an `updated_at` fingerprint causes a redundant fetch or re-derivation,
  never a skipped change.
- Empty fingerprint means "unknown"; the consumer must treat the item or fragment as
  changed.
- Producers should prefer exact content hashes where they are free. GitHub's recursive
  tree API supplies a blob SHA per path without downloading content, which is why
  `github/repo` gets exact per-file change detection at no extra API cost.

## Consumer obligations

1. Ignore run directories without `_COMMIT`.
2. Reject unknown `schema_version`.
3. Verify shard `sha256` before parsing; a mismatch fails the run rather than
   processing partial data.
4. Process `tombstones` only when `scope == "full" && truncated == false`.
5. Treat an empty `fingerprint` as changed.
6. Treat `metadata` as untrusted input. It originates from third-party systems and
   flows into LLM prompts; see the security section of `feature-design.md`.

## Garbage collection

`inget state gc` performs, in order:

1. Delete run directories older than `retention.runs` (default 30 days) that are not
   the latest committed run for their source and datatype.
2. Delete blobs referenced by no retained run and no live `fragments` row.
3. Delete `derivations` rows whose fragment has `missing_runs` above
   `retention.missing_runs` (default 3).

GC never deletes a blob referenced by live state, so a stale run directory can always
be replayed until it is itself collected.

## Versioning history

| Version | Change |
|---|---|
| 1 | Initial specification. |
