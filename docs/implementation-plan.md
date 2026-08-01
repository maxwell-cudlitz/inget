# Implementation Plan: inget

Derived from `feature-design.md`. Steps are sequential; each ends in a verifiable
state. Decision references (D1–D15) point at the design document.

## Prerequisites

- Go 1.26+ (installed: 1.26.5).
- Docker, for `deploy/docker-compose.yaml` (Postgres 17 + pgvector ≥ 0.8, TEI).
- Tokens, only for live source testing: `INGET_GITHUB_TOKEN`, `INGET_MONDAY_TOKEN`.
- No API keys needed for unit tests or CI — fakes cover the model layer (D14).

Every step must leave `make build test lint` green. Steps 1–8 require no network and no
credentials.

---

## Step 1: Project skeleton

**Goal.** A buildable, testable, lintable repository with the conventional layout.

**Actions.**
- `go mod init github.com/maxwellcudlitz/inget`.
- Create `cmd/inget/`, `cmd/inget-fetch/`, `internal/`, `migrations/`, `prompts/`,
  `deploy/`, `docs/`.
- `Makefile` with `build test lint run clean fmt tidy`; build to `bin/`.
- Apache-2.0 `LICENSE`; `.gitignore` covering `bin/`, `.inget/`, `config.local.yaml`.
- `internal/logging`: `log/slog` JSON handler, `LOG_LEVEL` / `LOG_FORMAT` /
  `LOG_DESTINATION`, secret redaction by key pattern.
- Cobra root commands for both binaries with `version` only.
- GitHub Actions CI: `go vet`, `golangci-lint`, `go test ./...` on push and PR.

**Acceptance.** `make build` produces `bin/inget` and `bin/inget-fetch`;
`./bin/inget version` prints a version; `make lint test` passes; CI green.

---

## Step 2: Configuration

**Goal.** One root config, correct precedence, secrets by indirection only.

**Actions.**
- `internal/config`: structs for the full schema in `feature-design.md`.
- Viper: `config.yaml` base, merge `config.local.yaml`, `INGET_` prefix,
  `.` → `__` key replacer, `AutomaticEnv`.
- Walk the decoded struct to pre-register every scalar key with `BindEnv`, since
  `AutomaticEnv` only sees known keys.
- Validation: required fields, referential integrity (each datatype's `source` and
  `destinations` exist), enum values, dimension bounds, glob syntax.
- `SecretRef` type resolving `*_env` names to values at load, never storing them in the
  serializable struct.
- `config_hash` and per-datatype `domain_hash` computation over the canonicalized
  effective config.
- Ship `config.yaml` with the documented defaults, all endpoints local.

**Acceptance.** Table-driven tests for precedence (base < local < env), nested env
override (`INGET_MODELS__GENERATOR__MODEL`), every validation failure, and hash
stability across key reordering. A test asserts list-element env override is *not*
supported, documenting the known viper limitation.

---

## Step 3: Artifact envelope and blob store

**Goal.** Round-trippable implementation of `docs/artifact-envelope.md`.

**Actions.**
- `internal/artifact`: `Manifest`, `Record`, `Fragment` types with JSON tags.
- `BlobStore` over `gocloud.dev/blob` for `file://`, `s3://`, `gs://`; two-level hex
  sharding; zstd via `klauspost/compress`; `HasBlob` existence check before write.
- Shard writer rolling at `shard_target_bytes` uncompressed, emitting per-shard
  compressed size and SHA-256.
- Manifest writer implementing the commit protocol, `_COMMIT` last.
- Reader: resolve `latest` to the newest ULID run with `_COMMIT`, reject unknown
  `schema_version`, verify shard SHA-256 before parsing, stream records.

**Acceptance.** Round-trip test writing 10k synthetic records across multiple shards
and reading them back identically. Tests prove: a run without `_COMMIT` is invisible;
a corrupted shard fails the read; a duplicate blob is written once; unknown
`schema_version` is rejected.

---

## Step 4: State store

**Goal.** Every guard level persisted, with locking and resumable checkpointing (D13).

**Actions.**
- `internal/state`: the `StateStore` interface from the design.
- `migrations/state/`: goose SQL for the `inget_state` schema.
- Postgres implementation on `pgx/v5` with `pgxpool`; advisory lock via
  `pg_try_advisory_lock(hashtext(...))`; work claiming with
  `SELECT … FOR UPDATE SKIP LOCKED`.
- SQLite implementation on `modernc.org/sqlite` for offline development and tests, with
  the lock as a single-row mutex table.
- A shared conformance test suite run against both implementations.

**Acceptance.** Conformance suite passes for both drivers. Tests prove: a second
`Lock` on the same datatype fails while the first is held; interrupted work is
reclaimable; `ReferencedBy` returns reverse edges; signature change is detectable.

---

## Step 5: Delta engine

**Goal.** The invalidation cascade in isolation, fully unit-tested (D1, D2, D3).

**Actions.**
- `internal/delta`: `Reconcile(cached, incoming) Delta` covering added, modified,
  unchanged, deleted.
- `Signature` builder: stable hash over model ID, prompt template bytes, limits, and
  schema version. A missing contributor is a compile-time struct field, not a
  convention, so completeness is enforced structurally.
- Cache key derivation for levels 1–3.
- Glob scoping: match fragment keys against a view's `depends_on`, then compute the
  scoped composed hash. `path.Match` semantics extended for `**`.
- Drift measurement: normalized Levenshtein distance with the `drift_threshold`
  comparison (D4).
- Deterministic composer: tier then path ordering, `max_chars` truncation.

**Acceptance.** This is the highest-value test surface in the project. Table-driven
tests cover every delta permutation; identical inputs in different orders produce
identical composed hashes; changing a prompt byte changes the signature; a view whose
globs match nothing is reported as skippable; drift below and above threshold behave
correctly; `**` and single-segment globs match as specified.

---

## Step 6: Model clients

**Goal.** OpenAI-compatible generator and embedder, plus the fakes CI depends on (D10).

**Actions.**
- `internal/model`: `Generator` and `Embedder` interfaces with `Signature()`.
- One `openai` driver for both roles: `/v1/chat/completions` and `/v1/embeddings`,
  configurable `base_url`, `model`, key from environment, `temperature: 0`, `seed`.
- `hashicorp/go-retryablehttp` transport; `cenkalti/backoff` for 429 and 5xx with full
  jitter; usage extraction for cost accounting; bounded concurrency via
  `errgroup.SetLimit`.
- Batching to `batch_size`; MRL truncation to `truncate_dims` with re-normalization.
- Fakes: `Generator` returning a deterministic hash-derived string, `Embedder`
  returning a deterministic hash-derived unit vector of the configured width.

**Acceptance.** `httptest` server tests cover happy path, 429 with `Retry-After`, 5xx
retry then success, malformed response, and truncation-plus-renormalization producing
unit-length vectors. Fakes are deterministic across runs.

---

## Step 7: pgvector destination

**Goal.** A working destination with model binding enforced (D7, D8, D9).

**Actions.**
- `internal/destination`: registry plus the `Destination` interface.
- `migrations/destination/`: goose SQL for `inget_vectors`, `inget_model_registry`, and
  indexes. HNSW created last.
- pgvector driver: `AssertModel` against the registry; batched
  `INSERT … ON CONFLICT (id) DO UPDATE`; `pgx.CopyFrom` into a staging table then
  `INSERT … SELECT … ON CONFLICT` for cold loads; `DeleteItem` by
  `(datatype, item_id)`; `Search` setting `hnsw.iterative_scan = relaxed_order` and
  `hnsw.ef_search`.
- `deploy/docker-compose.yaml`: `pgvector/pgvector:pg17` plus TEI serving
  Qwen3-Embedding-0.6B.
- `inget migrate` wiring.

**Acceptance.** Integration test gated on `INGET_TEST_PG`: migrate, upsert 10k fake
vectors, HNSW search returns the planted nearest neighbour, re-upsert is idempotent,
`DeleteItem` removes every view for an item, and a mismatched model or dimension is
rejected with actionable text. Verify `CopyFrom` works with `halfvec` — flagged as
unconfirmed in research.

---

## Step 8: Enrichment pipeline

**Goal.** The full cascade end to end with fakes, no network (D1–D4, D11).

**Actions.**
- `internal/enrich`: stage sequence from the design, steps 1–10.
- `llm` enricher: prompt templates from `prompts/`, per-fragment derivation with cache
  lookup, per-view scoped composition and generation, output validation.
- `passthrough` enricher: fragment or item content direct to views, no LLM.
- `internal/pipeline`: orchestration, `errgroup` worker pool, per-item transactional
  checkpointing, SIGTERM handling that stops new claims, drains in-flight work, and
  marks the run `interrupted`.
- `inget run`, `inget plan`, `inget state show|gc|unlock`.
- Prompt templates for all eight `github/repo` views and both `monday/item` views,
  written from scratch rather than adapted from any existing prompt text.

**Acceptance.** The cascade test is the gate: seed state for a 100-fragment item with
8 views, change one fragment, assert exactly one derivation, only the views whose globs
match that fragment regenerate, and only those embed and upsert. A second identical run
performs zero LLM calls and zero upserts. SIGTERM mid-run then re-run completes without
duplicating work. `inget plan` output matches what `inget run` then does.

---

## Step 9: GitHub connector

**Goal.** `inget-fetch` produces valid artifacts for `github/repo`.

**Actions.**
- `internal/source/github`: enumeration over `orgs`, explicit `repos`, and `topics`
  with filters; `pushed_at` as level-0 fingerprint.
- Recursive tree API for per-path blob SHAs; tarball streamed and extracted in memory;
  content retained only for fragments whose blob is absent from the store.
- Noise filtering and tier classification; sub-file fragments for
  oversized files.
- `internal/ratelimit`: `x-ratelimit-*` handling, secondary-limit backoff, ETag
  conditional requests.
- `inget-fetch` command with `--source --datatype --only --since --scope --event-file
  --limit --dry-run`.

**Acceptance.** Unit tests with recorded HTTP fixtures cover pagination, filters,
tarball extraction, tier classification, and sub-file splitting. A live smoke test
(`--limit 3`) against a real org produces a committed run that `inget run --dry-run`
reads without error. Re-running fetch with no upstream changes writes zero new blobs.

---

## Step 10: Monday connector

**Goal.** `inget-fetch` produces valid artifacts for `monday/item` under complexity
metering.

**Actions.**
- `internal/source/monday`: GraphQL client, `API-Version: 2026-07` pinned,
  `Authorization` without `Bearer`.
- Board enumeration by offset; `items_page(limit: 500)` then root-level
  `next_items_page(cursor:)`; checkpoint board progress so 60-minute cursor expiry
  cannot fail a long run.
- Fragments per column value (fingerprint over raw `value` JSON) and per update;
  board column schema fetched once per board and cached.
- Adaptive complexity limiter: parse IETF `RateLimit` headers, request the `complexity`
  block, `SetLimit` reactively, sleep `t` when remaining drops below
  `complexity_reserve`, distinct handling for `COMPLEXITY_BUDGET_EXHAUSTED`,
  `maxConcurrencyExceeded`, `DAILY_LIMIT_EXCEEDED`, `IP_RATE_LIMIT_EXCEEDED`, all
  honoring `Retry-After`.
- `--event-file` mapping a Monday webhook payload to item IDs via `event.pulseId`.

**Acceptance.** Unit tests over the header parser and limiter state machine, including
budget exhaustion and recovery. Fixture tests for cursor pagination, cursor expiry
recovery, and typed column value extraction (`text` versus `value`). Live smoke test
against a real board produces a committed run consumable by `inget run`.

---

## Step 11: Reference resolution

**Goal.** Cross-record references with reverse-dependency invalidation (D12).

**Actions.**
- `internal/enrich/refs`: `Resolver` interface and registry.
- `inget` resolver reading from state and destinations for cross-record lookups
  (`fields: [description, "view:role"]`).
- `http` resolver with URL templating, env-supplied auth, and a response field
  allowlist.
- `key_from` extraction against item metadata and fragment keys.
- Reverse edge persistence; invalidation of dependent views when a referent's
  fingerprint changes; `max_reference_depth`, cycle detection, and a per-run cascade cap
  that logs and defers the remainder.
- `metadata_fields.related_keys` population.

**Acceptance.** Tests prove: a changed referent invalidates exactly the dependent views
of referencing items; a cycle terminates at the depth bound; a cascade beyond the cap
is deferred and logged; `related_keys` reaches vector metadata and is GIN-queryable.

---

## Step 12: Quality harness

**Goal.** `inget eval` as the acceptance gate for datatypes and prompts (D14).

**Actions.**
- Sample N items per datatype, generate views, embed, score the five metrics.
- Report per datatype and per view, not aggregated; exit non-zero on threshold breach.
- `--embedder` override for A/B comparison on the real corpus.
- Thresholds in config with the documented defaults.

**Acceptance.** Harness runs against fakes in CI (asserting mechanics, not quality) and
against live models when credentials are present. A deliberately degraded prompt is
detected as a threshold breach.

---

## Step 13: Reindex and operations

**Goal.** Recovery paths for model and prompt changes.

**Actions.**
- `inget reindex` scoped by datatype, view, or destination: rebind the model registry,
  regenerate or re-embed as required, report progress, resumable via `work`.
- `inget state gc` implementing the three-phase collection from the envelope spec.
- `inget query` for verification, with `--json`.
- `deploy/`: Kubernetes CronJob examples with `concurrencyPolicy: Forbid`,
  `backoffLimit: 0`, `restartPolicy: Never`, and a raised
  `terminationGracePeriodSeconds`.

**Acceptance.** Changing the embedder in config then running `reindex` produces a
consistent index with the new model recorded on every row and the registry rebound.
`gc` reclaims orphan blobs without touching live ones. Interrupting `reindex` and
resuming completes correctly.

---

## Step 14: Release and documentation

**Goal.** Installable artifacts and maintained documentation.

**Actions.**
- goreleaser: cross-compiled static binaries (all dependencies are pure Go), Docker
  image, Homebrew tap formula.
- GitHub Actions release workflow on tag: test, build, publish image, update tap.
- `README.md`: user-facing quick start first, then developer insights.
- `AGENTS.md`: repository structure and functionality for agent maintenance, including
  how to add a source, a datatype, an enricher, and a destination.
- Block comments at the top of every file; doc comments on exported symbols.

**Acceptance.** A tagged release publishes binaries, image, and formula.
`brew install` then `inget version` works on a clean machine. Both docs describe the
shipped behavior.

---

## Verification

End-to-end acceptance for the complete feature:

1. `docker compose up -d` brings up Postgres + pgvector and TEI.
2. `inget migrate` creates state and destination schemas.
3. `inget-fetch --source github --limit 20` writes a committed `full` run.
4. `inget plan` reports expected work; `inget run` executes it; counts match.
5. `inget query "which repos handle terraform"` returns plausible hits with scores.
6. `inget run` again performs zero LLM calls and zero upserts — the cascade holds.
7. Push a one-line change to one file in one repo, re-fetch, re-run: exactly one
   fragment derivation, only the views depending on that path regenerate, only those
   re-embed.
8. `inget-fetch --only <one-item> --scope partial` then `inget run`: no tombstones
   issued, no other items touched.
9. `inget eval` passes thresholds for every configured datatype.
10. Edit one view's prompt, `inget plan`: a signature change is surfaced with an
    estimated cost before any spend.
11. `SIGTERM` mid-run, then re-run: resumes and completes without duplicate work.
12. Change the embedder model, run `inget run`: rejected with reindex guidance. Run
    `inget reindex`: index rebuilt consistently.
