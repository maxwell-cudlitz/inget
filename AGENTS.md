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
internal/delta/       reconciliation, hashing, signatures, glob scoping
internal/state/       StateStore interface, postgres, sqlite
internal/enrich/      pipeline stages, llm + passthrough enrichers, composer, refs
internal/model/       generator + embedder clients (OpenAI-compatible)
internal/destination/ registry, pgvector
internal/ratelimit/   adaptive limiter, header parsers
internal/pipeline/    orchestration, worker pool, checkpointing, signals
migrations/           goose SQL (state/, destination/)
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

## Hazards

- **Signature completeness (D2).** Any input that changes generated output must be a
  struct field feeding the signature hash. A forgotten contributor means silently stale
  data, which no test will notice unless the field is structural.
- **Glob scoping (D3).** View dependency globs decide what regenerates. Wrong matching
  means wrong invalidation, both directions.
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
- **Cost.** `inget plan` exists so no run spends money unexpectedly. Any change that can
  increase LLM calls must be visible there first.
