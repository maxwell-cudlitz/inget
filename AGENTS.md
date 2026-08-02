# AGENTS.md

Maintenance guide for agents working in this repository. Read this, then
`docs/progress.md`, before making changes.

## What inget is

Two binaries that turn source data into searchable vectors, split so that a fetch
failure can never waste LLM spend:

- `inget-fetch` enumerates items from a source (GitHub, Monday), splits them into
  fingerprinted fragments, and writes immutable content-addressed artifacts to a blob
  store, finishing with an atomic `_COMMIT` marker.
- `inget` reads committed artifact runs, runs a staged invalidation cascade to decide
  what actually changed, enriches only that, embeds it, and upserts view vectors into a
  destination (pgvector).

The wire contract between them is `docs/artifact-envelope.md`. Neither binary listens on
a socket; there is no server and no authentication surface.

## Authoritative documents

| Document | Role |
|---|---|
| `docs/feature-design.md` | The contract. Decisions D1–D15 are binding; do not re-derive them. |
| `docs/artifact-envelope.md` | Wire format between the binaries, `schema_version 1`. |
| `docs/implementation-plan.md` | Ordered steps 1–14, each with actions and acceptance criteria. |
| `docs/progress.md` | Current step, deviations, and which design sections each step needs. |

If an implementation detail contradicts the design, the design wins — or the design gets
amended explicitly and the amendment is recorded in `docs/progress.md`.

## Layout

```
cmd/inget/            enrichment entrypoint
cmd/inget-fetch/      fetch entrypoint
internal/cli/         shared cobra scaffolding: root command, version, --config, error exit
internal/logging/     slog setup, secret redaction         [implemented]
internal/config/      loading, precedence, validation, secret indirection, hashing [implemented]
internal/artifact/    envelope schema, manifest, shards, blob store [implemented]
internal/source/      connector registry (github/, monday/)
internal/delta/       reconciliation, hashing, signatures, glob scoping [implemented]
internal/state/       Store interface, postgres, sqlite         [implemented]
internal/enrich/      pipeline stages, llm + passthrough enrichers, composer, refs
internal/model/       generator + embedder clients (OpenAI-compatible)   [implemented]
internal/destination/ registry, pgvector
internal/ratelimit/   adaptive limiter, header parsers
internal/pipeline/    orchestration, worker pool, checkpointing, signals
migrations/           goose SQL (state/, destination/), embedded  [state implemented]
prompts/              text/template prompt files per datatype
deploy/               docker-compose, k8s CronJob examples
```

`internal/cli` is an addition to the layout in the design document: both binaries need
identical root-command scaffolding, so it lives in one place rather than being duplicated
across two mains.

## Conventions

- Source files stay under 250 lines; packages stay single-purpose. Every file opens with
  a block comment; exported symbols get doc comments.
- Errors are values. Wrap with `fmt.Errorf("doing x: %w", err)` at package boundaries.
  Never swallow. Log an error once, at the outermost handler — `cli.Execute`.
- Logs are JSON on stderr via `log/slog`. Stdout is reserved for program data
  (`--json` output, query results, `version`). Nothing writes log files.
- Config is layered: `config.yaml` < `config.local.yaml` < `INGET_*` env, with `__`
  expressing nesting. Secrets appear only as the *names* of environment variables
  (`token_env`, `api_key_env`, `dsn_env`), never as values. Read the whole file with
  `config.Load`, the log block alone with `config.LoadLog`, and a secret with
  `cfg.Secret(ref)`.
- Config keys come from `mapstructure` tags. Adding a scalar field makes it
  env-overridable automatically — `config.EnvKeys` walks the schema — so no registration
  list needs updating. Decoding is strict: unknown keys fail the load.
- State SQL is written once for both drivers, with `?` placeholders and only the
  intersection of PostgreSQL and SQLite syntax. Everything that differs is a field on
  `state.dialect`. Statements go through the helpers in `internal/state/store.go`, never
  straight to `*sql.DB`.
- Tests are stdlib `testing`, table-driven, asserting behavior over implementation.
- Dependencies are pinned in `go.mod` and must be pure Go so binaries cross-compile
  statically. Prefer stdlib; see D15 for the approved library set.

## Tasks

```bash
make build        # -> bin/inget, bin/inget-fetch, version stamped via ldflags
make test         # go test -race ./...
make lint         # go vet + golangci-lint (skips with a warning if not installed)
make lint-install # install the pinned golangci-lint
make fmt tidy clean
```

`make build test lint` must be green before any step is considered done.

`make lint` degrades quietly: without golangci-lint on `PATH` it runs `go vet` alone and
prints a warning to stderr. `go vet` catches none of the linters CI enforces — unchecked
errors, unused fields, unwrapped boundary errors — so run `make lint-install` once and
confirm the output says how many issues were found, not that it skipped.

The state conformance suite runs against sqlite alone unless a scratch PostgreSQL is
pointed at. Both drivers, from a clean container:

```bash
docker run -d --rm --name inget-pg -e POSTGRES_PASSWORD=inget -e POSTGRES_DB=inget \
  -p 55433:5432 postgres:17-alpine
INGET_TEST_PG='postgres://postgres:inget@127.0.0.1:55433/inget?sslmode=disable' \
  go test -race ./internal/state/...
```

## Extension points

Each of these is a registry plus an interface; adding an implementation should not
require touching the pipeline.

- **A source**: implement the connector interface in `internal/source/<name>/`, register
  it, add its config block and datatype definitions.
- **A datatype**: define its fragmenter, tiers and views in config, add prompt templates
  under `prompts/<source>/<datatype>/`, then gate it with `inget eval`.
- **An enricher**: implement the enricher interface in `internal/enrich/`; it must
  contribute every input to its signature (D2) or the cascade will serve stale output.
- **A destination**: implement the destination interface in `internal/destination/`,
  including `AssertModel` so an embedder change cannot silently corrupt an index (D7).
- **A state driver**: add a `dialect` entry in `internal/state/dialect.go` and a migration
  directory under `migrations/state/`. If the new dialect needs a fourth difference beyond
  placeholders, row locking and lock strategy, add the field rather than a second
  implementation of the statements — the conformance suite is what keeps the drivers
  honest, and it only works while there is one implementation to test.

## Hazards

- **Signature completeness (D2).** Any input that changes generated output must be a
  struct field feeding the signature hash. A forgotten contributor means silently stale
  data. `BuildSignature` lists its fields by hand, so
  `TestBuildSignatureCoversEveryField` walks `SignatureInput` by reflection and fails when
  mutating a field leaves the digest unchanged. Add a field to that struct and the test
  tells you to encode it; extend `mutateField` if the new field has a kind it cannot
  change.
- **Glob scoping (D3).** View dependency globs decide what regenerates. Wrong matching
  means wrong invalidation, both directions. `ViewSkippable` takes the *changed* set and
  tests it directly; that set must include deleted keys. Filtering it against the current
  fragment set instead — the obvious-looking refactor — makes a view whose only changed
  dependency was deleted look skippable, and it then stays stale forever.
- **Drift is normalised by runes (`internal/delta/drift.go`).**
  `levenshtein.ComputeDistance` measures in runes, so dividing by `len()` in bytes
  understates drift on any non-ASCII string and suppresses the re-embed that
  `drift_threshold` exists to trigger. The tests cover accented, CJK, emoji and
  mixed-script text for exactly this reason.
- **The composed document format is part of the cache key
  (`internal/delta/compose.go`).** Entries are joined `\n---\n` and headed `## <key>`.
  Changing the separator, the header or the ordering changes every level-2 hash and
  regenerates every view in every datatype — a full re-spend. `Compose` also reports
  truncation; passing that up is required, not optional.
- **Model usage details are nested (`internal/model/wire.go`).** `encoding/json` matches
  tag names literally and has no path syntax, so `json:"prompt_tokens_details.cached_tokens"`
  compiles, matches nothing, and reports zero cache hits forever. Nested response objects
  need nested structs.
- **Embeddings are placed by reported index (`internal/model/embedder.go`).** The OpenAI
  schema does not promise ordered `data`. Mapping by arrival order stores every vector
  against the wrong view with no error at any layer.
- **MRL truncation is client-side (`internal/model/embedder.go`).** Do not add a
  `dimensions` field to the embedding request: TEI and most self-hosted OpenAI-compatible
  servers do not implement it, and truncating plus re-normalizing locally is what a server
  that does implement it would do anyway.
- **Secret vocabulary.** `internal/logging` deliberately does not redact `input_tokens`,
  `cache_key`, `related_keys` or `signature`. Do not widen the patterns to generic
  `token`/`key` matches; the redaction tests assert these negatives.
- **Config hashing (`internal/config/hash.go`).** Hashes are computed from the decoded
  struct, never from viper's raw settings map: an environment override arrives as a
  string, so raw hashing would make an override that restates a file value look like a
  config change and re-fetch everything. `Config` also implements `String` on purpose —
  `fmt` reads unexported fields with `%+v`, which would otherwise print the resolved
  secret snapshot.
- **Commit ordering (`internal/artifact/writer.go`).** Blobs, then shards, then
  `manifest.json`, then `_COMMIT`. Writing the marker any earlier makes a crashed fetch
  look like a complete run, and a consumer would then issue tombstones for items the
  producer never reached. `Commit` validates the manifest before writing it, so a producer
  bug fails at the boundary rather than in every future read.
- **fileblob URL parameters (`internal/artifact/store.go`).** `file://` stores get
  `metadata=skip`, `create_dir=true` and `no_tmp_dir=true`. Dropping `metadata=skip` makes
  the driver write a `.attrs` sidecar per object, which doubles the object count and puts
  non-run entries into the listings `ListRuns` walks. Dropping `no_tmp_dir` reintroduces
  cross-device rename failures when `TMPDIR` is on another mount.
- **Run identifiers are canonical uppercase ULIDs.** `latest` resolves by lexical
  comparison, so `ParseRunID` rejects the lowercase encoding that Crockford base32 would
  otherwise accept: a lowercase directory name sorts after every canonical one and would
  masquerade as the newest run.
- **Backend registration weight (`internal/artifact/drivers.go`).** Linking all three
  gocloud drivers costs about 27 MB of stripped binary (12 MB with `fileblob` alone,
  21 MB adding `s3blob`, 39 MB with `gcsblob` as well). That is the price of one code path
  for three schemes; if a release needs to be small, drop an import there and accept that
  the matching URL scheme fails at open time.
- **State store weight (`internal/state/drivers.go`).** Linking `internal/state` costs
  about 12 MB of stripped binary, most of it modernc.org/sqlite. Both drivers are always
  linked, because the driver is a configuration value.
- **`RETURNING` order is unspecified.** `ClaimWork` sorts its result in Go. PostgreSQL
  returns updated rows in whatever order the UPDATE touched them, not the subquery's
  `ORDER BY`, and SQLite happens to agree with the subquery — so a test that trusted the
  SQL alone passed on sqlite and failed on postgres.
- **The advisory lock lives on a pinned connection (`internal/state/lock.go`).** A
  PostgreSQL advisory lock belongs to a session, so `Lock` holds an `*sql.Conn` for the
  lock's lifetime and hands it back only on release. Running the unlock through the pool
  would unlock from whichever connection answered, which is not necessarily the one
  holding it. Release detaches from the caller's context on purpose: a run releasing its
  lock during SIGTERM handling has an already-cancelled context.
- **State conformance runs against sqlite by default.** `INGET_TEST_PG` adds the postgres
  half of the suite, and that run DROPs the `inget_state` schema of the database it names
  before every case. A behaviour asserted only on sqlite is not asserted.
- **Cost.** `inget plan` exists so no run spends money unexpectedly. Any change that can
  increase LLM calls must be visible there first.
