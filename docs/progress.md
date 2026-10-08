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
| 7 | pgvector destination | **done** |
| 8 | Enrichment pipeline | **done** |
| 9 | GitHub connector | **done** |
| 10 | Monday connector | not started (skipped ahead of 11) |
| 11 | Reference resolution | **done** |
| 12 | Quality harness | **done** |
| 13 | Reindex and operations | **done** |
| 14 | Release and documentation | **done** |

Sizing note: steps 5, 8, 9 and 10 carry more surface than one session should hold. Plan on
splitting each into an implementation session and a test-hardening session, so expect
roughly 18 sessions rather than 14. Steps 1–8 need no network and no credentials; schedule
9 and 10 for when `INGET_GITHUB_TOKEN` and `INGET_MONDAY_TOKEN` are available.

## Design section map

`feature-design.md` line ranges to read per step. Everything else can stay unread.

| Step | Sections | Lines |
|---|---|---|
| 1 | Architecture, Repository Layout, Observability, D15 | 51–118, 1370–1405, 1349–1369, 596–640 |
| 2 | Configuration, D2 (hashing) | 808–1003, 157–180 |
| 3 | `artifact-envelope.md` in full, D5, D6 | all, 261–300 |
| 4 | State schema, D13 | 643–744, 532–554 |
| 5 | D1, D2, D3, D4, Enrichment Pipeline | 121–260, 1276–1303 |
| 6 | D10, API/Interface | 371–458, 1004–1166 |
| 7 | D7, D8, D9, Destination schema | 301–370, 745–807 |
| 8 | D11, Enrichment Pipeline, Edge Cases | 459–491, 1276–1327 |
| 9 | `github/repo`, D6, Security considerations | 1207–1228, 283–300, 1328–1348 |
| 10 | `monday/item` | 1229–1275 |
| 11 | D12 | 492–531 |
| 12 | D14, Testing Strategy | 555–595, 1406–1429 |
| 13 | Edge Cases, `artifact-envelope.md` GC | 1304–1327, 263–275 |
| 14 | Non-Goals, Repository Layout | 1430–1441, 1370–1405 |

These offsets are valid for `feature-design.md` at 1488 lines. Editing that file shifts
everything below the edit, so regenerate the map with
`grep -n '^#\{2,3\} ' docs/feature-design.md` whenever the design changes.

## Step 1 record

Implemented: module `github.com/maxwell-cudlitz/inget`; `internal/logging` (slog JSON/text,
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

**Resolved in step 9.** `CGO_ENABLED=0` builds fine. `zricethezav/gitleaks/v8` v8.30.1 is pure
Go — its regex engine runs on `wasilibs/go-re2` over `tetratelabs/wazero` — and
`detect.NewDetectorDefaultConfig()` returns the whole default ruleset in one call. It adds
about 20 indirect modules and roughly 4 MB of stripped binary. Its `Detect` path accumulates
no findings and is mutex-guarded, so one detector is safe to share across the worker pool, and
it is constructed with `Redact = 100` so a finding never carries a plaintext secret.

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
passes (1.0s); `go vet` clean; `go mod tidy` a no-op. 24 test functions across 6 test
files cover all acceptance items: every delta permutation; identical inputs in different
order produce identical composed hashes; changing a prompt byte changes the signature;
`**` and single-segment globs match as specified; drift below and above threshold behaves
correctly; a view whose globs match nothing is skippable; cache key levels are distinct
even with identical input strings.

Deviations: two, both from the Go interfaces sketched in the design rather than from the
plan's actions.

- **`Delta` carries keys, not fragments.** The design declares
  `Added, Modified, Unchanged []Fragment`; the implementation uses `[]string` for all four
  categories. Step 8 therefore needs the incoming fragment map on hand to recover
  fingerprints for level-1 cache keys, which it holds anyway.
- **`Compose` takes entries and returns a `Composition`.** The design declares
  `Compose(it Item, derived map[string]string, scope []string) (string, error)`. A map
  cannot express tier-then-path ordering, so the composer takes `[]ComposeEntry` (key,
  tier, content) and returns text, hash and truncation facts together.

Choices made where the plan was silent:

- **Separator between entries is `\n---\n`, and each entry is headed `## <key>`.** The
  plan says "deterministic composer" but does not prescribe a format. The key header gives
  the model path attribution — `surface` and `operations` are questions about *where*
  things live — and makes a rename change the composed hash, which is the correct
  invalidation for content that moved.
- **Cache key domain separation uses a `Ln` prefix and NUL bytes.** This makes levels
  unforgeable from each other even with identical string inputs, since SHA-256 of
  "L1\x00a\x00b" ≠ "L2\x00a\x00b". Config hashing uses length-prefixed encoding for a
  similar unambiguity guarantee; here the simpler NUL approach suffices because inputs
  are already either hex digests or prompt text (neither contains NUL).
- **`Reconcile` returns sorted slices.** The plan does not require ordering, but
  deterministic output simplifies test assertions and makes log messages stable.
- **`Compose` returns the hash alongside the text**, plus whether `max_chars` clipped the
  document and the pre-truncation rune count. The plan says "compute the scoped composed
  hash"; returning it from the same call avoids double-hashing, and the design requires
  truncation to be recorded and logged rather than silent.
- **`DriftExceedsThreshold` uses strict `>` not `>=`.** A drift of exactly the threshold
  does not exceed it: the operator chose that threshold as the bound of acceptable change.

## Still open

Nothing. Every library question raised through step 14 is decided and recorded in D15.
Raise new ones here rather than deciding them inside a step.

The module path mismatch raised during step 14 was resolved in that step: the module is
`github.com/maxwell-cudlitz/inget`, matching the remote, so `go install` resolves.

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

Verified: `go build`, `go vet`, `gofmt` and `go test -race` clean; `go mod tidy` a no-op.
**`make lint` was not actually green**: golangci-lint was not installed locally, and the
target skips it with a warning in that case, so only `go vet` ran. The linters CI enforces
found 12 issues in this package — see the review-fix record below. Install it with
`make lint-install` before calling a step done. 29 test functions across 6 test files cover all acceptance items: happy-path
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

- **Cache-hit tokens** come from the OpenAI extension field
  `prompt_tokens_details.cached_tokens`, decoded through a nested `promptTokensDetails`
  struct. DeepSeek and Kimi both report cache hits there; it feeds `inget plan` cost
  estimates.
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

## Step 5–6 review fixes

A review of both commits found one broken gate, three bugs and a set of gaps. All are
fixed; `go build`, `go vet`, `gofmt`, `golangci-lint run ./...` and `go test -race ./...`
are green, with lint reporting zero issues across the repository.

Broken gate:

- **12 lint findings in `internal/model`**, invisible locally because `make lint` skips
  golangci-lint when it is not installed. Eleven were unchecked errors (`resp.Body.Close`,
  and `Write`/`Encode`/`Decode` in tests); one was `FakeGenerator.dims`, a field nothing
  read. Bodies now close through `defer func() { _ = c.Close() }()`, tests write through
  `writeJSON`/`writeString` helpers that fail on a write error, and the dead field is gone.

Bugs:

- **Cache-hit tokens were never parsed.** `json:"prompt_tokens_details.cached_tokens"` on a
  flat field matches a literal key of that name, which no provider sends, so
  `Usage.CacheHitTokens` was always zero and every `inget plan` estimate would have
  overstated cost. Now a nested `promptTokensDetails` struct, covered by a test that decodes
  raw provider JSON.
- **Drift mixed runes and bytes.** `levenshtein.ComputeDistance` returns a rune distance;
  the normalizer divided by `len()` in bytes, understating drift on every non-ASCII string
  — a one-character edit in CJK text scored a third of its true value — which silently
  suppresses the re-embed `drift_threshold` exists to trigger. Now
  `utf8.RuneCountInString`, with tests for accented, CJK, emoji and mixed-script text.
- **Embedding order was assumed.** `embeddingData.Index` was decoded and ignored, so a
  provider returning `data` out of order would store every vector against the wrong view,
  with no error and nothing wrong-looking until search answers drifted. Vectors are now
  placed by reported index, with out-of-range and duplicate indices rejected.

Gaps closed:

- **Returned vector width is validated** against `Dims()` and the error names the setting to
  change, instead of surfacing later as a pgvector column mismatch (D8).
- **`ViewSkippable(changedKeys, dependsOn)`** dropped its fragment-set parameter. It
  filtered the changed set against the current fragments, so a deleted key — absent from
  that set by definition — reported its views as skippable and left them stale forever.
- **`Compose` reports truncation.** It returns a `Composition` (text, hash, `Truncated`,
  `OriginalChars`); the design requires clipping to be recorded and logged, and the caller
  previously could not tell.
- **Composed entries carry a `## <key>` header**, giving views path attribution and making a
  rename change the composed hash.
- **Signature completeness is enforced structurally.** `TestBuildSignatureCoversEveryField`
  walks `SignatureInput` by reflection and fails if mutating any field leaves the digest
  unchanged, so a field added to the struct and forgotten in `BuildSignature` fails the
  build rather than silently serving stale views (D2).
- **`max_input_chars` is enforced.** It was a dead config field; an oversized prompt now
  fails locally with the setting named, rather than costing a round trip to be rejected by
  the provider.
- **Seed 0 reaches the provider.** `json:"seed,omitempty"` dropped it while the signature
  still recorded `seed=0`, quietly unpinning determinism for anyone who configured that
  value.
- **Retries are observable and interruptible.** Each retry logs at warn with attempt,
  delay and cause; a cancelled context aborts the wait instead of issuing one more doomed
  request; the last attempt no longer sleeps before giving up; and a `Retry-After` beyond
  two minutes fails immediately rather than parking a worker on an exhausted quota.
- **The `dimensions` request parameter is gone.** MRL truncation is client-side only, so
  TEI — the step 7 deploy target, which does not implement the parameter — behaves like
  providers that do.

API changes later steps must account for:

| Before | After |
|---|---|
| `Compose(...) (string, string)` | `Compose(...) Composition` |
| `ViewSkippable(fragmentKeys, changedKeys, dependsOn)` | `ViewSkippable(changedKeys, dependsOn)` |
| `OpenAIGeneratorConfig.Concurrency`, `OpenAIEmbedderConfig.Concurrency` | removed; the pipeline owns the worker pool and reads `models.*.concurrency` itself |
| `internal/model/openai.go` held both roles | split into `generator.go`, `embedder.go`, with shared helpers left in `openai.go` |

Backoff bounds in `retry.go` are now variables so tests can shrink the clock; the package
suite dropped from 9.2s to 2.1s. They are written only by tests.

## Step 7 record

Implemented: `internal/destination`, the vector sink surface plus the pgvector driver, and
`inget migrate`. The destination schema is the design's "Destination schema (pgvector)"
section, with the parameterised parts rendered from configuration.

Files: `destination.go` (interface, `Row`, `SearchQuery`, `SearchResult`), `row.go` (`RowID`,
vector text encoding, the shared write-path checks), `open.go` (`Options`, identifier
whitelist, driver registry, `FromConfig`), `pgvector.go` (pool, migration rendering, extension
version check, `AssertModel`), `pgvector_write.go` (`Upsert`, `BulkLoad`, `DeleteItem`),
`pgvector_query.go` (`Search`), `version.go` (extension version comparison).
`migrations/destination/pgvector/00001_init.sql`, `migrations/embed.go` extended with
`Destination`, `cmd/inget/migrate.go`, `deploy/docker-compose.yaml`.

Pinned: nothing new. pgx v5.10.0 and goose v3.27.3 came with step 4; the package uses pgx
directly rather than through `database/sql` because `CopyFrom` is pgx's. `inget` grew from
5.1 MB to 17 MB (the state store plus the destination); `inget-fetch` is unchanged at 5.1 MB.

Verified: `make build test lint` green with golangci-lint actually installed —
`golangci-lint run` reports 0 issues, `go mod tidy` a no-op, `gofmt` clean, and
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64` cross-compiles both binaries. The integration suite
ran against `pgvector/pgvector:pg17` in Docker, which is how the acceptance items were
actually exercised: migrate is idempotent; the embedding column is `halfvec(4)` as rendered;
`AssertModel` binds, agrees on repeat, and rejects another model, another signature and
another width with text naming `inget reindex`; a write before `AssertModel` fails; 500
vectors plus a planted neighbour returns the planted one at score ≥ 0.99; a search restricted
to one of four views over 200 vectors returns only that view and finds its planted row;
re-upsert leaves the row count unchanged; `DeleteItem` removes all three views of an item and
tolerates an absent one; `BulkLoad` writes 1000 rows, merges a second identical load, and
leaves no staging table; a batch size that does not divide the row count writes every row.
`./bin/inget migrate` was run against the same database with the shipped `config.yaml` and
the resulting `inget_vectors` matches the design's DDL column for column and index for index.

Deviations from the design and the plan, all deliberate:

- **The destination schema is a rendered template, not static SQL.** The design pins
  `halfvec(1024)`, but `storage`, the embedder width and the table name are all
  configuration, and PostgreSQL cannot parameterise a type modifier. The `.sql` file is a Go
  template; `renderMigrations` substitutes the shape and hands the result to goose as an
  in-memory `fstest.MapFS`. The DDL therefore still lives in one readable, versioned place,
  at the cost of the file not being runnable by `psql` as it stands.
- **Migrations are keyed by driver, not by dialect.** `migrations/destination/pgvector/`
  rather than `.../postgres/`, because the next destination may not be a SQL database at all
  and would have no dialect to name.
- **Each destination gets its own goose version table.** `<table>_goose_version`. With one
  shared `goose_db_version`, a second destination in the same database would read version 1
  as applied and create nothing. The extension and the model registry are `IF NOT EXISTS`
  for the same reason, and the registry is deliberately shared — its primary key is the table
  name, which is exactly what D7 needs. A test migrates two destinations of different widths
  and storage types into one database and asserts both.
- **No `pgvector-go` dependency; vectors cross the wire as text.** The binary format is
  about four times smaller, but halfvec is IEEE 754 half precision, and hand-rolling
  float32 → float16 (subnormals, overflow, round-to-nearest-even) produces vectors that are
  quietly slightly wrong — a failure mode with no symptom except worse search. The server's
  own parser is correct, so an embedding is sent as `[0.1,…]` and cast server-side.
- **`CopyFrom` never touches a halfvec column.** The plan asked to confirm `CopyFrom` works
  with `halfvec`; it is sidestepped instead. The staging table is `CREATE TEMP TABLE (LIKE
  <table> INCLUDING DEFAULTS)` with `embedding` retyped to `text`, so COPY carries text and
  the single merge statement does the cast. `INCLUDING DEFAULTS` is load-bearing: `LIKE`
  alone copies `NOT NULL` but not `DEFAULT now()`, and `updated_at` then has no value — this
  was found by the integration run, not by inspection.
- **D7 is enforced on every write, not only in `AssertModel`.** `Upsert` and `BulkLoad`
  check each row's model, signature and width against the binding, and refuse outright if
  `AssertModel` was never called. The registry alone cannot catch a batch assembled from two
  embedders inside one process, and nothing downstream would report it.
- **`inget migrate` lives in `cmd/inget/`, not `internal/cli`.** `internal/cli` is shared
  with `inget-fetch`, so a subcommand there would link the state store and both gocloud-free
  destination drivers into a binary that needs neither. `cli.Execute` already takes
  subcommands from the caller, which is the seam the design intended.
- **`Migrate` refuses pgvector older than 0.8.0.** Below it an HNSW scan filters after the
  index scan, so a search restricted to one view loses recall silently rather than failing
  (D9). Checking at migration time is the one moment the extension is guaranteed present and
  the operator is watching.

Choices made where the plan was silent:

- **Row IDs are NUL-separated before hashing.** `sha256(datatype‖item_id‖view‖frag_key)`
  taken literally is not injective — datatype `a/b` with view `c` collides with datatype `a`
  and view `b/c`. A test asserts both boundaries.
- **A duplicate id inside one call is an error, not a dedupe.** An id is derived from
  identity, not content, so two rows sharing one is a caller that computed the same view
  twice. Both PostgreSQL upsert forms refuse to touch a row twice in one statement anyway,
  so the alternative is a confusing error from the server.
- **The table name is whitelisted to lowercase identifiers.** It reaches DDL and DML as text
  because neither a table name nor a type modifier can be a bind parameter. It comes from a
  config file rather than a request, so this is not the primary defence, but it is three
  lines and it stops `Options` from being a hazard for a future caller.
- **`Upsert` batches with `pgx.Batch`; a batch is not a transaction.** A failure partway
  leaves earlier batches written, which is what the pipeline's per-item checkpointing
  expects — an item is checkpointed after its rows land.
- **`Search` returns cosine similarity, not distance.** `1 - (embedding <=> …)`, so larger
  is closer and no caller has to remember which direction it is sorting.
- **`SET LOCAL` scopes both HNSW settings to the transaction**, so a pooled connection is
  never handed back still carrying them.
- **`DeleteItem` on an absent item is not an error.** A tombstone for an item that never
  reached this destination is a normal outcome of a partial run.
- **wrapcheck now exempts this module's own internal packages** (`.golangci.yml`). They wrap
  at their own boundaries with messages tests assert, so wrapping again in `cmd/inget`
  repeated the same phrase — "migrating the state store: migrating postgres state store: …".
  Errors from outside the module are still checked everywhere.

Known limitations, deliberate:

- No `--only` flag on `migrate`: it migrates the state store and every configured
  destination. A configuration with one unreachable destination therefore fails the whole
  command after the reachable ones are already correct, which is safe because every step is
  idempotent.
- The text wire format costs roughly four times the bytes of binary during a cold load. If a
  real reindex shows that as the bottleneck, the answer is a narrower float format for
  halfvec columns (it keeps ~3–4 decimal digits), not hand-rolled float16.
- No `Search` filter beyond datatype and view. `related_keys` and `metadata` have GIN indexes
  and no accessor; `inget query` (step 13) is the first caller that needs one.
- No k8s CronJob examples in `deploy/` yet — step 13 owns those.
- `internal/destination` is not linked by `inget-fetch` and never should be.

## Step 8 record

Implemented: `internal/enrich`, the enricher interface with LLM and passthrough
implementations; `internal/pipeline`, the orchestration layer with errgroup worker pool,
per-item transactional checkpointing, SIGTERM graceful shutdown, and run lifecycle
management; `inget run` and `inget plan` CLI commands; all 11 prompt templates.

**`internal/enrich/` (6 files):**
- `enricher.go` — `Enricher` interface, `TemplateData`, `FragmentTemplateData` types.
- `llm.go` — `LLMEnricher`: renders prompt template, calls Generator, derives signature
  from delta.BuildSignature.
- `fragment.go` — `FragmentLLMEnricher`: per-fragment enrichment with its own prompt and
  separate signature.
- `passthrough.go` — Returns input unchanged; fixed signature string.
- `prompt.go` — `PromptRenderer`: os.ReadFile → text/template.Parse, Render, and raw Bytes
  for signature hashing.
- `enrich_test.go` — 8 tests covering all enrichers, prompt loading, signatures.

**`internal/pipeline/` (8 files + 2 tests):**
- `pipeline.go` — `Deps`, `DatatypeConfig`, `RunConfig`, `Stats`, `Plan` types; shutdown
  context helpers.
- `reconcile.go` — Opens latest artifact run, reads records, reconciles with
  `delta.Reconcile` against persisted item fingerprints.
- `process.go` — `processItem`: the per-item stage sequence (load → reconcile → enrich →
  compose → generate views → embed → upsert → checkpoint).
- `enrich.go` — Fragment enrichment with derivation cache lookup; builds ComposeEntries and
  FragmentStates from artifact records.
- `views.go` — View processing: scope check via `delta.ViewSkippable`, level-2 input hash
  via `delta.ViewInputHash`, generation, drift check via `delta.DriftExceedsThreshold`,
  embedding, row assembly.
- `run.go` — Run lifecycle: Lock, resume via ResumableRun, errgroup worker pool with
  `SetLimit`, tombstone processing, stats, FinishRun.
- `signal.go` — `NotifyShutdown`: SIGTERM/SIGINT → graceful (close channel), second signal
  → cancel context.
- `stats.go` — Atomic counter collector for concurrent workers.
- `pipeline_test.go` — Tests for shutdown context and concurrent stats.
- `cascade_test.go` — **The gate test**: 100-fragment item with 8 views, full cascade
  integration.

**CLI (3 files in `cmd/inget/`):**
- `run.go` — `inget run [datatype]`: builds deps, handles signals, runs pipeline.
- `plan.go` — `inget plan [datatype]`: dry-run, outputs JSON plan to stdout.
- `helpers.go` — Shared dependency builders for Generator, Embedder, enrichers, config
  helpers.

**Prompts (11 `.tmpl` files):**
- `prompts/github/repo/{fragment,role,surface,internals,stack,integrations,stewardship,operations,aliases}.tmpl`
- `prompts/monday/item/{substance,progress}.tmpl`

Pinned: `golang.org/x/sync` v0.22.0 (was indirect, now direct for errgroup.SetLimit).

Verified: `go build ./...`, `go vet ./...`, and `go test -race -count=1 ./...` all pass.
The cascade gate test seeds a 100-fragment item with 8 views using sqlite state and
file:// artifacts, confirms all 100 fragment enrichments and 8 view generations on the
first run, zero work on a second identical run, and on a third run with one fragment
changed: exactly 1 fragment enrichment, 3 views regenerated (those matching `**`), 5
skipped (those matching specific paths the changed fragment doesn't match), and only 3
embeddings and 3 destination rows upserted.

Deviations from the plan:

- **`inget state show|gc|unlock` deferred to step 13.** The plan listed them in step 8's
  actions, but the acceptance criteria don't test them and the implementation plan's own
  step 13 says "inget state gc, unlock" are its deliverables. Added run and plan only.
- **Reference resolution is a no-op.** Step 11 implements D12; the pipeline does not call
  PutRefs or ReferencedBy. The stage 3 position is established in processItem's comment
  structure and in the TemplateData type (which has no References field yet — step 11 adds
  it). *Superseded by the step 11 record: references are implemented, and TemplateData
  deliberately gained no References field.*
- **The pipeline claims one item at a time from the work queue**, not a batch. The plan's
  errgroup pattern spawns one goroutine per claimed item; claiming batches larger than 1
  would require a separate claim loop, adding complexity without benefit since the
  concurrency is bounded by `g.SetLimit`. If profiling shows claim latency matters,
  batch claiming is a single-line change to the ClaimWork call.
- **The worker pool does NOT stop on a single-item failure.** processItem errors are logged
  and the item is marked `WorkFailed`; the pool continues. A run with partial failures
  finishes with status `ok` and the failed count in stats. This matches the design's
  "per-item transactional checkpointing" — a failed item is one item's problem, not a
  run-aborting event.
- **Prompt templates receive `TemplateData`/`FragmentTemplateData` directly**, not a nested
  struct. The Go template syntax `{{index .Metadata "full_name"}}` is simple; step 11 will
  add a `.References` field when reference resolution is implemented.

Choices made where the plan was silent:

- **`enrichFragments` always enriches all fragments** regardless of the delta. The plan says
  "derive per-fragment for Added and Modified; Unchanged read from derivations cache." The
  implementation achieves this by checking the derivation cache first (keyed by fingerprint
  + enricher signature), so Unchanged fragments always hit cache and Added/Modified always
  miss. No explicit delta-category check is needed.
- **`Plan` is a value type returned from `Run` in dry-run mode.** The plan says
  "`inget plan` output matches what `inget run` then does"; returning it from the same
  function guarantees they use the same reconciliation logic.
- **SIGTERM handling is a separate signal.go file** using `os/signal.Notify` in a goroutine.
  The first signal closes a channel that workers observe via `isShuttingDown(ctx)`. The
  second signal cancels the context directly.
- **`statsCollector` uses `sync/atomic.Int64`** rather than a mutex, since all operations
  are simple increments from concurrent goroutines and reads happen only after `g.Wait`.
- **`destination.Destination` binding (`AssertModel`) happens at startup**, not per-upsert.
  The buildDeps helper calls it when opening each destination, before the worker pool runs.

### Step 8 review and corrections

The record above described the step as delivered. A review found that several of its claims
were not true of the code, and that the step was never CI-green. Everything below is the
corrected state; where it contradicts the record above, this section wins.

Two CI gates were failing on the committed step. `gofmt -l .` listed five step-8 files, and
`go mod tidy` moved `golang.org/x/sync` from indirect to direct — the record claimed that
pin, but the file still said `// indirect`, so the tidy check would have failed too. Both are
fixed; `make build test lint` and the pinned `golangci-lint v2.12.2` are clean.

Corrected defects, worst first:

- **SIGTERM handling did nothing.** `runWorkers` created its own shutdown channel and
  overwrote the context value carrying the signal handler's, so `isShuttingDown` was
  permanently false, the pool never drained, and `RunInterrupted` was unreachable. The pool
  now reads the channel from the context. The acceptance criterion is a test:
  `TestInterruptedRunResumesWithoutDuplicatingWork`.
- **Guard state was written before the work it attested to.** `PutItem` and `PutFragments` ran
  before view generation, so an item that failed at generation, embedding or upsert still
  recorded its new fingerprint, reconciled as unchanged on the next run, and stayed stale
  indefinitely; a finished run also meant `ResumableRun` would not retry it. All guards plus
  the work row now go through one new store method, `state.CheckpointItem`, called after the
  upsert returns. That is the "one transaction" stage 10 always specified. Derivations stay
  outside it deliberately — they are a cache, so a later failure must not discard them.
- **Per-view scoped composition was missing.** One unscoped `Compose` fed every view and the
  level-2 hash was global. Each view now composes over the fragments its globs match
  (`delta.MatchesAny`, exported for this). `delta.MatchingFragments` had no caller before this.
- **Fragment derivation was serial.** It now runs in an `errgroup` bounded by a run-scoped
  permit set, so `models.generator.concurrency` bounds total in-flight generator calls across
  both fan-out stages rather than being squared by nesting. Concurrent derivations of the same
  cache key are collapsed with `singleflight`: two items sharing a file used to pay twice, at a
  measured 15 duplicate calls per 100 fragments.
- **`drift_threshold` never suppressed an embed.** The drift check sat inside the branch where
  the text was byte-identical, so drift was always zero there and any change re-embedded.
  `reuseVector` now compares the new text against the text behind the stored vector, keeps that
  text as the baseline when it reuses, and forces a re-embed on an embedder model or signature
  change. `ViewState.Text` is documented as the embedded text, not the last text.
- **Swallowed errors.** A `ClaimWork` failure ended the run silently as `ok`; the derivation
  cache read discarded its error; `cfg.Secret` errors turned a misconfigured `api_key_env` into
  an empty key and a later 401. All three propagate, and an absent `api_key_env` is
  distinguished from one that names an empty variable.
- **Missing stages.** Stage 7 (`metadata_fields`) was plumbed into `Deps` and never read;
  `annotateMetadata` now applies it, omitting `${…}` values that belong to step 11's resolver.
  Enricher output is validated for shape and length before storage, as the security section
  requires. `inget plan` now reports fragment derivations, view generations, tokens and cost
  from `price_per_mtok_*`, and surfaces changed signatures with a `WARN` and an affected count.
- **Prompt injection guards.** All eleven templates were bare `---` separators. Each now
  delimits ingested content, states that it is data, and carries a worked example, with the
  rationale and references (OWASP GenAI LLM01:2025 mitigations 1, 2 and 6; Anthropic's
  indirect-injection guidance) in a template comment. `internal/enrich/prompts_test.go` fails if
  a template loses its guard.
- **Level-1 cache key was incomplete.** `FragmentCacheKey` now covers the fragment key, and
  `FragmentTemplateData` no longer carries item metadata, so what the prompt can read and what
  the key distinguishes are the same set.
- **Dead and misleading code removed.** Unused `Deps.Generator`, unused `ItemsSkipped`, a
  no-op status branch, and the `var _ = delta.Reconcile` import hack in the gate test.

New API on `state.Store`: `CheckpointItem`, `HasDerivation` (a read that does not touch
`last_hit_at`, so a dry run cannot keep a cold entry alive), and `RunStatus` (without it the
lifecycle `FinishRun` records is unobservable). All three are covered by the conformance suite,
including a rollback case.

Also added: `--only`, `--limit` and `--dry-run` on `inget run`, `--only`/`--limit` on
`inget plan`; a limited run records itself as `partial` scope and issues no tombstones (D6);
plan mode takes no datatype lock and opens no destination, so estimating cost does not require
a reachable vector database.

Test additions: real generator and embedder call counters replace assertions on the pipeline's
own statistics in the gate test; interrupted-then-resumed run; plan-matches-run; signature
change surfaced; scoped composition; `reuseVector`, `selectWork`, `annotateMetadata`,
`changedKeys` unit tests; prompt guard tests; state checkpoint conformance cases.

Known gaps, unchanged by this pass and still owned by later steps: reference resolution (step
11), fragment-granularity destination rows, `--force LEVEL`, and `inget state show|gc|unlock`
(step 13).

## Step 9 record

Implemented: the GitHub connector and the fetch side of the system. `inget-fetch` now produces
committed artifact runs for `github/repo`, and `inget plan` reads them.

**`internal/source/` (2 files):** `source.go` — `Connector`, `EventMapper`, `Ref`, `Item`,
`Fragment`, `ListQuery`, `Result`, the tier constants and `ErrStopList`. `registry.go` —
`Options`, `Register`, `Open`, `Registered`, `FromConfig` (the only place a source token is
read).

**`internal/source/github/` (10 files):** `github.go` (connector, registration, webhook
mapping), `domain.go` (typed `domain`/`limits` blocks and the enumeration filters),
`client.go` (net/http client, `getJSON`, `paginate`, `stream`), `request.go` (retries,
response classification, `Link` parsing), `repos.go` (enumeration and the repository
resource), `pages.go` (the two listing shapes, `--since` as a stopping condition),
`tree.go` (recursive tree), `tarball.go` (bounded in-memory extraction), `filter.go` (noise
patterns, tier patterns, MIME), `split.go` (sub-file fragments), `secrets.go` (gitleaks),
`fetch.go` (the assembly).

**`internal/fetch/` (3 files):** `fetch.go` (`Deps`, `RunConfig`, `Report`), `run.go` (lock,
enumerate, worker pool, tombstones, commit, run history), `item.go` (one item into one
record, and the blob reuse decision).

**`internal/ratelimit/` (2 files):** `ratelimit.go` (`Limiter`, `Unlimited`, `RetryAfter`,
`UnixReset`), `github.go` (token bucket over `x/time/rate` plus a reactive pause from
`x-ratelimit-*` and `retry-after`).

**CLI:** `cmd/inget-fetch/{main,fetch,deps}.go`. Fetching is the root command's own action, so
the invocation is `inget-fetch [flags]` as the design specifies, not `inget-fetch fetch`. That
needed two new optional fields on `cli.App`: `Run` and `Flags`.

**Also:** `config.DecodeInto` (strict decoding of the raw driver blocks, which the config
package's own doc comment had promised since step 2 but nothing provided); `secret_scan` in
`config.yaml`'s github limits block.

Pinned: `zricethezav/gitleaks/v8` v8.30.1 (D15), `golang.org/x/time` v0.15.0 (promoted from
indirect for `rate.Limiter`).

Verified: `make build`, `gofmt -l`, `go vet`, `go test -race -count=1 ./...` and the pinned
`golangci-lint v2.12.2` are all clean, and `go mod tidy` is a no-op. A live smoke test against
the real API committed a run for `spf13/cobra` and `oklog/ulid` (54 fragments, 54 blobs), a
second identical fetch wrote **0 blobs and reused 54**, and `inget plan github/repo` read the
committed run and estimated 54 fragment derivations and 16 view generations from it.

Deviations from the plan and the design:

- **`Fetch` returns a `Result`, not `(Item, []Fragment, error)`.** The envelope needs per-item
  warnings — a truncated tree, an excluded file, an archive that hit its cap — and the design's
  signature cannot express them. Warnings from a connector reach the manifest, because a run
  that quietly dropped half a repository and one that fetched all of it must not look the same
  afterwards.
- **`EventMapper` is a separate optional interface**, not a `Connector` method. A source
  without webhooks would otherwise carry a method that always fails, and a caller could not
  distinguish that from a payload it did not understand.
- **ETag conditional requests are not implemented.** The plan lists them, but nothing in this
  build has anywhere to persist an ETag across runs, and within one run no path is requested
  twice — so the mechanism would be dead code. Making it real needs an etags table in
  `inget_state`; it belongs with step 13's operations work. The saving it forgoes is small: one
  conditional request per organisation listing per run.
- **`inget-fetch` links `internal/state`.** The design's architecture diagram puts the level-0
  skip in the fetcher, which means reading `ItemFingerprints`, so the fetch binary needs a state
  DSN. That is also what makes tombstones computable. The cost is binary size: `inget-fetch` went
  from 5.1 MB to 55 MB, almost all of it gocloud.dev's AWS and GCS SDKs plus pgx and sqlite. The
  github connector including gitleaks is about 4 MB of it. AGENTS.md's "inget is 17 MB" was
  already stale; it is 51 MB.
- **The fetch lock key is `fetch:<datatype>`, not the datatype.** A fetch only reads guard
  state, so blocking an enrichment run that is consuming an earlier artifact would serialize two
  jobs that do not conflict.
- **`--since` forces partial scope.** The design's CLI table does not say so, but the envelope
  specification names `--since` among the things that make a run partial, and it is right: a run
  that only looked at recently-changed items cannot conclude anything about the rest.

Choices made where the plan was silent:

- **`--only` bypasses the domain filters.** An operator naming a repository explicitly, or a
  webhook naming one, should get it; dropping it because config excludes forks would leave an
  empty run with no explanation. The empty-repository check still applies, because there is
  nothing to fragment.
- **Blob reuse is trusted from state, without a `HasBlob` check.** A fragment whose git blob SHA
  matches what state recorded already has its content stored under the digest state remembers,
  and GC never collects a blob live state references. One saved round trip per unchanged
  fragment, which is most of them.
- **The fragment cap is applied before any upload**, so content about to be dropped is never
  transferred. The connector sorts fragments by tier then key, which is what makes the surviving
  prefix the informative one rather than an alphabetical accident.
- **A connector fetch failure is a warning; an artifact or state failure aborts the run.** The
  first is one item's problem and the item still appears in the enumerated set, so it produces no
  tombstone. The second would fail every remaining item the same way.
- **Files over `blob_max_bytes × 16` are recorded without content** rather than split into
  arbitrarily many pieces. Past that size the file is a data dump, and its pieces would crowd out
  the rest of the repository under `max_fragments_per_item`.
- **Secret exclusion keeps the fragment.** Content is dropped, the fingerprint is kept, and
  `meta` records `excluded=secret` with the rule IDs. Dropping the fragment entirely would make
  it look deleted and churn the cascade; keeping the fingerprint means its next change is still
  detectable.
- **`--since` accepts a duration** (`24h`) as well as an RFC 3339 timestamp and a plain date.
  A nightly job wants the duration, and computing the timestamp in a shell wrapper is a step
  nobody should need.
- **`inget-fetch` reports as JSON on stdout**, one object per datatype, matching the convention
  that stdout carries program data and stderr carries diagnostics.
- **A datatype whose source driver is not linked is skipped with a warning**, unless it was named
  explicitly with `--datatype`, in which case it is an error naming the registered drivers. The
  shipped configuration declares `monday`, whose connector lands in step 10, and failing the whole
  command over it would make the implemented half unusable.

Known gaps this step leaves: `--force LEVEL` is still unimplemented (step 13 owns it). Fixed
along the way: the `github/repo` `stack` view's `depends_on` listed `go.sum`, which the noise
filter drops, so the glob could never match; the entry is removed and the reason recorded in
`config.yaml`. Removing an unmatchable glob changes the datatype's `config_hash` but no view's
scoped composition, so it regenerates nothing.

Two gaps flagged during the step and closed before it ended: `request_test.go` now covers a real
403-with-retry-after against the client (retried) versus a 403 without one (not retried, because a
permission failure is not transient) and the attempt bound; `run_test.go` covers content over
`blob_max_bytes`, which is recorded as truncated rather than failing the item. `backoffBase` became
a package variable so those tests exercise the retry path without sleeping through it — the github
package's suite runs in 0.4s rather than 7s.

Sizing note carried forward: as with steps 5 and 8, this step was larger than one session should
hold, but the test surface landed with it rather than after it. The remaining thin spot is the
orchestrator's behaviour when the blob store itself fails mid-run, which needs a failing
`artifact.Store` and is worth doing when one exists for another reason.

## Step 11 record

Step 10 was skipped by instruction, so `monday/item` — the one datatype in the shipped config
that declares a reference — has no connector yet. Everything below is therefore exercised
against `github/repo` in tests rather than against the shipped Monday configuration.

Implemented: `internal/enrich/refs` (resolver interface plus registry, the `inget` and `http`
resolvers, `key_from`/`key_regex` extraction, payload rendering, per-reference digests, the
invalidation cascade); `refs.stale_depth` on the state schema with `Refs`, `MarkRefsStale` and
`StaleReferrers` on the Store, and reference edges written inside `CheckpointItem`'s
transaction; stage 4 and the cascade in `internal/pipeline/process.go`; the pull half in
`reconcile`; `related_keys` on destination rows; `key_regex` and `token_env` on
`config.Reference` with validation; the `key_regex` the shipped Monday reference needs to turn
a repo URL into a `github/repo` item ID.

Verified: `gofmt -l .` clean, `go vet ./...` clean, `golangci-lint run` (v2.12.2) reports 0
issues, `go test -race -count=1 ./...` passes, `make build` produces both binaries, `go mod
tidy` is a no-op. The gate test is
`TestChangedReferentInvalidatesOnlyTheDependentViews`: the referring item resolves its
reference, a second identical run does nothing, the referent's view text then changes and the
item regenerates 3 of its 8 views — the three whose globs match `ref:linked_repo` — embedding
and upserting only those, with zero fragment derivations; a fourth run is again silent.
The pgvector `related_keys` overlap query and its GIN index are covered by a case gated on
`INGET_TEST_PG`, which has not been observed running here.

### How invalidation works, and what it costs

The mechanism is the part to understand before changing any of it. A record that changed sets
`refs.stale_depth` on every edge pointing at it: one indexed UPDATE, so a widely-referenced
record costs the same as an unreferenced one. The mark is cleared only by the referring item's
own re-resolution, which happens inside that item's checkpoint transaction.

That single choice answers three requirements at once:

- **Cross-datatype.** A run is per datatype, so the referent and its referrer are processed by
  different runs. The mark is durable, so the referrer's next run finds it.
- **Deferral.** `max_cascade_per_run` is applied where the expense is — the pull side, in
  `reconcile` — and an item left out keeps its mark, so the next run's identical query finds it.
  Nothing has to remember what was skipped.
- **Termination.** The mark carries the depth it was made at, and a record already at
  `max_reference_depth` marks nothing. `X → Y → X` stops after the bound.

Cost, since it was asked directly: nothing re-ingests. `inget-fetch` is untouched by a cascade;
an invalidated item is read from the artifact run already on disk. No generator call happens
unless the payload actually moved: fragment fingerprints are unchanged so derivations hit the
level-1 cache, and the level-2 input hash is unchanged so every view is skipped. The floor is a
few state reads plus one blob read per `key_from` glob match. `processItem` also only cascades
for an item that republished something, so a no-op re-resolution ends the chain rather than
passing it along.

### Deviations from the plan and the design

- **No BFS walk and no visited set.** The design's edge-case table pairs
  `max_reference_depth` with "visited-set cycle detection". The depth carried on each mark
  makes a visited set redundant — depth increases monotonically and stops at the bound — and it
  also removes the need to enumerate referrers at all, since one UPDATE marks them.
  `ReferencedBy` is still on the Store, still tested, and is what `inget state show` will read.
- **`stale_depth` is a new column, in migration `00002_ref_staleness.sql` for both dialects.**
  Editing `00001_init.sql` would have diverged from any already-migrated database.
- **The `inget` resolver reads state only, not destinations.** The plan says "state and
  destinations". `state.views.text` is by definition the text the stored vector was produced
  from, so a destination round trip would return the same string while requiring a new
  `Destination` method and a reachable vector database during enrichment — which `inget plan`,
  which opens no destination, does not have.
- **`TemplateData` gained no `References` field**, contrary to the step 8 note. Payloads reach
  prompts through the two paths config already declares: `inject_as: fragment` becomes a compose
  entry, so it lands inside the untrusted-data envelope the view prompts already fence, and
  `inject_as: metadata` lands in `.Metadata`. A third path would be a third place for injected
  instructions to look plausible, and a third answer needed for "which cache key moves when this
  changes".
- **`ViewInputHash` takes a third argument, the metadata-reference digest.** A
  metadata-injected payload is not in the composed document and no glob can scope it, so the
  only honest place for it is every view's level-2 key. It is `""` for a datatype with no such
  reference.
- **Two config fields were added beyond the design's example**: `key_regex` (one capture group,
  validated) and `token_env`. Without the first, the design's own `key_from: "column:repo_url"`
  cannot resolve against a `github/repo` item ID, and the alternative is a resolver that knows
  what a GitHub URL looks like. `token_env` follows the repo's secrets-by-indirection rule; the
  header sent is `Authorization: Bearer <token>`.
- **Reference configuration contributes no signature of its own.** Adding a field changes the
  payload, so the digest moves; removing a reference removes its edge and its compose entry, so
  the composed hash moves. Both are already covered by D2's existing keys.
- **A reference resolves to a key namespace, not a bare ID.** `refs.IngetKey` renders
  `<datatype>|<itemID>` and an http key is `<reference name>|<raw>`, so the reverse index cannot
  collide two unrelated referents. Those strings are also what `related_keys` carries.

### Choices made where the plan was silent

- **`inject_as: metadata` writes `<name>.<field>`**, keeping two references that pull the same
  field name apart.
- **Reference payloads compose at tier 0**, ahead of source files, so a deliberately declared
  reference survives `compose.max_chars` truncation rather than being clipped by whatever else
  was in scope.
- **An unresolved referent records an edge with an empty digest.** That edge is what lets the
  referent's first run invalidate this item once it exists; the case is tested both ways. A
  `key_from` that extracts no key records nothing, so an item with no linked record does not
  re-enqueue itself every run.
- **A metadata-injected change bypasses the level-1 scope check for every view**
  (`viewRequest.anyChanged`), leaving level 2 to decide which actually regenerate.
- **An invalidated item absent from the artifact run is skipped with a debug log**, not
  enqueued: the claim loop cannot process what it cannot read, and it would fail every run
  forever. Its mark is collected with the rest of its state by `inget state gc` (step 13).
- **A cascade failure is logged, not returned.** The item's own work is committed and correct,
  and the next change to it marks again.

### Known gaps

- **An `http` referent changes invisibly.** Nothing tells inget that an external ticket moved,
  so the payload refreshes only when the referring item is processed for another reason. A TTL or
  a `--force` scope would fix it; step 13 owns `--force`.
- **`enrich.max_reference_depth: 0` disables the cascade but not resolution.** References still
  resolve and still guard their views; only the cross-record invalidation stops. That is the
  reading validation already allows ("0 disables reference resolution" in `validate.go` is now
  imprecise wording, left alone rather than changed mid-step).
- **Tombstoning an item leaves its outgoing edges.** They are filtered out of the work set by
  the artifact-run check above; `state gc` should delete them.

## Drift deletion and finish_reason guard (2026-08-02)

Two changes in one session: the fuzzy drift threshold mechanism is deleted, and a
finish_reason/empty-content guard is added to the generator client.

### Task A: drift threshold deletion

The Step 5–6 review repaired drift so that it actually functioned — normalising by runes
instead of bytes, measuring from the embedded text instead of the last text — and it is now
deleted because it guarded a free local operation. The irony is explicit.

**Rationale.** View generation is already gated by the level-2 scoped input hash (stage 7),
which protects the only paid operation. Drift gated stage 9 (embedding, a local TEI server on
Metal) and stage 10 (a local pgvector upsert) — both free. Measured on 2026-08-02, DeepSeek V4
Flash is not reproducible at temperature 0 with a fixed seed: two byte-identical requests
returned 487 and 298 characters, so a 0.02 lexical threshold is exceeded on essentially every
regeneration and never suppresses anything. A semantic replacement would need the new vector
computed before it could threshold on it, so it could only skip the upsert; it would
additionally need the previous vector, which state does not store. The mechanism is therefore
deleted rather than replaced.

**Invariants preserved:**
1. Generation does not happen when the composed input is unchanged (level-2 input hash, stage 7).
2. Embedding does not happen when the view text is byte-identical to the text behind the stored
   vector (`EmbeddedHash` comparison in `reuseVector`).

**Files deleted:** `internal/delta/drift.go`, `internal/delta/drift_test.go`.

**Files changed:** `internal/delta/delta.go`, `internal/pipeline/views.go`,
`internal/pipeline/pipeline.go`, `internal/pipeline/process.go`,
`internal/pipeline/helper_test.go`, `internal/pipeline/views_test.go`,
`internal/config/schema.go`, `internal/config/lookup.go`, `internal/config/load.go`,
`internal/config/validate_lists.go`, `internal/config/load_test.go`,
`internal/config/validate_test.go`, `internal/config/hash_test.go`,
`internal/config/testdata/minimal.yaml`, `internal/config/testdata/reordered.yaml`,
`cmd/inget/deps.go`, `internal/state/types.go`, `internal/enrich/enricher.go`,
`config.yaml`, `go.mod`, `go.sum`.

**Dependency removed:** `github.com/agnivade/levenshtein` v1.2.1.

### Task B: finish_reason and empty-content guard

Measured against the live Moonshot API: kimi-k3 returned HTTP 200 with `finish_reason`
`"length"` and empty content when `max_tokens` was too small. Without this guard, a truncated
or empty completion is returned as valid, cached at level 2, embedded, and upserted as a
legitimate view.

**Files changed:** `internal/model/wire.go` (added `FinishReason` to `chatChoice`),
`internal/model/generator.go` (guard after zero-choices check),
`internal/model/generator_test.go` (four table-driven httptest cases).

### Verified

`make build`, `make test` (`go test -race ./...`), `go vet ./...`, `gofmt -l .` (empty),
`golangci-lint run` (0 issues), `go mod tidy` (no-op) — all green.

### Correction to the record above

The guard tests landed in `internal/model/generator_test.go`, pushing it to 317 lines, over
the 250-line budget. They were moved to `internal/model/guard_test.go` (105 lines), leaving
`generator_test.go` at 226, and the test was renamed
`TestGeneratorRejectsTruncatedOrEmptyCompletions`. A stale "drift threshold" phrase in D10's
Kimi paragraph was also removed. Re-verified: `gofmt`, `go vet`, `make build`,
`go test -race -count=1 ./...`, `golangci-lint run` (0 issues), `go mod tidy` (no-op), and
`levenshtein` absent from both `go.mod` and `go.sum`.

## Live provider verification, 2026-08-02

The first real model calls this project has made. Everything before this ran against fakes or
`httptest`, so the notes below are measurements, not assumptions.

**Embedder — working.** `Qwen/Qwen3-Embedding-0.6B` under Homebrew TEI 1.9.3 on Metal, at
`127.0.0.1:8090`. `/v1/embeddings` returns 1024 dims at L2 norm 1.000000, one object per input
with distinct `index` values, and echoes the model string — satisfying every field
`embedder.go` depends on, with `fit()` needing no truncation because native width already
equals `dimensions: 1024`. Cosine between two unrelated sentences was 0.2995. Warmup takes
about three minutes.

TEI's CPU Docker images are per-architecture and not multi-arch (`cpu-1.9` is x86_64,
`cpu-arm64-1.9` is aarch64), and a container has no Metal or MPS access on any host, so
`deploy/docker-compose.yaml` now takes `TEI_IMAGE_TAG` and `TEI_HOST_PORT` and documents the
Homebrew route as preferred on Apple Silicon. Port 8090 rather than 8080 because a gluetun
container holds 8080 on this machine.

> **Corrected 2026-08-02.** `cpu-arm64-1.9` does not exist. Checked against the registry after
> an `up -d` failed with `no matching manifest for linux/arm64/v8`: `cpu-1.9` publishes
> linux/amd64 only, and no version-pinned aarch64 CPU tag is published at all — `cpu-arm64-1.9`,
> `cpu-arm64-1.8`, `cpu-arm64-1.7` and `cpu-arm64` are all absent. The only aarch64 CPU tag is
> the moving `cpu-arm64-latest`, whose index digest today is
> `sha256:35c50d7494de22deecdb783b8f5b7e1d05765709bd90071b03469b9440d28656`; `name:tag@digest`
> is valid in `TEI_IMAGE_TAG`, which is how to pin it. The compose comment and
> `docs/local-walkthrough.md` were both fixed. Also worth knowing: a failed image pull aborts
> the whole `up`, so a wrong tag leaves postgres down too, which reads like a broken compose
> file rather than a bad tag.

### The compose embedder needed two flags to work at all (2026-08-02)

Running that arm64 image surfaced two defects in `deploy/docker-compose.yaml`, both dating from
step 6 and neither reachable until the container actually ran on this architecture.

**Warmup was OOM-killed.** The container reached `Warming up model` and died six seconds later:
`exit=137 oom=true`, with the HTTP port never opening, so it presented as a hang — `docker
compose ps` showed `health: starting` and `curl` got connection-refused rather than a 503. The
cause is `--max-batch-tokens`, which defaults to 16384 on CPU and bounds the batch warmup
allocates; a 16384-token forward pass of Qwen3-Embedding-0.6B exceeded a 7.75 GB Docker VM that
also held postgres. Set to 4096: warmup now completes in about 58 seconds after roughly 70
seconds of weight download, and 4096 is still far above anything inget sends, since generated
view text is bounded by `models.generator.max_output_tokens` and passthrough text by
`compose.max_chars`.

**Every full batch would have been rejected.** `--max-client-batch-size` caps the inputs one
request may carry and defaults to 32, while `config.yaml` sets `models.embedder.batch_size: 64`.
Set to 64 in compose, and confirmed both directions against a live router: 64 inputs to a
default-configured native TEI returns
`422 {"message":"batch size 64 > maximum allowed batch size 32","type":"Validation"}`, 32 inputs
succeed, and 64 inputs to the reconfigured container return 64 vectors.

Which commands that breaks is worth writing down, because the failure arrives late and looks
unrelated to batching. `inget run` on `github/repo` calls `Embed` once per item with that item's
pending views — eight, under the limit. `inget reindex` claims `DefaultBatchSize` 8 items and
embeds their views in one call: 64. `inget eval` embeds the whole sample, which the client splits
into requests of `models.embedder.batch_size`: 64 again. So a run can succeed for hours and eval
or reindex still fail on the first call. `internal/model/retry.go` does not retry a 4xx other
than 429, so it fails loudly rather than corrupting anything.

The same two flags are needed on the native Homebrew router, whose defaults are identical — its
startup `Args` line prints `max_batch_tokens: 16384, max_client_batch_size: 32`. Both the
compose comment and the walkthrough now show them.

Measured warmup, same model on the same machine: about three minutes at the default 16384 batch
tokens, about one minute at 4096, on either backend. The native router does get Metal
(`Starting Qwen3 model on Metal(MetalDevice(DeviceId(1)))`) where the container reports
`on Cpu`, which confirms the standing advice to prefer the Homebrew build on Apple Silicon.

Verified through the container afterwards: a single input returns 1024 dimensions at unit norm,
reported model `Qwen/Qwen3-Embedding-0.6B`, which is the width `config.yaml` pins and
`halfvec(1024)` in the destination expects.

Two log lines in that startup look like failures and are not: `Could not download
onnx/model.onnx` (404) and `Could not start ORT backend`. TEI's CPU image probes for an ONNX
export, this model publishes none, and it falls back to Candle — `Starting Qwen3 model on Cpu`
is the line that says it worked. The walkthrough says so, because the ERROR level does not.

Related and unaddressed: TEI logs `--auto-truncate` defaults to true, so an input over the
batch-token bound is silently shortened rather than refused. Nothing the shipped configuration
sends comes near 4096 tokens, but a passthrough datatype with a large `compose.max_chars` could,
and the failure mode would be an index quietly built from truncated text.

**Generator — DeepSeek V4 Flash, working.** `temperature: 0`, `seed` and
`max_output_tokens: 1024` are all accepted; a two-sentence answer finished with
`finish_reason: stop` at 73–109 completion tokens, of which 25 were reasoning tokens.
`prompt_cache_hit_tokens` was 0 even on an identical repeat, attributed to the prompt being
below DeepSeek's caching minimum — `plan`'s cache-hit estimates remain unverified against a
real composed document.

**Generation is not reproducible.** Two byte-identical requests at `temperature: 0` with
`seed: 1` returned 487 and 298 characters of different wording. This is a provider property,
not a client bug. It is what motivated deleting the drift threshold, and it forced the D14
change below.

**Kimi K3 — rejected as default.** Refuses every temperature but 1, and `wire.go` sends
`temperature` unconditionally, so the configured 0 makes every call a hard 400. It also spends
its output budget on reasoning before emitting content: 509 reasoning tokens and empty content
with `finish_reason: length` at `max_tokens` 512, needing 2048 to finish. Recorded in D10.

### D14 changed: eval embeds stored view text and never generates

Agreed this session and recorded in D10, D14 and the Step 12 plan. `inget eval` reads
`state.views.text` and re-embeds it rather than regenerating views, because generator
non-determinism would otherwise fold into every metric and confound the `--embedder`
comparison the same decision relies on. Consequences for whoever implements Step 12: no
generator credentials are needed, a corpus with stored view text is a hard prerequisite
(`inget-fetch` then `inget run`), and an empty sample must be reported as empty rather than
scored as a pass.

### Step 12 is not started

No `eval` command exists in the CLI or anywhere in the Go sources. None of its four
deliverables — sampling, the five metrics, per-datatype reporting with a non-zero exit, the
`--embedder` override, threshold config — are implemented. This session did prerequisites and
corrective work only. `.inget/` was cleaned at some point, so the corpus needs re-fetching,
and `INGET_STATE_DSN` and `INGET_PGVECTOR_DSN` are not yet exported.

## Step 12 record

Implemented: `internal/eval` (sampling, the five metrics of D14, the metadata-derived query,
per-datatype and per-view reporting, the verdict); `eval:` config block with the documented
thresholds, defaulted in `normalize` and range-checked in `validate`; `state.ViewedItems` on
the Store with a conformance case; `cmd/inget/eval.go` wiring the command, the three embedder
overrides and JSON-on-stdout reporting with a non-zero exit; `selectDatatypes` lifted into
`cmd/inget/helpers.go` so `run`, `plan` and `eval` resolve the datatype argument once.

Verified: `gofmt -l .` clean, `go vet ./...` clean, `golangci-lint run` (v2.12.2) reports 0
issues, `go test -race -count=1 ./...` passes, `make build` produces both binaries, `go mod
tidy` is a no-op. The gate test is `TestDegradedViewsBreachThresholds`: the same four items
with every view replaced by one generic sentence breach the distance thresholds while coverage
still passes, which is the point of scoring five metrics rather than counting rows.
`TestLiveEmbedderSeparatesHealthyFromDegraded` ran against Qwen3-Embedding-0.6B under Homebrew
TEI 1.9.3 on Metal and measured, on the healthy fixture: self-retrieval 1.0000 over 12,
distinctiveness 0.5759 over 54 pairs, coverage 1.0000, view distinctiveness 0.2776 over 4
items, metadata top-3 1.0000 over 4 — every threshold cleared. The degraded fixture scored
self-retrieval 0.2500 and both distances 0.0000.

### What the harness measures, and why not the obvious thing

Retrieval is computed **inside the re-embedded sample**, not against a destination. The plan
does not say which, and the destination was the wrong answer for three separate reasons: the
sample is embedded by the model under test, whose vectors a destination bound to another model
must reject (D7); the vectors already in a destination were produced by the configured
embedder, so `--embedder` could not be compared against them at all; and a quality gate that
needs a reachable vector database is a gate that stops being run. The cost is that scores are
relative to the sample, so `eval.sample_size` is part of a score's meaning — two runs compare
only at a fixed size, which is also why the sample is drawn in digest order rather than at
random.

**Self-retrieval excludes the query's own vector.** With the vector included the metric is
trivially 1.0 for every corpus, since a query identical to a document always ranks that
document first — it would have measured nothing. Leave-one-out makes it measure whether an
item's views retrieve *each other* before another item's, which is the property "a view's own
text retrieves that item at rank 1" was after. An item with one view therefore has nothing to
be retrieved by and is not scored.

**A metric with no evidence is skipped, not scored.** A skipped metric is neither a pass nor a
breach: a single-view datatype cannot have self-retrieval or view distinctiveness, and calling
that a failure would report a configuration choice as a quality problem. An unscorable
*corpus* is different and does not pass: no items, no stored views, no view under a configured
name, or a single item all exit non-zero with a reason naming what to run.

### Choices made where the plan was silent

- **The sample is drawn from items that have stored views**, not from all live items, and the
  report carries `live_items`, `viewed_items` and `sampled_items` so the difference is visible.
  Sampling unprocessed items would have scored "the pipeline has not finished" as "the prompts
  are bad". That needed one new read: `ViewedItems`, one `SELECT DISTINCT item_id`, because
  finding processed items by asking per item is one query per item in the corpus.
- **View rows under names configuration no longer declares are ignored.** They are stale state
  awaiting `state gc`, and scoring them would report on a view nobody asked for.
- **The metadata query is built by value shape, not by key name.** Metadata keys differ per
  connector, so nothing generic can name the useful fields; what is generic is that a URL, a
  timestamp, a bare number and a single character say where or when a record lives rather than
  what it is about. Those are dropped, the rest are joined in key order, duplicates once, and
  the keys themselves are excluded because they are identical across every item of a datatype
  and would dilute the part that discriminates. An oversized value is clipped rather than
  dropped: a long description is the most informative field an item has.
- **Thresholds are applied per datatype, and the per-view breakdown is informational.** One
  weak view out of eight is a prompt to fix, not a reason to fail a datatype, and the report
  carries per-view coverage, self-retrieval, vector count and mean length so that which one it
  is takes no second command. `MeanChars` is there because an unexpectedly short view is the
  first symptom of a prompt that has started refusing.
- **Thresholds are plain floats defaulted in `normalize`, not pointers.** The schema's stated
  convention is a pointer for a field whose zero value is meaningful, but `EnvKeys` walks a
  zero `Config` and `flatten` skips nil pointers, so a pointer threshold would not have been
  env-overridable — which is exactly how CI would want to set one. The cost is that 0 is
  indistinguishable from unset; it would gate nothing either way.
- **Three embedder override flags, not one.** `--embedder` alone cannot express a real A/B: a
  second model usually means a second endpoint and a different native width, and a width
  mismatch is rejected by the embedder client before any score exists. `--embedder-url` and
  `--embedder-dims` complete it, and a truncation target the new model cannot satisfy is
  dropped rather than left to fail every call.
- **`eval` is a positional argument like `run` and `plan`**, not the `--datatype` flag the
  design's CLI sketch shows, matching what the two implemented commands already do.

### Embedder dedupe: a measured server bug, fixed in the client

`internal/model/embedder.go` now sends a repeated text once and fans the vector back out.
This is not an optimisation. Measured against Homebrew TEI 1.9.3 on Metal serving
Qwen3-Embedding-0.6B on 2026-08-02:

- A text appearing more than once in one `/v1/embeddings` request came back with a vector
  whose cosine against the same text embedded alone was 0.25 — internally consistent across
  the duplicates, unrelated to the correct vector.
- It is nondeterministic: `[mid, mid]` returned the correct vector twice on two attempts and
  the wrong one on a third.
- Distinct inputs are unaffected. A four-text batch, and mixed-length pairs in both orders,
  matched their single-text embeddings at cosine 1.000000, so this is not a padding or
  last-token-pooling problem.
- Single-text requests are stable across repeats.

The client-side fix is correct independently of the bug — identical text must embed
identically — and it is why the degraded fixture now scores distinctiveness 0.0000 instead of
0.1244. It also cut the live test from 6.76s to 0.83s by embedding 5 unique texts instead of
16. The pipeline's `embedViews` benefits without change, which matters: two views of one item
producing byte-identical text would otherwise have stored a garbage vector silently.

### Known gaps

- **No live end-to-end run behind the harness.** The plan's prerequisite — `inget-fetch` then
  `inget run` for one datatype, then `inget eval` over the result — has not been done here.
  `INGET_GITHUB_TOKEN` is not in this environment, no PostgreSQL or pgvector is running, and a
  real run spends generator tokens. What has been exercised end to end is `inget eval` against
  an empty sqlite state (reports unscorable, exits 1) and the harness against seeded state with
  both a fake and a live embedder.
- **The retrieval pool is the sample, so the metric gets easier as the corpus grows** relative
  to a real query against the whole index. A pool of every stored view would need the whole
  corpus re-embedded per run, which is the cost D14 chose not to pay. Watch for scores that
  look good on a 20-item sample and worse in `inget query`.
- **`metadata_top3` cannot be scored for a datatype whose items carry no prose metadata.** It
  is reported as skipped, which is honest but means one of the five gates is silently absent
  for such a datatype.
- **The Monday datatype is still unmeasured**, since step 10 has no connector, so nothing has
  exercised the harness against a `passthrough`-style corpus where the embedder does the
  semantic work — the case D14 says reporting per datatype exists for.

## Step 13 record

Implemented: `internal/gc` (three-phase collection, locking, dry run); `internal/artifact/gc.go`
(`RunDirs`, `DeleteRun`, `Blobs`, `DeleteBlob`, `Run.BlobRefs`); `internal/state/gc.go`
(`LiveBlobRefs`, `CollectDerivations`) and `internal/state/inspect.go` (`Counts`, `RecentRuns`,
the only timestamp read in the package); `Store.ForceUnlock`; `internal/reindex` (rebind, resumable
re-embed pass, prune); `destination.RebindModel` and `destination.PruneStaleVectors` with the
pgvector implementations; `cmd/inget/reindex.go`, `cmd/inget/query.go`, `cmd/inget/state.go`
(`show|gc|unlock`), all wired into `main.go`; `deploy/kubernetes/` (base, three CronJobs, a
one-shot reindex Job). `pipeline.AnnotateMetadata` and `pipeline.ShuttingDown` were exported so
reindex assembles row metadata and observes shutdown through the same code the pipeline uses,
rather than a second copy of each.

Verified: `gofmt -l .` clean, `go vet ./...` clean, `golangci-lint run` (v2.12.2) reports 0 issues,
`go test -race -count=1 ./...` passes, `make build` produces both binaries, `go mod tidy` is a
no-op — no new dependency was needed for this step. Both postgres-gated suites were run against
`pgvector/pgvector:pg17` from `deploy/docker-compose.yaml`, which is what covers the new SQL:
`internal/destination` (rebind, prune, the write path's reaction to a rebind) and the
`internal/state` conformance suite on both drivers.

The step's acceptance was then exercised live, with Qwen3-Embedding-0.6B under Homebrew TEI 1.9.3
on Metal writing into that pgvector, over three seeded items with two views each and
`truncate_dims: 256`:

- Pass 1 embedded 6 views, bound the registry, and left every row and every `views` row carrying
  `qwen3-embedding-0.6b` at 256 dims.
- Pass 2 with the same configuration reported `items=0 unchanged=3 vectors=0` — a reindex is free
  to retry.
- `inget run` under a changed embedder refused with the D7 message naming `inget reindex`, which is
  the deadlock the command exists to break.
- Pass 3 under the changed embedder reported `rebound=[smoke] items=3 vectors=6`, and both the
  table and `inget_model_registry` moved to the new model.
- Deleting one `views` row then reindexing reported `vectors=5 pruned=1`: the orphan vector was the
  one row the prune deleted.
- `inget query` returned sensible rankings under both bindings (terraform query: 0.7402 on
  `terraform-vpc/role`; python query after the rebind: 0.8159 on `etl-pipelines/role`).
- `inget state gc --dry-run` completed against real advisory locks, and exited with
  `lock is held by another process` while a `psql` session held `hashtext('github/repo')`.

### Reindex re-embeds and does not regenerate

The plan says "regenerate or re-embed as required". Reindex only re-embeds, and the regenerate half
stays in `inget run`. The reason is that regeneration needs the artifact run — compose the
fragments a view depends on, run the prompt, validate the output — which is stages 5 to 11 of the
pipeline. That code exists, already detects a moved enricher signature through the level-2 guard,
and already prices the work in `inget plan` before anything is spent. A second implementation of it
inside reindex would have to open the artifact store, resolve references, hold the generator budget
and account for cost, and would be the only path that spends generator tokens from a command whose
whole point is that it does not.

What reindex covers is the case `inget run` *cannot*: an embedder change makes the destination
refuse every write until the registry is rebound, and `AssertModel` refuses to rebind by design.
Nothing else can move that binding, and having moved it, the mover owes the index a rewrite. So the
division is: reindex owns the model binding and the vectors, `run` owns the text.

### Choices made where the plan was silent

- **`state gc` takes no datatype argument.** Whether a blob is referenced is a claim about every
  datatype at once, because content addressing shares blobs across datatypes and sources.
  Collecting one datatype's runs would compute the retention set from that datatype alone and
  delete blobs another still points at. The blob phase is therefore global, and the command's scope
  is the whole configuration.
- **Collection holds both locks of every datatype for its duration.** `<datatype>` is what a
  pipeline run takes and `fetch:<datatype>` what a fetch takes, and the dangerous overlap is a
  fetch that has uploaded blobs but not yet committed the manifest that references them. Holding
  both makes "unreferenced" true for as long as it takes to act on it; the cost is that gc and a
  run cannot overlap, which is the constraint every other writer here already accepts.
- **Blobs of an uncommitted run directory are collectable.** Its records cannot be read, so there
  is no way to know what it references. Deleting them is recoverable — the next fetch re-uploads
  exactly what it needs, since `HasBlob` will miss — and the alternative is retaining the debris of
  every crashed fetch forever. Phase 1 still retains young uncommitted directories, so the two
  phases disagree on purpose.
- **The prune is scoped by what the pass actually rewrote.** Full view scope, completed, at least
  one item: anything less and the predicate "not written by the current binding" catches rows the
  pass skipped rather than rows nothing wants. The empty-corpus case is the one worth stating: a
  datatype with no stored views prunes nothing, because pruning there empties the table.
  `--prune-unconfigured` is opt-in and demands a complete pass over every datatype, every view and
  every destination.
- **A second reindex pass is free, and `--force` is how you pay anyway.** A view whose stored
  model, signature and embedded hash already match the configured embedder is skipped, which makes
  the command safe to retry and safe to leave in a Job that might be re-applied. `--force` exists
  for the case that guard gets wrong: a destination that lost rows while state still claims they
  are current.
- **Reindex runs record `inget-reindex` as their binary.** `ResumableRun` keys on binary, datatype
  and config hash, so a distinct name is what stops a half-finished reindex from being adopted as a
  resumable pipeline run and the reverse. They compute different work sets from the same datatype.
- **Rows are written with `BulkLoad`, one call per batch.** The interface documents it as the
  reindex path, and a batch's rows become one set-based merge rather than one statement per row.
  Views are embedded per batch rather than per item for the same reason: one call, and the
  embedder client's dedupe means a repeated text is sent once.
- **The HNSW index is not dropped and rebuilt.** `migrations/destination/pgvector/00001_init.sql`
  suggests reindex is where that happens, and it would be faster on a cold load, but the pass is
  resumable and an interruption between the drop and the rebuild would leave a table with no vector
  index and nothing to recreate it — goose has already applied that migration. A slower reindex is
  better than an index that silently went missing.
- **`state unlock` takes a datatype and releases both of its keys**, rather than taking a raw lock
  key. The keys are an implementation detail of two packages; the thing an operator knows is which
  datatype is stuck.
- **`ForceUnlock` does not terminate a postgres backend.** An advisory lock cannot be stolen, only
  outlived, so the postgres path probes the lock and reports `ErrLocked` if a live session holds it.
  Killing another process's session on an operator's behalf is not a decision this command should
  make, and there is no stale-lock case on that driver to justify it.
- **`inget query` searches every configured datatype when none is named**, using each datatype's
  first destination, and merges the hits by score. The design's own verification writes
  `inget query "which repos handle terraform"` with no flags, which only works if a bare query
  means something. Each search asserts the model binding first: a query vector from another model
  is the one failure mode that produces plausible-looking nonsense.
- **`state show` degrades when the blob store is unreachable.** The artifact store is opened for one
  field per datatype — the newest committed run — so an unreachable bucket logs a warning and leaves
  that field empty rather than failing the command. A diagnostic that needs everything to be
  working is a diagnostic nobody can use when something is not.
- **`Counts` is nine separate queries.** One statement with nine subselects would be one round trip
  and considerably harder to read, and this is a command a human runs occasionally.

### Known gaps

- **Reference-injected metadata is not reproducible by a reindex.** `resolved.Metadata` is not
  persisted — state holds the item's own metadata and the reference edges — so a rewritten row
  carries item metadata, the configured `metadata_fields` and `related_keys`, but not the values an
  `inject_as: metadata` reference resolved to. The pass warns once per datatype that declares one,
  and `inget run` restores them the next time the item changes. Persisting the resolved payload in
  the checkpoint would close this; it was not in scope here.
- **No live gc against a populated store.** The phases are unit-tested against a real file:// store
  with an aged ULID, and the lock behaviour was checked live, but nothing has run a collection over
  a store with real runs and thousands of blobs. Watch the first real run with `--dry-run` and read
  the retained/deleted counts before letting the CronJob delete anything.
- **The Kubernetes manifests are unvalidated against an API server.** They parse, and the ConfigMap's
  embedded `config.yaml` was loaded by the binary to prove it validates, but no cluster was available
  to `kubectl apply --dry-run=server` them, and the image tag they name does not exist until step 14
  publishes it.
- **`state show` reports no work-queue detail.** It counts guards and lists recent runs, but not how
  many `work` rows a run still has pending, which is what someone watching a long resume actually
  wants. The rows are there; the read is not.
- **Reindex is single-threaded across batches.** Items are claimed in batches and each batch is one
  embedding call, but batches run in sequence, so a large corpus is bounded by embedder latency
  rather than by throughput. The work-claiming design already supports several processes running the
  pass at once, which is the cheaper way to fix it than a worker pool inside one.

## Step 14 record

Implemented: `.goreleaser.yaml` (two builds, one archive carrying both binaries plus
`config.yaml` and `prompts/`, checksums, a git-derived changelog, `dockers_v2` publishing one
multi-arch image, `homebrew_casks` publishing to a tap); `Dockerfile` (distroless static
pinned by index digest, both binaries, prompts, `WORKDIR /opt/inget`, `USER nonroot`);
`.github/workflows/release.yaml` (tag-triggered: vet and test, then buildx, ghcr login and
goreleaser pinned to v2.17.1); `make release-check|release-snapshot|release-install`; `dist/`
in `.gitignore`. Documentation: the README's status block, install section and a new
Releasing section; AGENTS.md gained a Releasing section, the new files in the layout, and four
release hazards. Twelve test files that lacked a top block comment got one; a sweep found no
exported symbol without a doc comment.

Two defects in earlier steps were fixed while verifying this one. `deploy/kubernetes/base.yaml`
named `prompts/github/repo/{fragment,role}.md`, but the files are `.tmpl` — validation checks
config syntax, not that a prompt exists, so it would have failed at the first `inget run`
rather than at load. And every manifest pinned `ghcr.io/maxwellcudlitz/inget`, a namespace this
repository's workflow token cannot write to; they now name `ghcr.io/maxwell-cudlitz/inget`.

Verified: `gofmt -l .` clean, `go vet ./...` clean, `golangci-lint run` (v2.12.2) reports 0
issues, `go test -race -count=1 ./...` passes, `make build` produces both binaries.
`goreleaser check` validates the configuration, and `goreleaser release --snapshot --clean`
completed on an arm64 Mac: 8 binaries, 4 archives, `checksums.txt`, the rendered cask, and both
platform images built locally. Then, against the artifacts rather than the source tree:

- the darwin/arm64 archive extracts to a self-sufficient directory — both binaries report
  their version, `inget migrate` and `inget plan` load the bundled `config.yaml` and fail only
  on absent credentials, and all eleven prompt paths that configuration names resolve inside
  the archive;
- the image runs `inget version` and `inget-fetch version`, reports `WORKDIR /opt/inget`,
  `USER nonroot:nonroot` and `INGET_CONFIG=/etc/inget/config.yaml`, and carries all eleven
  prompt templates at `/opt/inget/prompts`;
- the rendered cask points at `github.com/maxwell-cudlitz/inget` releases, declares both
  binaries, and carries the quarantine-clearing postflight block.

Cross-platform builds were confirmed to need no QEMU: the linux/amd64 image built on an arm64
host, because the Dockerfile only copies and never executes.

### A cask, not a formula

The plan says "Homebrew tap formula". goreleaser deprecated `brews` in favour of
`homebrew_casks`, and Homebrew's own guidance is that a tap shipping pre-built binaries ships
casks; `brews` is scheduled to disappear in goreleaser v3. Following the tool costs one thing:
a cask is macOS-only, so Linux users take the archive or the image, which is what the README
now says. The other consequence is Gatekeeper. An unsigned binary installed from a cask is
quarantined and reports "inget is damaged and cannot be opened", so the cask clears the
attribute in a postflight hook. That bypasses Apple's verification and is documented as such in
the caveats and in the README's security notes; the alternative is a paid Developer certificate
and notarization, which is not a decision a release step should make on its own.

### Choices made where the plan was silent

- **No Windows target.** linux and darwin, amd64 and arm64. Nothing in the project has ever
  been run on Windows, the deployment target is a Linux container and the development target is
  a Mac, and shipping a binary nobody has executed is worse than not shipping it. Every
  dependency is pure Go, so adding `goos: windows` later is a one-line change plus a test pass.
- **The archives carry `prompts/` and `config.yaml`.** Migrations are embedded and prompts are
  not — `enrich.LoadPrompt` reads the path each view names — so a download of binaries alone
  cannot run. That also decided the image layout: `prompts/` is copied in and `WORKDIR` is
  `/opt/inget`, which is what makes the relative paths in a mounted config resolve. Embedding
  prompts would remove the whole problem and would also make the shipped bytes unmodifiable,
  which contradicts the reason they are files; it is a design question, not a packaging one.
- **No config.yaml in the image.** The repository's copy names localhost endpoints, wrong in
  every container. A missing file fails at load naming the path it wanted, which is a better
  first run than a silent connection to nothing, and `INGET_CONFIG` already points where the
  manifests mount theirs.
- **One image with both binaries.** The manifests choose with `command`, so the fetch job and
  the run job pull the same layers and cannot be version-skewed across one artifact envelope.
  Two images would halve each pull and double the tag surface for no other gain.
- **The base image is pinned by index digest, not by tag.** `nonroot` moves, and an image that
  rebuilds onto an untested base is the failure a pin exists to prevent. It must be the
  top-level index digest: a per-platform digest would drop an architecture from the manifest.
- **No QEMU in the release workflow.** Nothing is executed for the target platform, so
  emulation buys nothing but minutes. This is only true while the Dockerfile has no `RUN`, and
  the comment in the workflow says so.
- **goreleaser is pinned to v2.17.1**, in the workflow and in the Makefile, exactly as
  golangci-lint is. A release pipeline that changes underneath a tag is not a pipeline.
- **`mod_timestamp` is the commit timestamp.** Two builds of one tag otherwise differ by the
  mtime embedded in the binary, which makes checksums unreproducible for no reason.
- **The changelog is read from git, not from the GitHub API.** History here is linear commits
  on main — `step N: <goal>` — so a pull-request-based changelog would be empty.
- **`skip_upload: auto` on the cask, and no `latest` for a prerelease.** Tagging `v0.1.0-rc.1`
  publishes binaries and an image but must not hand every `brew upgrade` a release candidate or
  redirect an unqualified `docker pull`.
- **The release workflow re-runs vet and the suite.** CI already ran them on main, but a tag can
  be pushed at any commit, and a release is the one build that cannot be taken back.

### Known gaps

- **No release has been published, so the acceptance criterion is unproven end to end.** What
  was verified is everything up to the push: the configuration validates, every artifact builds,
  and each one was inspected or executed. What cannot be verified from here is `brew install` on
  a clean machine, the ghcr push, and the tap commit — all three need a tag, a published tap
  repository and the `HOMEBREW_TAP_TOKEN` secret. Cut `v0.1.0` once the tap exists and read the
  workflow log rather than assuming.
- **The tap repository does not exist yet.** goreleaser commits into
  `maxwell-cudlitz/homebrew-tap`; it does not create it. Until then the cask step fails after
  the binaries and the image are already published, which is recoverable by re-running the job.
- **`deploy/kubernetes/` still pins `v0.1.0`.** Correct once that tag is cut, wrong at every
  later version: manifests and releases are versioned separately here, so a bump has to reach
  both.
- **The module path still disagreed with the repository owner when this record was first
  written, and was renamed immediately after.** The module is now
  `github.com/maxwell-cudlitz/inget`: `go mod edit -module`, the import in 93 files, the `-X`
  paths in the Makefile and `.goreleaser.yaml`, and the wrapcheck glob in `.golangci.yml`. Test
  fixtures that use `maxwellcudlitz/...` as a synthetic item ID were left alone — they are
  arbitrary strings, not references to this repository.
- **The Kubernetes manifests remain unvalidated against an API server**, unchanged from step 13,
  except that the image they name is now one a release can actually publish and the prompt paths
  in the ConfigMap now match files that exist.
- **The image is not signed, and its SBOM attestation is unverified.** `dockers_v2` attaches
  one by default, but a snapshot build pushes no attestations, so nothing here has confirmed it
  lands. Cosign with GitHub OIDC would add signing; it needs `id-token: write` and a decision
  about key policy, which is not this step's to make.

### docs/local-walkthrough.md

Added after the step's own commit: a front-to-back local run — compose stack, 50 popular but
size-bounded public repositories, fetch, plan, run, eval, query — plus a curl read path. Three
things in it are decisions rather than transcription:

- **The corpus is chosen by `curl`, not by configuration.** The connector enumerates orgs,
  explicit `owner/name` entries or `topic:` searches; it has no popularity ordering and does not
  build arbitrary search qualifiers. So the walkthrough queries the search API for the ten
  most-starred repositories in each of five languages and hands the result to `--only`. That
  keeps configuration untouched, and `--only` marks the run partial so no tombstones are issued.
- **`size:<20000` is load-bearing.** GitHub reports checkout size in kilobytes, and the bound is
  what keeps every archive inside `limits.tarball_max_bytes`. Verified against the live API: the
  per-language query returns 50 repositories with a maximum of about 20 MB. Sorting by stars
  alone returns curated link lists — `awesome-*`, `build-your-own-x`, `public-apis` — in which
  `stack`, `surface` and `operations` have nothing to read.
- **"Commands over curl" is curl plus psql, because there is no server.** An HTTP surface is an
  explicit non-goal, so the walkthrough curls the embedder for a query vector and hands it to the
  same statement `Search` issues, including both `SET LOCAL` settings. The vector goes in as a
  psql variable rather than interpolated text, and the section says plainly that this path skips
  `AssertModel` — the one guard a hand-written query loses.

Every JSON field, key layout and flag in it was read out of the source rather than recalled: the
fetch `Result` tags, `Plan` and `Estimate`, the eval report, `query --json` (whose view field is
`view`, not `view_name`), `state show`, the run directory layout (`runs/<source>/<datatype with
/ flattened to _>/<ULID>`), and the rendered `halfvec(1024)` cast. At 289 lines it exceeds the
250-line convention; it was reviewed and tightened twice, and splitting a front-to-back
walkthrough into parts would defeat what it is for.

The no-spend variant it documents (`enricher: passthrough`, `fragment_enricher.enabled: false`,
`compose.max_chars: 8000`) is the answer to a question this step raised and could not otherwise
settle: how to exercise the whole path on a real corpus without a generator bill. It lowers
`max_chars` because passthrough embeds the composed document verbatim, and the shipped 120000
characters is at or past the embedder's input limit.

## Post-step work, 2026-08-02

### Fragment input is bounded, and the estimate stopped pricing what is never sent

`fragment_enricher.max_input_chars` fed only the enricher signature: the whole file was rendered
into the prompt, and `models.generator.max_input_chars` then rejected anything over 120,000
characters. Both halves were wrong — a 1 MB file failed the run instead of costing money, and the
setting documented a bound nothing applied. It now truncates in `enrich.FragmentLLMEnricher`, at a
rune-safe line boundary with a notice appended, and the fragment enricher's schema version is 2
because derivations cached under 1 came from untruncated content.

`inget plan` was reporting $10.34 for a 50-repository corpus. Three of the four causes were
estimation rather than spend: input priced without either truncation bound, 418 fragments with no
stored blob counted as derivations (the pipeline also sent a blank-data prompt for each, now
skipped), and output priced at the full `max_output_tokens` for every call. The last one stays —
it is the documented upper bound — so the same artifact run now prices at $7.64 with input at
17.6M tokens rather than 36.1M.

### The connector's path filter cannot see content

The fourth cause was real: a JVM heap dump (`java_pid26365.hprof`, 15 MB in 1 MiB pieces), webpack
bundles, wordlists, CDP protocol dumps and CSV exports were being summarised one file at a time.
`internal/source/github/content.go` judges shape once bytes are in hand and before splitting, so a
dump is one decision rather than sixteen: non-text at any size, and above 256 KB a longest line
over 5,000 bytes, a mean line under 16 bytes, or a bulk extension.

The mean-line bound was 40 in the first draft and flagged a 275 KB hand-written Rust file, which
averages 34 bytes a line. That measurement is why the bound is 16 and why bulk extensions carry
the cases a mean cannot: nobody hand-writes 256 KB of JSON. Measured over the sample corpus: 119.5
MB of fragment content down to 88.3 MB by shape, plus 25.5 MB the new path patterns never
transfer, with no source file among the exclusions.

### The consumer drains the artifact backlog (extends D6)

D6 defines full and partial scope; it does not say how many runs a consumer reads. Reading only
the newest was implicit, and it makes a many-producer topology silently lossy: with one run per
item — a CI job announcing its own repository — every submission but the last is never read, and
nothing complains, because a partial run proves nothing about the items it omits.

`inget_state.consumed_artifacts` (migration 00003) records how far each datatype has consumed and
`inget run` drains everything past it, oldest first. The mark advances only behind a pass with no
failed item, not narrowed by `--only` or `--limit`, and not interrupted — anything else means read
this run again, which costs one reconcile and lets the failure retry. An unmarked datatype
consumes the latest run only, so adopting the mark does not replay a month of retained runs, and
the upsert is guarded so it cannot rewind.

Two consequences recorded rather than fixed. A plan over a backlog prices only the oldest pending
run, making it a lower bound where every other estimate is an upper bound; `pending_artifact_runs`
and a warning are what say so. Summing per-run estimates would double-count, because state does
not advance during a plan. And `inget state gc` collects runs past `retention.runs` whether or not
they were consumed, so the drain has to keep up with retention.

`docs/self-submission.md` is the worked example: `inget-fetch --only "$GITHUB_REPOSITORY"` in a
push workflow, shared artifact storage, and a consumer elsewhere. It needs no new code — the
artifact store is the submission interface, and `inget run`/`inget plan` never construct a
connector.

### The view prompts asked for more than the output budget allowed (2026-08-02)

The first real `inget run` against DeepSeek failed every item it reached: 50 work items, 2,657
fragment derivations cached, zero views, zero vectors. Each item died on `role`, the first of its
eight views, with `finish_reason: length` — and so never attempted the other seven.

The cause was in the prompts, not the model. `role` and `internals` asked for "3-6 paragraphs" of
"clear, detailed description" while `models.generator.max_output_tokens` is 1024, which is roughly
four short paragraphs. The generator treats a `length` finish as a hard error, which is right: a
description truncated mid-sentence should not be embedded and cached as though it were complete.
The view prompts now ask for 1-3 short paragraphs and say to stop, and `aliases` is bounded at 40
lines.

Shortening the prompts was preferred over raising the budget for two reasons. `max_output_tokens`
is part of the fragment enricher's signature, so raising it would have invalidated the 2,657
derivations already paid for; and a view is embedded as a single vector, where density retrieves
and padding does not.

The same run surfaced a second, independent failure: `compose.max_chars` was 120000 and so was
`models.generator.max_input_chars`. Because the generator rejects rather than truncates an
oversized prompt, every item whose composition reached the cap failed on the template's own bytes
— HelloGitHub composed to exactly 120,000 and produced a 121,570-character prompt. Two changes
follow. `validateComposeFits` now requires a margin of `min(8192, max_input_chars/8)`, so the
misconfiguration fails at startup instead of after the derivations underneath it have been bought.
And `DefaultComposeMaxChars` is 100000 rather than the 120000 in the design document: a default
equal to the shipped input bound is a default that cannot be used.

Test fixtures carrying `compose.max_chars: 5000` against `max_input_chars: 4096` were themselves
invalid under the new rule — they described a document that could never be sent — and were
corrected rather than exempted.

### Thinking mode was spending the whole output budget (2026-08-02)

Shortening the view prompts was not enough: `role` still failed with `finish_reason: "length"` on
large repositories, and items that got past `role` failed on `internals`. The cause was not the
prompts at all. DeepSeek V4 Flash enables thinking by default, reasoning tokens are generated
inside `max_tokens` and billed at the output rate, and one published live test exhausted a
512-token allowance entirely on reasoning before producing any answer. Against a 100,000-character
composed document the 1024-token budget was gone before the first word of the description.

Two further consequences of the same contract, both silent. Reasoning is billed at the output
rate, so every fragment derivation was paying for thinking nobody read. And DeepSeek documents
that `temperature`, `top_p` and `seed` have no effect in thinking mode — so the determinism the
cascade's cache keys claim, and which `buildSignature` records as `temperature=0,seed=1`, was not
being provided by the model.

`models.generator.request_options` is the fix, and it is deliberately generic rather than a
`thinking:` field: an opaque map merged into the chat completion body, so DeepSeek's toggle, a
reasoning effort, or some future provider's parameter all work without touching this code. It
feeds the generator signature for the usual reason (D2) — a parameter that changes output must
invalidate what was cached under the old value. Keys the client sets from typed configuration are
refused at config load, because a `max_tokens` smuggled through the map would void every
truncation guard while the signature still claimed the old budget.

The shipped config disables thinking. These are summarisation tasks; the reasoning bought nothing
and cost the budget it needed to answer.

Cost of the change, recorded because it is the kind of thing that should be visible: it
invalidated 4,091 cached derivations, about $1.50 of already-spent generation, and the user
accepted that. It pays for itself over the remaining ~13,600, which no longer buy reasoning
tokens.

One gap observed and not fixed: `inget_state.signatures` was empty after two runs, so `inget plan`
could not warn that this change invalidated everything. `recordSignatures` runs at the end of a
run, and neither run reached it — the first errored out of `runWorkers` and the second was
interrupted with a cancelled context. The warning exists precisely to make a mass invalidation
visible before it is paid for, and it is blind until a run finishes cleanly once.

### A list prompt needs a countable bound, and a failed view is expensive (2026-08-02)

With thinking disabled, seven of the eight `github/repo` views completed and every remaining
failure was `aliases`: 12 items done, 79 views, 79 vectors, 31 items failed on the eighth view.
"Output a flat list … at most 40 lines" was not a bound the model kept — four numbered input
categories and "include both technical and plain-language variations" produced a headed, sectioned
answer past 1024 tokens. The prompt now states the output shape as a hard requirement: at most 25
lines total, six words a line, no headings or numbering or commentary, stop after the last term.
Roughly 220 tokens against a 1024 budget.

The failure exposed a cost amplifier worth recording. `CheckpointItem` writes an item's guards in
one transaction, so an item that fails on its eighth view discards the seven generations that
succeeded, and the retry pays for all eight again — about 217 wasted generations across those 31
items. The atomicity is right: a stored view claims that generation, embedding and upsert all
happened. What is missing is the level-2 equivalent of the derivation cache — generated view text
keyed by view name, composed-input hash and enricher signature, so a failure elsewhere in the item
does not discard work already paid for. The fragment cache is exactly what made these retries cheap
on the derivation side; views have no such thing. Left as a follow-up because it is another state
table, not a config change.


## Query reranking, 2026-10-07

The local Lex replacement test needs an OpenAI-compatible reranker. This explicitly
extends the v1 query non-goal: optional CLI LLM ranking is allowed; an application-facing
search service and HTTP server remain out of scope. The design and roadmap record it.

`models.reranker` is an optional independent Generator role. `query.rerank` enables it
(default false), selects a 50-row candidate pool per datatype, bounds candidate previews
at 200 Unicode characters and names the disk prompt. These fields use ordinary strict
config decoding, environment overrides and secret references. A declared/enabled model
must be complete. Existing config still loads; query settings change neither source
artifact hashes nor indexing generator signatures. Model schema/validation moved into
separate files to keep source files below 250 lines.

`query --rerank` can override the setting, including `--rerank=false`. Retrieval uses
`max(limit, candidates)` rows per datatype, groups by datatype/item and keeps the best
matching view. Identity and best-view text form each preview; JSON escaping and the
shipped prompt's untrusted-data guards protect the query and candidates. The model must
return a complete permutation of supplied IDs as JSON. Scores remain vector similarity;
successful ordering adds `rank`. Failures/timeouts warn on stderr and return grouped
vector order, while command cancellation propagates. The operation has an overall
deadline across retries. Pools of fewer than two skip inference; larger pools are ranked
even when they fit within the output limit. The hard pool cap is 1000, checked before
query embedding.

The ignored local Vertex profile enables this role using the same global Gemini model,
temperature 0, 1024 output tokens, 30s timeout, zero thinking budget and JSON object mode.
The refreshed chat bridge permits that narrow request profile and the wrapper overrides
both generation roles. Lex's 50-row retrieval pool and vector-score preservation are
retained. Differences: JSON with strict permutation validation instead of permissive
CSV; best-view text instead of metadata-only previews; 200 Unicode characters instead
of 200 bytes; explicit seed 1; common client retries bounded by 30s; ranking every pool
of two or more (Lex CLI skipped pools fitting its output limit).

Verification: `make build test lint` passed with race detection and the pinned linter
reporting zero issues. Thirteen mock HTTP/ADC bridge tests passed. Live synthetic
three-document ranking passed against both the direct official Vertex OpenAI endpoint
and the refreshing bridge, putting the expected Terraform candidate first. The local
pgvector CLI returned one grouped repository; disabling ranking returned its top three
facets. Re-running ingestion processed/generated/embedded zero items. Four pre-existing
wrapcheck failures in the shared model request encoder were fixed by wrapping encoding
errors; request bodies and signatures are unchanged.

The live probe establishes transport/output correctness, not retrieval-quality parity.
The local index still holds one repo, so multi-item evaluation remains necessary. D14
continues to evaluate vector retrieval, not this query-time ranking stage; no schema,
state, artifact or vector migration was needed.

## Full-fetch consumer deletion safety, 2026-10-07

The fetch producer computes tombstones from the complete enumerated item set, while its
records omit unchanged items and failed downloads. The consumer instead inferred deletion
from record absence, so a subsequent full fetch could delete live indexed items. It now
uses only explicit manifest tombstones under full, untruncated, unrestricted scope;
deduplicates and sorts IDs; and refuses contradictory record/tombstone IDs before work.
The plan's deletion count reports only eligible explicit tombstones. D6 and the artifact
envelope now distinguish full enumeration from incremental record contents; no wire fields
or schema version changed.

Regression tests exercise the real fetch/commit/consumer boundary with one unchanged item,
one failed download, one changed item and one genuinely removed item, plus empty full runs,
deduplication, read-only planning, partial/truncated/restricted scope and contradictory
manifests. Targeted pipeline/artifact/fetch tests pass with race detection.


## GitHub empty-repository conflicts, 2026-10-07

The full UMG fetch encountered a repo whose metadata names `main` while its tree API
returns HTTP 409 with `Git Repository is empty.`. The connector previously counted this
as a failed item; it now recognizes only that status/message combination and returns
metadata/fingerprint, zero fragments and an explicit empty-repository warning. This
matches the existing no-default-branch behavior. Other conflicts, permission errors and
validation errors still fail and are not mistaken for empty content. No archive is
requested for the empty repository.

Table-driven HTTP regression tests exercise the real request/tree/fetch path, verifying
metadata preservation, zero fragments, the warning, no retry/archive, other 409s, and the
same message under other statuses. A read-only live connector probe against the affected
repo returned zero fragments with the expected warning; it wrote no artifacts or state.

Validation after both fetch-safety fixes: `make build test lint` passed, including the
full race-test suite, `go vet ./...`, and golangci-lint (zero issues). Both local binaries
were rebuilt. No full organization fetch or paid ingestion was started by this repair.


## Terminal progress, 2026-10-07

Implemented an optional command-owned terminal dashboard in `internal/progress`, wired through
shared CLI scaffolding for both binaries. The default `--progress auto` detects interactive
stderr, `plain` prints periodic snapshots, and `off` retains existing output. Existing local
wrapper arguments already pass through. Fetch reports and query data remain on stdout;
JSON/text diagnostic logs retain configured levels, destinations and secret redaction, and
stderr logs are coordinated with the live panel. The design records the explicit human-output
extension and promotion of two already-pinned pure-Go dependencies.

Context observers receive only identities/stages and counters. Fetch shows streaming discovery,
known totals after listing, worker activity, fetched/skipped/failed items, fragments and warning
counts, then commit. Ingestion reports per-artifact reconciliation, active fragment/view work,
actual generation and embedding counts, checkpointed successes and failures. Interrupted and
cancelled work retains its status. Resumed completion counts are actual attempts, without
forcing the ratio to 100%. No persistence, source/model request, artifact, hash, signature or
index migration changed. Existing processes must finish/restart to use the new display.

Regression tests verify real fake-source/artifact/state and pipeline boundaries, cache skips,
failed items, checkpoint failures, dry-run inertness, enumeration totals before worker drain,
concurrent display totals, stdout isolation, redaction, terminal-control escaping, narrow-width
clipping and refresh cleanup. `make build test lint` passed with race detection, vet and the
pinned linter reporting zero issues. Both binaries were rebuilt.

Release verification: `make release-check` passed. `make release-snapshot` passed for
Linux/macOS amd64/arm64 binaries and both local Docker architectures, publishing nothing.
The Docker buildx activity directory was redirected to workspace `BUILDX_CONFIG` so the
snapshot did not need writes under the user Docker configuration directory. A real
GitHub metadata-only dry run through the local wrapper verified automatic TTY and plain
progress against isolated SQLite state; JSON stdout stayed intact and models were unused.


## Usage-calibrated planning costs, 2026-10-08

The native planner now counts distinct uncached fragment cache keys across the
whole work set, includes rendered prompt wrappers and truncation notices in its
legacy allowance, and accepts `plan --estimate-profile PATH` for an additional
`estimate.expected` forecast. Versioned JSON profiles fit input tokens to complete
prompt characters and record mean completion usage per stage. Expected composition
uses cached summary rune lengths or measured mean lengths, including headings,
separators, scope filters and compose caps. The historical `upper_bound` label
remains compatible; it is a generation allowance, not a provider-bill guarantee.
The design and docs/cost-estimates.md distinguish these reporting contracts.

Profiles match datatype and every enricher signature, require finite nonnegative
values and samples, and reject stale/missing/extra stages. The strict CLI loader
rejects unknown fields, trailing JSON and files over 1 MiB. One profile applies to
one selected datatype. Profiles are reporting-only RunConfig inputs, outside
configuration hashes, generation settings and cache signatures. Planning excludes
embedding/retry charges and does not model resolved reference inputs. It still
covers the oldest named pending artifact run; ingestion drains the backlog.

A new state PeekDerivation reads output without touching last_hit_at. Prompt sizing
renders without calling generation. The existing per-run singleflight derivation
path also rechecks durable cache after entering its closure, avoiding a duplicate
paid call when an outer miss predates another worker's completed cache write.
This retains native cache patterns without cross-process locking or schema changes.

The ignored local wrapper automatically supplies .inget/local/estimate-profile.json
for plan, preserving explicit profile overrides. Its calibration uses 2,199 measured
fragment responses, 121 view responses from 17 attempted repositories plus two views
of the interrupted eighteenth, and 2,206 cached summary lengths. Four paid truncated
search_terms responses remain included; no sample was resumed and no prompts changed.
Larger repositories and retries remain sources of forecast uncertainty.

Validation: make build and the full make test race suite passed, including fresh
scratch PostgreSQL state/destination tests as well as SQLite. Pinned golangci-lint
reported zero issues after one redundant test selector was corrected; the affected
race test passed again. make release-check and make release-snapshot passed for all
four binary targets and both local Docker architectures, publishing nothing.

The exact user command, python3 .inget/local/local.py --refreshing plan github/repo,
completed against the existing full artifact in 204 seconds: 473,047 unique remaining
fragment derivations and 15,771 possible view generations. The allowance is
$1,732.8192295; measured expected generation cost is $289.9396307592. Both use current
configured pricing and exclude embeddings/retries. A native no-profile limited plan
omitted expected, and a stale profile failed before estimating. Counts and complete
row hashes of all nine operational state tables, including cache timestamps and the
consumption marker, were unchanged; the vector count stayed 98. No model requests or
new paid inference occurred. The local binary was rebuilt after an initial wrapper
invocation exposed the flag before integration had finished.
