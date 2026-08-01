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

> **Status: under construction.** Step 1 of 14 in `docs/implementation-plan.md` is
> complete: the binaries build and report their version. Fetching and enrichment are not
> implemented yet.

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

Configuration is layered, highest precedence first:

1. `INGET_*` environment variables, with `__` for nesting
   (`INGET_MODELS__GENERATOR__MODEL`)
2. `config.local.yaml` — your gitignored overrides
3. `config.yaml` — the committed base

Secrets never appear in config files. Config names the environment variable that holds a
secret (`token_env: INGET_GITHUB_TOKEN`) and the process reads it at startup.

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
the blob store a replay log and an audit trail, and makes prompt iteration free.

Steps 1–8 of the plan need no network access and no credentials, so most development runs
entirely offline against fakes.

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
