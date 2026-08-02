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

> **Status.** Steps 1–9 and 11–14 of `docs/implementation-plan.md` are complete: both
> binaries build and release, configuration is layered and validated, artifacts round-trip
> through a content-addressed blob store, the invalidation cascade skips at four levels, the
> GitHub connector fetches, the pipeline enriches and embeds, references resolve across
> records, `inget eval` gates quality, and `reindex`, `state gc` and `query` cover
> operations. Step 10, the Monday connector, is not implemented — `github/repo` is the only
> datatype you can fetch today, and the `monday/item` prompts and configuration shipped here
> are ahead of the code.

## Install

Homebrew, on macOS:

```bash
brew install maxwell-cudlitz/tap/inget
inget version
```

Docker, for either binary:

```bash
docker run --rm ghcr.io/maxwell-cudlitz/inget:latest version
docker run --rm -v "$PWD/config.yaml:/etc/inget/config.yaml:ro" \
  ghcr.io/maxwell-cudlitz/inget:latest plan
```

Or take a `tar.gz` from [releases](https://github.com/maxwell-cudlitz/inget/releases): it
carries both binaries plus `config.yaml` and `prompts/`. From source, with Go 1.26+:

```bash
git clone https://github.com/maxwell-cudlitz/inget
cd inget
make build       # -> bin/inget, bin/inget-fetch
./bin/inget version
```

One thing to know before the first run: prompt templates are read from disk, not embedded.
`config.yaml` names them by relative path, so run from a directory that has `prompts/` beside
the config, or make those paths absolute. The published image carries `prompts/` and resolves
them from its own working directory; a Homebrew install carries the binaries only.

## Usage

The full command surface is specified in `docs/feature-design.md`. Today:

```bash
inget version
inget --help
inget migrate            # create the state schema and the vector tables
inget plan [datatype]    # what a run would do, and what it would cost
inget run  [datatype]    # enrich, embed, upsert
inget eval [datatype]    # score retrieval quality against the thresholds
inget query "<text>"     # search the index and print the hits
inget reindex [datatype] # re-embed stored views after an embedder change
inget state show|gc|unlock

inget-fetch              # enumerate a source and write an artifact run
```

A local stack, from nothing to a searchable index:

```bash
docker compose -f deploy/docker-compose.yaml up -d
export INGET_STATE_DSN='postgres://inget:inget@localhost:5432/inget?sslmode=disable'
export INGET_PGVECTOR_DSN="$INGET_STATE_DSN"
export INGET_GITHUB_TOKEN=ghp_...
inget migrate
inget-fetch --limit 20   # write a committed artifact run
inget plan               # what would change, and what it would cost
inget run                # enrich, embed, upsert
inget eval               # did the result come out searchable?
inget query "which repos handle terraform"
```

`inget plan` before `inget run` is the point of the design: you see the number of LLM calls
and the estimated cost before spending anything. `plan` reads only — it opens no
destination, takes no lock and calls no model — and reports the fragment derivations, view
generations, estimated tokens and cost a run would spend, plus any prompt or model change
that has invalidated cached work. Its estimates are upper bounds.

### Fetching

`inget-fetch` is one action, so it has no subcommand:

```bash
inget-fetch                                  # every configured datatype, full scope
inget-fetch --datatype github/repo           # one datatype
inget-fetch --only owner/repo,owner/other    # named items only
inget-fetch --event-file hook.json           # items named by a webhook payload
inget-fetch --since 24h                      # or an RFC 3339 timestamp, or a date
inget-fetch --limit 3 --dry-run              # enumerate and report; write nothing
```

It writes an immutable, atomically committed run to the blob store and reports what it did
as JSON on stdout. Three things it does that are worth knowing:

- **It skips what has not changed.** A repository whose last push matches what state
  already recorded costs one line of a listing response and no further request. The rest
  get one tree request and one archive each, never one request per file.
- **It uploads only new content.** Fragment content is content-addressed, so a repository
  where one file changed writes exactly one blob. Re-fetching an unchanged repository
  writes none.
- **It excludes detected secrets.** File content matching the gitleaks ruleset is left out
  of the artifact, and the exclusion is recorded in the manifest by path and rule, never by
  value. Turn it off per source with `limits.secret_scan: false` if you have a reason.

`--only`, `--event-file`, `--since` and `--limit` all mean the run did not see the whole
domain, so it is recorded as partial and issues no tombstones: an item it never looked at
is not an item that was deleted. Only a complete, untruncated run can conclude that
something was removed at the source.

### Quality

`inget eval` answers the question a vector pipeline otherwise leaves open: is what you
stored actually retrievable? It samples items that have stored view text, re-embeds that
text, and scores five metrics per datatype and per view against thresholds in config. A
breach exits non-zero, so it works as a CI gate for a new datatype or a prompt change.

```bash
inget eval                      # every configured datatype
inget eval github/repo          # one datatype
inget eval --embedder BAAI/bge-m3 --embedder-url http://localhost:8091/v1 --embedder-dims 1024
```

| Metric | What a low score means | Default floor |
|---|---|---|
| `self_retrieval` | an item's views do not look like each other's nearest neighbours | 0.80 |
| `distinctiveness` | different items embed to nearly the same place | 0.05 |
| `view_coverage` | some configured view produced no text | 0.90 |
| `view_distinctiveness` | the views of one item are restatements of each other | 0.05 |
| `metadata_top3` | a query built from an item's own metadata does not find it | 0.60 |

Reports are JSON on stdout with a per-view breakdown, so `inget eval | jq '.[].views'`
shows which prompt is the weak one rather than only that the datatype is weak. The floors
and the sample size are config, and every key is env-overridable:

```yaml
eval:
  sample_size: 20             # items with stored views sampled per datatype
  thresholds:
    self_retrieval: 0.80
    distinctiveness: 0.05
    view_coverage: 0.90
    view_distinctiveness: 0.05
    metadata_top3: 0.60
```

Two properties are worth knowing. It never calls the generator — it re-embeds text already
in state — so it needs no generator credentials and costs nothing but embedding time, and
running it twice on the same corpus gives the same numbers. And the retrieval pool is the
sample itself, so scores are comparable across runs and across embedders at a fixed
`eval.sample_size`, but not between different sample sizes. An empty or single-item corpus
is reported as unscorable and exits non-zero: it means `inget-fetch` and `inget run` have
not populated anything to measure.

### Searching

`inget query` is the verification path, not an application API: it answers "is what I stored
retrievable" without a psql session and a hand-written vector literal.

```bash
inget query "which repos handle terraform"                  # every datatype, merged by score
inget query "terraform modules" --datatype github/repo --view stack
inget query "python batch jobs" --limit 5 --json | jq '.[].item_id'
```

The query is embedded by the configured embedder, and every destination is checked against
that embedder before it is searched. Searching an index built by another model returns
rankings that look ordinary and mean nothing, so that check is a refusal rather than a
warning.

### Operations

Two things go wrong that a re-run cannot fix, and there is a command for each.

**The embedder changed.** A destination table is bound to one embedder, so every write is
refused until the binding moves — and moving it means rewriting the rows the old model left.
`inget reindex` does both. It re-embeds the view text already in state, so it needs no
generator credentials, spends no generator tokens, and does not regenerate anything.

```bash
inget reindex                        # every datatype: rebind, re-embed, prune
inget reindex github/repo            # one datatype
inget reindex --view stack           # one view; no prune, since the others were skipped
inget reindex --force                # rewrite even views already on this embedder
```

It claims per-item work rows, so an interrupted pass resumes where it stopped, and a second
pass over an already-current index reports zero vectors written. When it finishes a complete
pass it deletes the rows the new embedder never wrote — views you removed from config, items
deleted while the old model was bound. A datatype with no stored views prunes nothing and
says so: an empty state is a reason to run `inget run`, not a reason to empty the index.

A prompt change is *not* a reindex. `inget run` regenerates a view whose enricher signature
moved, and `inget plan` prices that before anything is spent.

**Storage grows.** `inget state gc` performs the three-phase collection: run directories past
`retention.runs` except the latest committed one, blobs no retained run and no live fragment
references, then the derivations of fragments missing for more than `retention.missing_runs`
runs.

```bash
inget state gc --dry-run   # report what would go
inget state gc             # collect
inget state show           # what each cascade level holds, plus recent runs
inget state unlock github/repo
```

`gc` holds every datatype's locks while it runs, so it exits rather than racing a fetch or a
run in progress — and for the same reason it takes no datatype argument: whether a blob is
referenced is a question about every datatype at once. `state show` writes nothing and works
even when the blob store is unreachable. `state unlock` only ever has something to remove on
the sqlite driver; a postgres advisory lock is released by the server when its holder's
connection drops, so a lock that is still held belongs to a live process and unlock says so.

### Deploying

`deploy/kubernetes/` has working examples: a ConfigMap and a placeholder Secret, hourly
CronJobs for fetch, run and a weekly gc, and a one-shot Job for reindex.

```bash
kubectl apply -f deploy/kubernetes/base.yaml      # namespace, config, credential placeholders
kubectl apply -f deploy/kubernetes/cronjobs.yaml  # fetch, run, gc
kubectl apply -f deploy/kubernetes/reindex-job.yaml
```

Fill the Secret from your own secret management rather than committing values into it. Four
settings in those manifests are load-bearing and the comments say why: `concurrencyPolicy:
Forbid`, `backoffLimit: 0`, `restartPolicy: Never`, and a `terminationGracePeriodSeconds`
long enough for SIGTERM to drain the items in flight. Neither binary listens on a socket, so
there is nothing to authenticate against and no ingress to police; every credential is a
Secret key named by configuration.

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

### References

A datatype can pull context out of another record, or out of a system inget does not
ingest, so that a work item is searchable by what the repository it links to actually
does:

```yaml
references:
  - name: linked_repo
    resolver: inget                # cross-record: read another datatype from state
    datatype: github/repo
    key_from: "column:repo_url"    # a fragment glob; "metadata:<field>" reads metadata
    key_regex: 'github\.com/([^/"?#]+/[^/"?#.]+)'   # narrow a URL to "owner/name"
    fields: [description, "view:role"]              # metadata field, or a generated view
    inject_as: fragment            # fragment | metadata
```

The `http` resolver is the same shape with `endpoint: "${TICKETS_BASE_URL}/api/{key}"` and
an optional `token_env`. `fields` is an allowlist in both cases: nothing else from the
response reaches a prompt.

What this costs is the part worth understanding. Changing a referenced record does not
re-fetch anything and does not re-embed the items that reference it — it marks them, and
the next run of *their* datatype re-resolves the reference. If the payload came back
identical, every view is skipped by the same guard that skips an unchanged item, so the
run spends no tokens. If it moved, only the views whose `depends_on` matches
`ref:<name>` regenerate. Two settings bound the blast radius:

```yaml
enrich:
  max_reference_depth: 2      # how far an invalidation travels; also what ends a cycle
  max_cascade_per_run: 5000   # invalidations one run will take; the rest wait for the next
```

Resolved keys land in the vector row's `related_keys` column, which is GIN-indexed, so
"what else points at this repository" is a query rather than a scan.

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
a config edit, and the error says so rather than letting the write through — `inget reindex`
is the one command allowed to move the binding, and it rewrites the rows the old model left
before deleting whatever the new one never wrote.

Steps 1–8 of the plan need no network access and no credentials, so most development runs
entirely offline against fakes. Two suites are the exception. The destination tests need
pgvector — HNSW recall and halfvec casting are the extension's behaviour and cannot be faked
— and skip unless `INGET_TEST_PG` points at a scratch database with it. The quality
harness's live case needs a reachable embedder and skips unless `INGET_TEST_EMBEDDER_URL`
names one; it is the only test that measures a real embedding space rather than a synthetic
one, and it needs no database and no credentials.

### Releasing

A release is a tag. `.github/workflows/release.yaml` runs the suite, then goreleaser builds
static binaries for linux and darwin on amd64 and arm64, publishes the archives and
`checksums.txt`, pushes one multi-arch image to `ghcr.io/maxwell-cudlitz/inget`, and updates
the Homebrew cask in `maxwell-cudlitz/homebrew-tap`.

```bash
make release-install    # install the pinned goreleaser (v2.17.1)
make release-check      # validate .goreleaser.yaml
make release-snapshot   # build every artifact into dist/, publish nothing
git tag -a v0.1.0 -m 'v0.1.0' && git push origin v0.1.0
```

Nothing is built inside the Dockerfile — goreleaser has already cross-compiled, so the image
copies the same binaries the archives contain. Two things the pipeline needs that the
repository cannot provide: a `HOMEBREW_TAP_TOKEN` secret with write access to the tap, since a
workflow token cannot reach another repository, and an existing tap repository for the cask to
land in. Prereleases (`v0.1.0-rc.1`) publish binaries and the image but deliberately do not
move `latest` or the cask.

## Security notes

- File content matching the gitleaks ruleset is excluded from artifacts, and the exclusion
  is recorded by path and rule rather than by value. Detection is not elimination: keep the
  blob store private and encrypted at rest, since a credential the ruleset does not
  recognize will still land in it. Artifacts are not safe to share publicly.
- Fetched content is untrusted input flowing into prompts. It is delimited and treated as
  data, output is validated and never interpreted as instructions, and no tool calls are
  driven by it. This is mitigation, not elimination — see the design's residual risks.
- Credentials are read only from environment variables named by config, and log output
  redacts secret-bearing attribute keys.
- Neither binary listens on a socket, so there is no server and no authentication surface.
  A webhook receiver would introduce one; `--event-file` deliberately reads a payload from
  disk instead.
- Released binaries are not code-signed or notarized, so the Homebrew cask clears the macOS
  quarantine attribute on them at install time. That bypasses Gatekeeper's verification, which
  is a real trade: verify `checksums.txt` against the release if you want assurance the archive
  is the one that was published.

## License

Apache-2.0. See `LICENSE`.
