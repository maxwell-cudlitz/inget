# inget

Turn the things your team already writes — repositories, project boards — into a vector
index you can search in plain language, without re-paying for work that has not changed.

`inget` splits ingestion into two binaries:

- **`inget-fetch`** pulls items from a source, breaks them into fingerprinted fragments,
  and writes immutable artifacts to a blob store.
- **`inget`** reads those artifacts, works out what actually changed, enriches only that
  through an LLM, embeds it, and upserts the result into pgvector.

Because the two are separate, a fetch failure never wastes model spend, and you can
iterate on prompts against artifacts you already have — no re-fetching, no rate limits.

> **Status: under construction.** Steps 1–7 of 14 in `docs/implementation-plan.md` are
> complete: the binaries build, report their version and load a validated layered
> configuration; the artifact envelope — content-addressed blob store, JSONL record shards,
> manifests, the `_COMMIT` protocol — round-trips; the state store persists every level of
> the invalidation cascade on PostgreSQL or SQLite; the cascade engine and the model clients
> are implemented; and the pgvector destination stores, filters and searches view vectors.
> No command fetches or enriches yet — the connectors and the pipeline are steps 8–10.

## Install

Requires Go 1.26+.

```bash
git clone https://github.com/maxwellcudlitz/inget
cd inget
make build       # -> bin/inget, bin/inget-fetch
./bin/inget version
```

## Usage

The full command surface is specified in `docs/feature-design.md` and lands over the
remaining implementation steps. Today:

```bash
inget version
inget --help
inget migrate            # create the state schema and the vector tables
inget plan [datatype]    # what a run would do, and what it would cost
inget run  [datatype]    # enrich, embed, upsert

# Both accept --only ID[,ID...] and --limit N; run also accepts --dry-run.
```

`plan` reads only: it opens no destination, takes no lock, calls no model, and reports the
fragment derivations, view generations, estimated tokens and cost a run would spend, plus any
prompt or model change that has invalidated cached work. Its estimates are upper bounds. A run
restricted by `--only` or `--limit` is recorded as a partial run and issues no tombstones,
because an item it never looked at is not an item that was deleted.

`migrate` is idempotent and needs the DSN variables config names. A local stack, from
nothing:

```bash
docker compose -f deploy/docker-compose.yaml up -d
export INGET_STATE_DSN='postgres://inget:inget@localhost:5432/inget?sslmode=disable'
export INGET_PGVECTOR_DSN="$INGET_STATE_DSN"
inget migrate
```

The shape it is building toward:

```bash
inget-fetch --source github --limit 20   # write an artifact run
inget plan                               # what would change, and what it would cost
inget run                                # enrich, embed, upsert
inget query "which repos handle terraform"
```

`inget plan` before `inget run` is the point of the design: you see the number of LLM
calls and the estimated cost before spending anything.

## Configuration

Both binaries read one `config.yaml`. Point them elsewhere with `--config PATH` or
`INGET_CONFIG`; the `config.local.yaml` override is looked for beside whichever file you
name.

Configuration is layered, highest precedence first:

1. `INGET_*` environment variables, with `__` for nesting
   (`INGET_MODELS__GENERATOR__MODEL`)
2. `config.local.yaml` — your gitignored overrides
3. `config.yaml` — the committed base

Three things are worth knowing before you edit it:

- **Lists are not env-addressable.** Scalars are overridable by environment variable, but
  `sources`, `destinations` and `datatypes` are lists, and there is no sane syntax for
  addressing a list element. Override those through `config.local.yaml`, which replaces
  the whole list entry it names.
- **Unknown keys are errors.** A misspelled key fails the load instead of silently doing
  nothing, and validation reports every problem at once — required fields, unknown source
  or destination names, out-of-range values, and invalid dependency globs.
- **Durations accept days.** `30d`, `1d12h` and `120s` all work.

Secrets never appear in config files. Config names the environment variable that holds a
secret (`token_env: INGET_GITHUB_TOKEN`) and the process reads it at startup. A variable
that is unset is only an error when something actually needs it, so a GitHub-only run
does not require the Monday token.

Logging is controlled independently of the config file so you can raise verbosity
anywhere:

| Variable | Values | Default |
|---|---|---|
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |
| `LOG_FORMAT` | `json`, `text` | `json` |
| `LOG_DESTINATION` | `stderr`, `stdout` | `stderr` |
| `LOG_REDACT` | extra comma-separated key patterns to redact | — |

Logs are JSON on stderr; stdout carries program data only, so piping `--json` output
stays safe. Nothing is written to log files.

## State

Nothing durable is kept on local disk. The `state` block chooses where the cascade's
bookkeeping lives:

```yaml
state:
  driver: postgres          # postgres | sqlite
  dsn_env: INGET_STATE_DSN  # names the variable holding the connection string
  # path: ./.inget/state.db # sqlite only
```

`postgres` is the default and the only one to run in production: it holds the per-datatype
lock as a session advisory lock, so a killed pod releases it with no cleanup, and its
schema lives in `inget_state` alongside the vector tables. `sqlite` exists for offline
development — same behaviour, but its lock is a row, so a killed process leaves it behind.

Two properties follow from this block. A second run over the same datatype exits
immediately rather than duplicating work, which is what makes overlapping cron ticks safe.
And progress is checkpointed per item, so a run interrupted 80% of the way through resumes
at 80% instead of paying for the first 80% again.

## For developers

```bash
make build test lint    # the gate: all three must be green
make lint-install       # install the pinned golangci-lint (v2.12.2)
make cover              # per-package coverage
```

Read `AGENTS.md` for repository structure and conventions, `docs/feature-design.md` for
the binding decisions (D1–D15), and `docs/progress.md` for where implementation
currently stands.

Two ideas explain most of the codebase:

**The invalidation cascade.** Work is skipped at four levels — item fingerprint,
fragment fingerprint, per-fragment derivation, per-view generation — each keyed by
content hash plus a *signature* covering the model, prompt, limits and schema version.
Re-running with nothing changed performs zero LLM calls and zero upserts. The corollary
is that any input which affects output must feed the signature, or the cascade serves
stale data.

**Artifacts are immutable and content-addressed.** A fetch writes blobs and shards, then
an atomic `_COMMIT` marker; a run without that marker is invisible to readers. This makes
the blob store a replay log and an audit trail, and makes prompt iteration free. The store
is one URL — `file://`, `s3://` or `gs://` — so moving from a laptop to object storage is
a configuration change.

One consequence worth knowing before you point `inget` at a real index: a destination table
is bound to exactly one embedder, recorded in `inget_model_registry` and checked on every
write. Vectors from two models occupy different spaces, so mixing them produces rankings
that look ordinary and mean nothing. Changing `models.embedder` is therefore a reindex, not
a config edit, and the error says so rather than letting the write through.

Steps 1–8 of the plan need no network access and no credentials, so most development runs
entirely offline against fakes. The destination tests are the exception: HNSW recall and
halfvec casting are the extension's behaviour and cannot be faked, so they skip unless
`INGET_TEST_PG` points at a scratch database with pgvector.

## Security notes

- Artifacts can contain secrets that were committed to fetched repositories. Keep the
  blob store private and encrypted at rest; it is not safe to share publicly.
- Fetched content is untrusted input flowing into prompts. It is delimited and treated as
  data, output is validated and never interpreted as instructions, and no tool calls are
  driven by it. This is mitigation, not elimination — see the design's residual risks.
- Credentials are read only from environment variables named by config, and log output
  redacts secret-bearing attribute keys.

## License

Apache-2.0. See `LICENSE`.
