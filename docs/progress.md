# Implementation Progress

Handoff record for the one-step-per-session cadence. A session should be able to orient
from this file plus the named design line ranges, without reading all 1353 lines of
`feature-design.md`.

## Session protocol

1. Read `AGENTS.md`, then this file.
2. Read the step's entry in `docs/implementation-plan.md` and only the design sections
   listed in the map below.
3. Implement, then make `make build test lint` green.
4. Update the status table, any deviations, and any decisions made.
5. Commit as `step N: <goal>`.

## Status

| Step | Goal | State |
|---|---|---|
| 1 | Project skeleton | **done** |
| 2 | Configuration | **done** |
| 3 | Artifact envelope and blob store | **done** |
| 4 | State store | **done** |
| 5 | Delta engine | **done** |
| 6 | Model clients | **done** |
| 7 | pgvector destination | not started |
| 8 | Enrichment pipeline | not started |
| 9 | GitHub connector | not started |
| 10 | Monday connector | not started |
| 11 | Reference resolution | not started |
| 12 | Quality harness | not started |
| 13 | Reindex and operations | not started |
| 14 | Release and documentation | not started |

Sizing note: steps 5, 8, 9 and 10 carry more surface than one session should hold. Plan on
splitting each into an implementation session and a test-hardening session, so expect
roughly 18 sessions rather than 14. Steps 1–8 need no network and no credentials; schedule
9 and 10 for when `INGET_GITHUB_TOKEN` and `INGET_MONDAY_TOKEN` are available.

## Design section map

`feature-design.md` line ranges to read per step. Everything else can stay unread.

| Step | Sections | Lines |
|---|---|---|
| 1 | Architecture, Repository Layout, Observability, D15 | 51–118, 1307–1342, 1286–1306, 539–584 |
| 2 | Configuration, D2 (hashing) | 752–940, 157–180 |
| 3 | `artifact-envelope.md` in full, D5, D6 | all, 258–295 |
| 4 | State schema, D13 | 587–688, 493–515 |
| 5 | D1, D2, D3, D4, Enrichment Pipeline | 121–257, 1213–1240 |
| 6 | D10, API/Interface | 366–419, 941–1103 |
| 7 | D7, D8, D9, Destination schema | 296–365, 689–751 |
| 8 | D11, Enrichment Pipeline, Edge Cases | 420–452, 1213–1264 |
| 9 | `github/repo`, D6, Security considerations | 1144–1165, 280–295, 1265–1285 |
| 10 | `monday/item` | 1166–1212 |
| 11 | D12 | 453–492 |
| 12 | D14, Testing Strategy | 516–538, 1343–1366 |
| 13 | Edge Cases, `artifact-envelope.md` GC | 1241–1264, 235–247 |
| 14 | Non-Goals, Repository Layout | 1367–1375, 1307–1342 |

These offsets are valid for `feature-design.md` at 1375 lines. Editing that file shifts
everything below the edit, so regenerate the map with
`grep -n '^#\{2,3\} ' docs/feature-design.md` whenever the design changes.

## Step 1 record

Implemented: module `github.com/maxwellcudlitz/inget`; `internal/logging` (slog JSON/text,
`LOG_LEVEL`/`LOG_FORMAT`/`LOG_DESTINATION`/`LOG_REDACT`, key-pattern redaction);
`internal/cli` (shared root, `version` with ldflags stamps and build-info fallback, single
outermost error handler); both `cmd/` entrypoints; `Makefile`; `.golangci.yml`
(golangci-lint v2 schema, verified with `golangci-lint config verify`); CI workflow;
Apache-2.0 `LICENSE`; `.gitignore`.

Verified: `bin/inget` and `bin/inget-fetch` build, both print a version, `go vet` clean,
`golangci-lint run` reports 0 issues, `go test -race ./...` passes, `gofmt` clean. CI has
not been observed running — there is no remote push yet.

Deviations from the design:

- Added `internal/cli`, which the design's layout does not list. Two binaries need
  identical root scaffolding; duplicating it across both mains would violate DRY.
- Deferred: config-driven log options. `PersistentPreRunE` currently calls
  `logging.Setup(logging.Default())`; step 2 replaces the argument with the loaded `log`
  block. Environment overrides already work, so behavior is not blocked.

Choices made where the plan was silent:

- Version metadata is injected with `-ldflags -X` and falls back to
  `debug.ReadBuildInfo`, so `go run` and `go install` builds still report truthfully.
- `make lint` warns and skips when `golangci-lint` is absent rather than failing; CI
  enforces it. `make lint-install` installs the pinned v2.12.2.
- CI additionally checks `gofmt` and that `go mod tidy` is a no-op.
- Pinned: cobra v1.10.1, golangci-lint v2.12.2, actions/checkout v7, actions/setup-go v7,
  golangci-lint-action v9.

## Step 2 record

Implemented: `internal/config` (schema for the documented file, viper layering with
`config.local.yaml` beside the base file, `INGET_` prefix with `__` nesting, strict
decoding, day-aware `Duration`, validation, secret indirection, config and domain
hashing); root `config.yaml` with the documented defaults; `--config` / `INGET_CONFIG` on
both binaries; `logging.Options.Validate` so the config layer and the logger agree on the
permitted level, format and destination.

Files: `schema.go` (types), `lookup.go` (name lookups and derived values), `duration.go`,
`load.go` (layering, strict decode, list-element defaults), `log.go` (`LoadLog`),
`secret.go`, `flatten.go` (one reflection walk), `hash.go`, `validate.go` (globals plus
the shared accumulator), `validate_lists.go` (referential integrity, globs, dimension
bounds). Fixtures `testdata/minimal.yaml` and `testdata/reordered.yaml`.

Verified: `make build test lint` green; `golangci-lint run` 0 issues; `go mod tidy` a
no-op; `./bin/inget --config config.yaml` loads the shipped file, and an invalid log level
in it exits 1 with the reason.

Deviations from the design and the plan:

- **Env names are computed here, not by viper's key replacer.** `BindEnv` is called with
  an explicit second argument from `config.EnvName`, because viper applies
  `SetEnvKeyReplacer` on the `AutomaticEnv` path but not to the one-argument `BindEnv`
  form. The replacer is still set, so both paths agree.
- **doublestar pinned at step 2, not step 5.** Glob syntax validation is a step 2
  acceptance item, and validating with `path.Match` while the delta engine matches with
  doublestar would accept patterns the matcher later rejects. D15's "pin when the step
  needs it" is satisfied: the dependency is used now. `go-viper/mapstructure/v2` is also
  direct, for the decode hooks.
- **Driver, enricher and resolver names are not enum-validated.** They are registry keys
  owned by `internal/source`, `internal/enrich` and `internal/destination`; validating
  them in config would mean editing config to add an implementation. Config checks that
  they are named and non-empty. Closed sets that belong to the schema — log fields,
  compression, `state.driver`, storage, granularity, `compose.order`, `inject_as` — are
  enum-validated.
- **Source `domain` and `limits` stay raw maps.** Their keys differ per driver, so the
  connector decodes them. `DomainHash` hashes the map, which is what the manifest needs.
- **No in-code defaults for the global blocks.** Only list-element settings get defaults
  in code (`compose.order`, `compose.max_chars`, `drift_threshold`), because viper cannot
  address list elements and so no layer can supply them. Everything else is required by
  validation, which keeps one source of truth: the shipped `config.yaml`.

Choices made where the plan was silent:

- `drift_threshold` is `*float64` so an explicit `0` — re-embed on any textual change —
  stays distinguishable from an absent key. `normalize` fills the default in before
  hashing, so a hash always reflects effective behavior.
- Secrets are snapshotted into an unexported map at load and read with `cfg.Secret(ref)`.
  A missing variable is not a load failure; it fails at the point of use, so a GitHub-only
  run does not require `INGET_MONDAY_TOKEN`. `Config.String` is implemented because `fmt`
  prints unexported fields with `%+v`; a test asserts a formatted config cannot leak a
  value. The cost is that `%+v` no longer dumps the struct.
- `ConfigHash` covers the source entry, the datatype entry, and the artifact limits that
  change what a fetch produces. `artifacts.url` is excluded: moving the bucket does not
  change content. Encoding is length-prefixed sorted pairs with a type tag per value and
  floats in `strconv` `'x'` form, so no decimal or separator ambiguity can move a hash.
- Validation reports every problem via `errors.Join` rather than failing on the first, and
  paths in messages name list elements (`datatypes[github/repo].views[role]`) rather than
  indices.
- `version` still skips the logger hook, as step 1 decided, so `LoadLog` only matters for
  the other commands; it tolerates a missing file but not a malformed one.
- No `inget config` command was added. `config.Load` has no CLI caller yet — step 4 is the
  first command that needs the whole file, and `inget plan` (step 8) is where hashes and
  signatures become visible. Adding a command now would be surface the design does not
  specify.

Known limitation, asserted by a test rather than worked around: list elements are not
env-overridable (`INGET_SOURCES__0__NAME` does nothing), and overriding a list through
`config.local.yaml` replaces the element rather than merging into it.

## Step 3 record

Implemented: `internal/artifact`, a round-trippable implementation of
`docs/artifact-envelope.md` schema_version 1. `Manifest`, `Shard`, `Counts`, `Record` and
`Fragment` with the documented JSON tags; the consumer obligations as `Manifest.Validate`
and `Record.Validate`; the key layout including datatype flattening (`github/repo` →
`github_repo`); monotonic ULID run IDs; a content-addressed blob store over
`gocloud.dev/blob` with `file://`, `s3://` and `gs://` registered; zstd framing shared by
blobs and shards; a shard writer that rolls at `shard_target_bytes` uncompressed and
reports per-shard compressed size and SHA-256; the commit protocol with `_COMMIT` last;
and a reader that resolves `latest`, rejects unknown `schema_version`, verifies shard
digests, and streams records.

Files: `schema.go` (types), `validate.go` (obligations), `path.go` (keys, digest and shard
path checks), `runid.go`, `codec.go` (zstd, magic sniffing), `store.go` (options, open,
blob get/put/has), `shard.go` (streaming shard writer), `writer.go` (run writer and
commit), `reader.go` (run resolution and record streaming), `config.go` (the one bridge to
`internal/config`), `drivers.go` (backend registration).

Pinned: `gocloud.dev` v0.46.0, `github.com/klauspost/compress` v1.19.1,
`github.com/oklog/ulid/v2` v2.1.2. All pure Go: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`
cross-compiles both binaries.

Verified: `make build test lint` green, `golangci-lint run` 0 issues, `go mod tidy` a
no-op. The acceptance tests assert 10k records across 61 shards read back identically and
in order; a run whose `_COMMIT` was removed is skipped by `LatestRun`, `ListRuns` and
`OpenRun` even though it sorts newest; an overwritten shard fails the read before any
record is delivered; a second `PutBlob` of the same content writes nothing and leaves the
object's mtime untouched; `schema_version` 99 is refused in both the manifest and a record.

Decisions made where the specification left room:

- **Shard verification is per shard, not per run.** The spec says to verify before
  parsing; the reader reads a compressed shard into memory, hashes it, and only then
  decodes. Verifying every shard of a run before delivering the first record would mean
  transferring the whole run twice. Records from earlier shards can therefore reach the
  consumer before a later shard is found corrupt, which the per-item transactional
  checkpointing of step 8 already tolerates.
- **`GetBlob` verifies the digest it was asked for.** The envelope only mandates shard
  verification. Blobs are capped at `blob_max_bytes`, so hashing on read is cheap, and it
  turns silent corruption into a failed run instead of a poisoned embedding.
- **Blob decoding sniffs the zstd magic number** rather than trusting
  `artifacts.compression`. A content-addressed store is long-lived and may hold blobs
  written under either setting. The digest check is what makes the sniff safe: raw content
  that happens to start with the magic bytes fails to decode as a frame, falls back to raw,
  and still verifies. Shards are unambiguous — the manifest records each file name and the
  extension names the codec — so `compression: none` produces `records-00000.jsonl`.
- **The writer stamps and normalizes.** `schema_version`, `datatype` and, when unset,
  `fragment_count` come from the run; nil `fragments` and `metadata` become `[]` and `{}`.
  A record whose datatype disagrees with the run is rejected rather than rewritten, because
  a shard that contradicts its manifest is unfixable after the fact.
- **`Counts` is split.** The writer fills in what it observes (`items`, `fragments`); the
  producer supplies through `CommitInfo` what only it knows — items skipped as unchanged,
  blobs written and reused, tombstones, truncation, warnings.
- **`Writer` is mutex-guarded** so the step 9 worker pool can write items as it finishes
  them, and refuses writes after commit.
- **`ErrBlobTooLarge` is a sentinel, not a failure.** Content over the cap writes nothing
  and returns a distinguishable error; the producer's response is to record the fragment as
  truncated with no blob.

Known limitations, deliberate:

- No garbage collection, no `Delete`, no listing of blobs. That is step 13, which owns the
  three-phase collection the envelope specifies.
- Nothing links `internal/artifact` yet, so the shipped binaries are still 7.8 MB. The
  first command that reads or writes a run pays the backend registration cost measured in
  AGENTS.md's hazards (about 39 MB stripped with all three drivers).
- No CLI command was added. Step 3's actions do not call for one, and `inget-fetch` is the
  first legitimate caller.

## Step 4 record

Implemented: `internal/state`, the persistence surface for every guard level, plus
`migrations/` as an embedded goose migration set. One `Store` interface, one
implementation, two dialects: postgres (default, schema `inget_state`, pgxpool behind
database/sql) and sqlite (modernc.org/sqlite, for offline development). The schema is the
design's state schema verbatim, with `locks` added for sqlite only.

Files: `state.go` (interface and its contract), `types.go` (value types and status
vocabularies), `dialect.go` (the three differences plus the placeholder rewriter),
`open.go` (options, connection setup), `drivers.go` (sqlite registration), `store.go`
(exec/get/each/inTx helpers, null and JSON handling), `migrate.go`, `items.go`,
`fragments.go`, `derivations.go`, `views.go`, `refs.go`, `signatures.go`, `runs.go`,
`work.go`, `lock.go`, `config.go` (the one bridge to `internal/config`).
`migrations/embed.go` plus `migrations/state/{postgres,sqlite}/00001_init.sql`.

Pinned: `github.com/jackc/pgx/v5` v5.10.0, `modernc.org/sqlite` v1.55.0,
`github.com/pressly/goose/v3` v3.27.3. All pure Go: `CGO_ENABLED=0 GOOS=linux
GOARCH=amd64` cross-compiles both binaries at 5.3 MB.

Verified: `make build test lint` green, `golangci-lint run` 0 issues, `go mod tidy` a
no-op. The conformance suite is 57 cases and passes against **both** drivers — postgres
via `INGET_TEST_PG` against `postgres:17-alpine` in Docker, which is how the postgres-only
paths (advisory lock, `SKIP LOCKED`, `jsonb`, quoted `"binary"`, `search_path`) were
actually exercised rather than assumed. The acceptance items specifically: a second `Lock`
on a held datatype returns `ErrLocked` from a separate connection; claimed-but-uncompleted
work is reclaimed by `ResetClaims` and re-claimed; `ReferencedBy` returns reverse edges and
loses the ones dropped from an edge set; a signature change is visible through
`Signature`/`PutSignature`; four concurrent workers claim every item exactly once.

Deviations from the design and the plan, all deliberate:

- **One implementation over `database/sql`, not two.** The plan reads as two drivers with
  their own code. Twenty-odd statements written twice is twenty-odd chances for the drivers
  to disagree, and a conformance suite cannot catch a divergence it is testing against
  itself. Postgres still uses `pgxpool` exactly as specified — `stdlib.OpenDBFromPool`
  adapts it — so `pgx.CopyFrom` is still reachable for step 7, and `rowserrcheck` and
  `sqlclosecheck` (already enabled in `.golangci.yml`) now have something to check.
- **`StateStore` is named `Store`.** `state.StateStore` stutters; step 3 renamed
  `BlobStore` to `artifact.Store` for the same reason.
- **`ClaimWork` takes a datatype.** The design's signature returns bare item IDs while
  `CompleteWork` requires a datatype, so a run spanning datatypes could not complete what
  it claimed. Adding the parameter keeps it symmetric with `EnqueueWork` and
  `CompleteWork`, and a run holds a per-datatype lock anyway.
- **Four methods the design does not list**: `Migrate` and `Close` (lifecycle, mirroring
  `Destination.Migrate`); `Item`, so the composed hash and metadata that `PutItem` writes
  can be read back; `ResetClaims`, without which stranded claims are unreachable;
  `ResumableRun`, without which a restarted process cannot find the run whose queue it
  should adopt and the work table's resumability is unusable.
- **`PutItem` takes one struct.** `Item` gains `Source`, `RunID` and `ComposedHash` because
  the `items` row has those columns and `source_name` is NOT NULL. Three trailing string
  parameters would be three chances to swap two of them.
- **`RefEdgeSource` and `ItemKey` are one type.** Identical shape, and one is the reverse
  of the other.
- **`internal/state` does not import `internal/artifact`.** Sharing a `Scope` type would
  link the gocloud backends — 27 MB — into anything touching state. Scope is a string with
  constants here.

Choices made where the plan was silent:

- **`search_path` rather than qualified table names.** The pool sets
  `search_path=inget_state, public` as a startup parameter, so runtime SQL is unqualified
  and identical for both drivers. Migration DDL *is* qualified, because it is read by
  humans and may be applied by the goose CLI. `Migrate` creates the schema itself before
  goose runs: goose records its version table through the search_path, and if the schema
  did not exist yet that table would land in `public` and the next run would believe
  nothing had been applied.
- **No timestamp crosses the interface.** Every timestamp is written by
  `DEFAULT CURRENT_TIMESTAMP` or by the statement itself, and no method returns one.
  SQLite has no timestamp type, so a returned timestamp needs per-dialect encoding;
  nothing needs one until `inget state show` (step 13), which can add typed accessors.
- **`PutFragments` owns the GC counter.** It takes the item's *complete* fragment set:
  first every fragment of the item is counted missing, then the present ones are upserted
  back to zero. Two fixed statements instead of a `NOT IN` list of up to 2000 keys, and the
  counter counts consecutive absences, which is what `retention.missing_runs` means.
- **`Derivation` reads and touches in one statement** (`UPDATE ... RETURNING output`).
  A cache hit on the hottest path in the pipeline costs one round trip, not two. The
  consequence is that reads write, so the state store cannot be a read replica.
- **`PutRefs` replaces an item's whole edge set.** A merge would leave an edge behind when
  a reference is removed, and a stale reverse edge invalidates views forever.
- **`ResumableRun` requires the config hash to match.** A run made under a different
  configuration computed a work set that may no longer be the right one, so it is not
  adopted. `StartRun` is an upsert so the adopted run can be reopened.
- **Terminal run statuses are a closed set** and `FinishRun` on an unknown run is an error.
  A typo'd status would silently make a run unresumable.
- **`ClaimWork` sorts in Go.** Found by the postgres run: `UPDATE ... RETURNING` yields
  rows in the order the update touched them, not the subquery's `ORDER BY`. sqlite happened
  to agree with the subquery, so the ordering assertion passed there and failed on
  postgres. This is the cross-driver divergence the suite exists to find, and it is the
  argument for running it against both before calling a step done.
- **The sqlite lock is `INSERT ... ON CONFLICT DO NOTHING`,** with contention read from the
  affected-row count rather than from a driver-specific constraint error code.
- **Lock release detaches from the caller's context** (`context.WithoutCancel` plus a 10s
  bound), because release usually happens while shutting down after SIGTERM.

Known limitations, deliberate:

- No `inget migrate`, `inget state show|gc|unlock` command. Step 7 wires migrate, step 13
  owns the rest. `Store.Migrate` exists and is tested; nothing calls it outside tests.
- A killed process leaves the sqlite lock row behind. `inget state unlock` (step 13) is the
  answer; the postgres advisory lock has no such problem, which is one more reason postgres
  is the default.
- `PutFragments` and `EnqueueWork` write one prepared statement execution per row. For a
  2000-fragment item on a remote database that is 2000 round trips inside one transaction.
  If it shows up in step 9's timings, batch into multi-row `VALUES` — the statements are in
  one place.
- Nothing links `internal/state` yet, so the shipped binaries are still 5.1 MB. The first
  command that touches state pays about 12 MB.

## Library decisions (approved, folded into D15)

Approved 2026-08-01 and written into `feature-design.md` D15 and the corresponding plan
steps, so no session needs to re-derive them.

| Step | Concern | Selection | Version at approval |
|---|---|---|---|
| 3 | Run identifiers | `oklog/ulid/v2` | v2.1.2, Apache-2.0 |
| 5 | Glob matching with `**` | `bmatcuk/doublestar/v4` | v4.10.0, MIT |
| 5 | Edit distance for D4 drift | `agnivade/levenshtein` | v1.2.1, MIT |
| 9 | Secret detection in fetched content | `zricethezav/gitleaks/v8` | v8.30.1, MIT |

Pin these when the step that needs them lands, not before, so `go.mod` stays honest about
what is actually used. `doublestar` is already pinned: step 2 validates dependency globs
with the same matcher step 5 will match with. `oklog/ulid/v2` landed with step 3, alongside
`gocloud.dev` v0.46.0 and `klauspost/compress` v1.19.1, which D15 had already selected.
Step 4 landed the three D15 had selected for the state store: `jackc/pgx/v5` v5.10.0,
`modernc.org/sqlite` v1.55.0 and `pressly/goose/v3` v3.27.3. goose is used through its
`NewProvider` API rather than its package-level globals, so two stores can migrate
different dialects in one process.

Deliberately kept in-tree:

- **Log attribute redaction** (`internal/logging/redact.go`). Generic redactors match
  `token`, `key` and `secret` as substrings and would redact `input_tokens`, `cache_key`,
  `related_keys` and `signature` — the exact fields the cascade and `inget plan` exist to
  expose. The custom allowlist is the entire job. The redaction tests assert these
  negatives; do not widen the patterns.
- **Config and signature hashing** (step 2). Length-prefixed serialization of sorted
  key/value pairs, not canonical JSON. The only Go RFC 8785 implementation is
  unmaintained, and length prefixing removes float and unicode formatting ambiguity.

Caveat carried into step 9: gitleaks reaches its regex engine through a WASM runtime
rather than CGO by default. Confirm `CGO_ENABLED=0` builds still work; if not, drop the
dependency for an in-tree ruleset.

## Step 5 record

Implemented: `internal/delta`, the invalidation cascade engine. Six source files plus a
package doc file, covering all the actions in the plan: fragment reconciliation, enricher
signature building, cache key derivation for levels 1–3, glob-based view scoping,
deterministic composition, and drift measurement.

Files: `delta.go` (package doc), `reconcile.go` (set comparison producing Delta),
`signature.go` (length-prefixed sorted SHA-256 over all enricher parameters),
`cachekey.go` (domain-separated hashing for L1/L2/L3), `scope.go` (doublestar glob
matching for view dependency declarations), `compose.go` (tier/path sort, separator join,
rune-aware max_chars truncation, composed hash), `drift.go` (normalised Levenshtein via
agnivade/levenshtein).

Pinned: `github.com/agnivade/levenshtein` v1.2.1, as selected by D15. doublestar was
already present from step 2.

Verified: `make build test lint` green; `go test -race -count=1 ./internal/delta/...`
passes (1.0s); `go vet` clean; `go mod tidy` a no-op. 30 test functions across 6 test
files cover all acceptance items: every delta permutation; identical inputs in different
order produce identical composed hashes; changing a prompt byte changes the signature;
`**` and single-segment globs match as specified; drift below and above threshold behaves
correctly; a view whose globs match nothing is skippable; cache key levels are distinct
even with identical input strings.

Deviations: none. The plan's actions mapped one-to-one onto the implementation.

Choices made where the plan was silent:

- **Separator between entries is `\n---\n`.** The plan says "deterministic composer" but
  does not prescribe the separator. Markdown horizontal rules are visually scannable in
  debug output and unlikely to appear mid-fragment since connectors strip them.
- **Cache key domain separation uses a `Ln` prefix and NUL bytes.** This makes levels
  unforgeable from each other even with identical string inputs, since SHA-256 of
  "L1\x00a\x00b" ≠ "L2\x00a\x00b". Config hashing uses length-prefixed encoding for a
  similar unambiguity guarantee; here the simpler NUL approach suffices because inputs
  are already either hex digests or prompt text (neither contains NUL).
- **`Reconcile` returns sorted slices.** The plan does not require ordering, but
  deterministic output simplifies test assertions and makes log messages stable.
- **`Compose` returns the hash alongside the text.** The plan says "compute the scoped
  composed hash"; returning it from the same function avoids double-hashing.
- **`DriftExceedsThreshold` uses strict `>` not `>=`.** A drift of exactly the threshold
  does not exceed it: the operator chose that threshold as the bound of acceptable change.

## Still open

Nothing. Every library question raised through step 14 is decided and recorded in D15.
Raise new ones here rather than deciding them inside a step.

## Step 6 record

Implemented: `internal/model`, the Generator and Embedder clients with one OpenAI-
compatible HTTP driver serving both roles (D10), plus deterministic fakes for CI.

Files: `model.go` (interfaces: `Generator`, `Embedder`, `Usage`), `openai.go` (the shared
OpenAI driver with `NewGenerator` and `NewEmbedder`, MRL truncation and re-normalization),
`retry.go` (exponential backoff with full jitter on 429 and 5xx, Retry-After parsing),
`wire.go` (JSON request/response types for `/v1/chat/completions` and `/v1/embeddings`),
`fake.go` (`FakeGenerator` returning hash-derived text, `FakeEmbedder` returning
hash-derived unit vectors).

Pinned: nothing new. The package uses only the standard library (`net/http`, `encoding/json`,
`crypto/sha256`, `math`, `context`, `time`). No external retry or HTTP client libraries
were needed; the plan mentioned `hashicorp/go-retryablehttp` and `cenkalti/backoff`, but
the retry surface here is 5 attempts with jitter and Retry-After — fewer than 100 lines of
clear code that would not benefit from two transitive dependency trees.

Verified: `make build test lint` green; `go vet` clean; `gofmt` clean; `go mod tidy` a
no-op. 19 test functions across 3 test files cover all acceptance items: happy-path
generation and embedding; 429 with `Retry-After` retries and succeeds; 5xx retries then
succeeds; malformed response fails cleanly; empty choices is an error; non-retryable 4xx
fails immediately on first attempt; truncation plus re-normalization produces unit-length
vectors; batching splits into the correct number of API calls; count mismatch is an error;
empty input returns nil without calling the server; fakes are deterministic across runs;
different inputs produce different fake outputs; signatures are stable and change when
parameters change.

Deviations from the plan:

- **No `hashicorp/go-retryablehttp` or `cenkalti/backoff`.** The retry logic is 5 attempts
  with exponential backoff, full jitter, and Retry-After — all standard patterns in under
  100 lines. Adding two external dependencies (plus their transitive graphs) for this
  would violate "prefer stdlib" from the conventions with no added capability.
- **Concurrency bounding is not in this package.** The plan says `errgroup.SetLimit`; that
  belongs at the pipeline orchestration layer (step 8) where the worker pool lives. The
  model client is a single-call-at-a-time interface. `Concurrency` is in the config and
  will be consumed by `internal/pipeline`.
- **Signature format is `openai:key=value,key=value` rather than SHA-256.** The delta
  package computes a SHA-256 over the enricher's full SignatureInput (which includes the
  model client's signature as one field). Making the model signature itself a hash would
  produce a hash-of-hash with no additional collision resistance and lose debuggability.
  The format is deterministic and sorted, satisfying D2.

Choices made where the plan was silent:

- **`chatUsageBlock.CacheHitTokens`** is mapped from the OpenAI extension field
  `prompt_tokens_details.cached_tokens`. DeepSeek and Kimi both report cache hits here;
  it feeds `inget plan` cost estimates.
- **Re-normalization is always applied.** The spec says "truncation plus re-normalization";
  normalization is applied unconditionally since it is idempotent on already-unit vectors
  and ensures the contract even if a provider returns un-normalized embeddings.
- **`Embed` returns `nil` not `[][]float32{}` for empty input.** This avoids an HTTP call
  and follows the Go convention that a nil slice is the zero value for an absent result.
- **Batching is purely sequential.** Parallel batch requests are a pipeline-level concern.
  The embedder sends one batch at a time, respecting the retry logic per call.

Known limitations, deliberate:

- No streaming. Generation responses are read in full. The pipeline processes one item at
  a time; streaming would add complexity for no latency benefit when the consumer cannot
  start work until the full response is available.
- No connection pooling beyond `http.Client`'s default transport. The default transport
  keeps connections alive and handles HTTP/2 multiplexing; a custom transport is not needed
  until profiling shows connection establishment as a bottleneck.
- Nothing links `internal/model` yet. The first command that uses it is `inget run`
  (step 8).
