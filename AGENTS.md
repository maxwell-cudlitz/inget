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

`docs/local-walkthrough.md` is a worked example rather than a contract: the compose stack, 50
public repositories chosen with the GitHub search API, and the curl-plus-psql read path. It
asserts observable behaviour — JSON field names, key layouts, SQL — so a change to any of those
should update it.

`docs/self-submission.md` is the other worked example: `inget-fetch` running inside a repository's
own CI, submitting to shared artifact storage for a consumer elsewhere to drain. Same status —
observable behaviour, not a contract.

## Layout

```
cmd/inget/            enrichment entrypoint: migrate, run, plan, eval, reindex, query, state [implemented]
cmd/inget-fetch/      fetch entrypoint; fetching is the root command's own action [implemented]
internal/cli/         shared cobra scaffolding: root command, version, --config, error exit
internal/logging/     slog setup, secret redaction         [implemented]
internal/config/      loading, precedence, validation, secret indirection, hashing [implemented]
internal/artifact/    envelope schema, manifest, shards, blob store, collection primitives [implemented]
internal/source/      connector interface + registry (github/ [implemented], monday/)
internal/fetch/       fetch orchestration: level-0 skip, worker pool, tombstones, commit [implemented]
internal/delta/       reconciliation, hashing, signatures, glob scoping [implemented]
internal/state/       Store interface, postgres, sqlite         [implemented]
internal/enrich/      enricher interface, llm + passthrough + fragment enrichers, prompts [implemented]
internal/enrich/refs/ reference resolvers (inget, http), key extraction, invalidation cascade [implemented]
internal/eval/        quality harness: sampling, five metrics, per-view report [implemented]
internal/gc/          three-phase collection: runs, blobs, derivations [implemented]
internal/reindex/     re-embed stored view text under a new embedder, resumably [implemented]
internal/model/       generator + embedder clients (OpenAI-compatible)   [implemented]
internal/destination/ registry, pgvector                    [implemented]
internal/ratelimit/   Limiter interface, header parsers, github limiter [implemented]
internal/pipeline/    orchestration, artifact backlog drain, worker pool, checkpointing, signals [implemented]
migrations/           goose SQL (state/{postgres,sqlite}/, destination/pgvector/), embedded
prompts/              text/template prompt files per datatype
deploy/               docker-compose [implemented], kubernetes/ CronJobs + reindex Job [implemented]
.goreleaser.yaml      release pipeline: archives, one multi-arch image, one Homebrew cask
Dockerfile            runtime image; copies goreleaser's binaries, builds nothing
.github/workflows/    ci.yaml on push and PR, release.yaml on tags
```

`internal/cli` is an addition to the layout in the design document: both binaries need
identical root-command scaffolding, so it lives in one place rather than being duplicated
across two mains. `internal/fetch` is likewise an addition, and mirrors `internal/pipeline`:
`inget-fetch` needs orchestration that is neither the connector's business nor the
entrypoint's. A subcommand that pulls heavy dependencies belongs to the entrypoint that
wants it instead: `migrate` lives in `cmd/inget/` because linking the destination drivers
into the shared package would put that weight into `inget-fetch`, which needs none of them.

Stripped binary sizes, both dominated by the gocloud.dev backends rather than by anything
either binary does: `inget` 51 MB, `inget-fetch` 55 MB. The github connector including the
gitleaks ruleset accounts for about 4 MB of the latter.

Destination migrations are keyed by driver, not by SQL dialect, because a destination need
not be a SQL database at all.

## Conventions

- Source files stay under 250 lines; packages stay single-purpose. Every file opens with
  a block comment; exported symbols get doc comments.
- Errors are values. Wrap with `fmt.Errorf("doing x: %w", err)` at package boundaries.
  Never swallow. Log an error once, at the outermost handler — `cli.Execute`.
- Logs are JSON on stderr via `log/slog`. Stdout is reserved for program data
  (`--json` output, query results, `version`). Optional `--progress` human output is
  coordinated on stderr when enabled; redirected auto mode keeps existing logs. Nothing
  writes log files.
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
- Prompts are files under `prompts/<source>/<datatype>/`, never string literals in Go. Each
  one opens with a `{{- /* … */ -}}` maintainer comment (it renders to nothing but is part of
  the signature), wraps ingested content in `<UNTRUSTED_DATA>` delimiters, and tells the model
  the block is data with a worked example. Prompt bytes feed the enricher signature, so edits
  are surfaced; rebuilding unchanged items requires the explicit
  `--rebuild-on-signature-change` flag on `inget plan` and `inget run`.
- Dependencies are pinned in `go.mod` and must be pure Go so binaries cross-compile
  statically. Prefer stdlib; see D15 for the approved library set.

## Tasks

```bash
make build        # -> bin/inget, bin/inget-fetch, version stamped via ldflags
make test         # go test -race ./...
make lint         # go vet + golangci-lint (skips with a warning if not installed)
make lint-install # install the pinned golangci-lint
make fmt tidy clean

make release-check      # validate .goreleaser.yaml
make release-snapshot   # build every release artifact into dist/, publish nothing
make release-install    # install the pinned goreleaser
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

The destination suite reads the same variable but needs pgvector, not plain PostgreSQL, and
skips entirely without it. Nothing about halfvec casting, HNSW recall under a filter, or
COPY into a staging table can be faked, so a green run with the variable unset has tested
none of it:

```bash
docker run -d --rm --name inget-pgvector -e POSTGRES_USER=inget -e POSTGRES_PASSWORD=inget \
  -e POSTGRES_DB=inget -p 55433:5432 pgvector/pgvector:pg17
INGET_TEST_PG='postgres://inget:inget@127.0.0.1:55433/inget?sslmode=disable' \
  go test -race ./internal/destination/...
```

The quality harness has a live case of its own, gated on a reachable embedder. It needs no
database and no credentials — state is sqlite in a temp directory — and it is the only test
that measures a real embedding space rather than a synthetic one:

```bash
text-embeddings-router --model-id Qwen/Qwen3-Embedding-0.6B --port 8090 --hostname 127.0.0.1
INGET_TEST_EMBEDDER_URL=http://127.0.0.1:8090/v1 go test -race ./internal/eval/ -run TestLive -v
```

`inget reindex` has no automated live case, because the thing worth verifying is a real
embedder writing into a real pgvector. Do it by hand when changing anything in
`internal/reindex` or the two new destination methods: seed `inget_state.items` and
`inget_state.views` with a few rows of text, `inget migrate`, then

```bash
inget reindex github/repo                                     # fills the index, rebinds
inget reindex github/repo                                     # must report 0 vectors written
INGET_MODELS__EMBEDDER__MODEL=other inget run github/repo     # must refuse, naming reindex
INGET_MODELS__EMBEDDER__MODEL=other inget reindex github/repo # rebinds and rewrites everything
```

and check that every row of the table carries the new model and that `inget_model_registry`
agrees. Deleting one `views` row before the last pass is how the prune gets exercised: the
orphan vector should be the one row it deletes.

## Releasing

A release is a tag: `git tag -a vX.Y.Z -m 'vX.Y.Z' && git push origin vX.Y.Z`.
`.github/workflows/release.yaml` runs vet and the suite, then goreleaser publishes

- one `tar.gz` per platform (linux and darwin, amd64 and arm64) carrying both binaries,
  `config.yaml` and `prompts/`, plus `checksums.txt`;
- one multi-arch image, `ghcr.io/maxwell-cudlitz/inget`, tagged `vX.Y.Z`, `vX.Y` and — only
  for a non-prerelease — `latest`;
- the Homebrew cask in `maxwell-cudlitz/homebrew-tap`.

Validate a change to the pipeline with `make release-check`, and prove it with
`make release-snapshot`, which needs docker with buildx and writes everything to `dist/`
without pushing. Snapshot mode cannot build a manifest without pushing, so it produces
per-architecture tags (`…-amd64`, `…-arm64`) instead of one multi-arch tag; that difference is
goreleaser's, not this configuration's.

Three preconditions, none of which the repository can satisfy on its own:

- **`HOMEBREW_TAP_TOKEN`** must be a secret holding a PAT with `contents:write` on the tap. A
  workflow's `GITHUB_TOKEN` is scoped to this repository and cannot push to another. If it is
  missing the release fails at the cask step, *after* the binaries and image are published —
  set the secret and re-run the job rather than re-tagging.
- **The tap repository must exist.** goreleaser commits into it; it does not create it.
- **`deploy/kubernetes/` pins an image tag** (`v0.1.0`). Manifests and releases are versioned
  separately, so a tag bump has to reach them or a CronJob pulls something that is not there.

The module path, the GitHub owner and the image namespace all read `maxwell-cudlitz`, and they
are three independent strings that have to move together: the module path lives in `go.mod`,
every import, the `-X` flags in the Makefile and `.goreleaser.yaml`, and the wrapcheck glob in
`.golangci.yml`; the owner keys the image, the tap and the clone URLs in the documentation.

## Extension points

Each of these is a registry plus an interface; adding an implementation should not
require touching the pipeline.

- **A source**: implement `source.Connector` in `internal/source/<name>/`, call
  `source.Register` from an `init`, blank-import the package from `cmd/inget-fetch/main.go`,
  and add its config block plus datatype definitions. Decode the driver-specific `domain` and
  `limits` maps with `config.DecodeInto`, which is strict, so a misspelled key fails the run.
  Implement `source.EventMapper` too if the source can deliver a webhook. Four things are
  `internal/fetch`'s job and must not be duplicated in a connector: blob writes, the level-0
  skip, tombstones and the fragment cap. A connector returns fragments sorted by tier then key
  and reports its own non-fatal problems as `Result.Warnings`.
- **A datatype**: define its fragmenter, tiers and views in config, add prompt templates
  under `prompts/<source>/<datatype>/`, then gate it with `inget eval`. The harness needs no
  code change: it reads the view list from config and reports per datatype.
- **An enricher**: implement the enricher interface in `internal/enrich/`; it must
  contribute every input to its signature (D2) or the cascade will serve stale output.
- **A reference resolver**: implement `refs.Resolver` in `internal/enrich/refs/`, call
  `refs.Register` from an `init`, and add whatever config field it needs to
  `config.Reference` plus its validation. A resolver is read-only and idempotent, returns an
  empty `Record` rather than an error for a missing referent, and applies the declared
  `fields` as an allowlist — a resolver that returns more than config asked for is a resolver
  that feeds a model something nobody reviewed. It needs no signature: the payload digest in
  `internal/enrich/refs/digest.go` already covers what it returned.
- **A destination**: implement the destination interface in `internal/destination/`,
  including `AssertModel` so an embedder change cannot silently corrupt an index (D7), plus
  `RebindModel` and `PruneStaleVectors`, which are what `inget reindex` needs to get past that
  same check deliberately. A driver that implements `AssertModel` and not the other two makes
  an embedder change unrecoverable for it.
- **A state driver**: add a `dialect` entry in `internal/state/dialect.go` and a migration
  directory under `migrations/state/`. If the new dialect needs a fourth difference beyond
  placeholders, row locking and lock strategy, add the field rather than a second
  implementation of the statements — the conformance suite is what keeps the drivers
  honest, and it only works while there is one implementation to test.

## Hazards

- **Reference staleness lives on the edge, not in a queue (`internal/state/refs.go`).** A
  changed record sets `refs.stale_depth` on every edge pointing at it — one indexed UPDATE
  regardless of fan-out — and the mark is cleared only by the referring item's own
  re-resolution, inside that item's checkpoint transaction. Two consequences are load-bearing.
  Deferral is free: an item the per-run cap left out keeps its mark and the next run's identical
  query finds it, so nothing needs to remember what was skipped. And the cascade cannot be made
  to loop, because the mark carries the depth it was made at and a record at
  `enrich.max_reference_depth` marks nothing. Do not "optimise" this into an unconditional
  cascade: `processItem` only cascades for an item that republished something, which is what
  stops a cycle from costing a reprocess on every future run.
- **A reference payload is an input, so it is in a cache key or the cascade is broken
  (`internal/enrich/refs/resolve.go`).** Fragment-injected payloads become compose entries
  keyed `ref:<name>`, so the existing `depends_on` globs and the composed hash cover them.
  Metadata-injected payloads reach every prompt and no glob can scope them, so they contribute
  `Resolution.Digest` to every view's level-2 input hash instead. A third injection path would
  need a third answer to "which key moves when this changes" before it is added.
- **Tombstones are computed from the enumerated set, never from the record set
  (`internal/fetch/run.go`).** An item that was enumerated and then failed to fetch is in
  `seen`, so it produces no tombstone. Deriving tombstones from what was written instead
  would delete every item that hit a transient error. `--limit`, `--only`, `--since` and
  `scope: partial` all suppress tombstones entirely, and every one of those cases has a
  test, because a wrong answer here deletes a live index.
- **The artifact writer outlives the worker pool (`internal/fetch/run.go`).**
  `errgroup.WithContext` cancels its context when `Wait` returns, and the shard writer holds
  the context of the first `Write` for its whole lifetime. Workers are therefore handed the
  run's context, not the group's; passing the group's makes `Commit` fail with
  `context canceled` on a run that otherwise succeeded. The group context governs
  enumeration only.
- **A git blob SHA is not a blob digest.** The tree endpoint's `sha` is SHA-1 over
  `blob <len>\0` plus content and is the level-1 *fingerprint*; the blob store keys on
  SHA-256 of the raw content. They are never interchangeable. Reusing a stored blob
  reference is safe only because a fingerprint match means the content is identical and GC
  never collects a blob live state references.
- **Sub-file fragments cannot use the file's blob SHA (`internal/source/github/split.go`).**
  Every piece would share it and the cascade could not tell which piece changed, so each
  piece is fingerprinted by the SHA-256 of its own bytes. Splits must also be
  deterministic: a cut point that moved between fetches would re-derive every piece of
  every large file forever.
- **A directory named `doc` is a Go or Python package as often as documentation
  (`internal/source/github/filter.go`).** Tier patterns are matched first-wins, so a broad
  `doc/**` in tier 0 silently reclassified `doc/*.go` as documentation and reordered
  composition. Prefer extensions over directory names, and enumerate extensions rather than
  globbing `main.*`, which claims `main.tf` and `main.css`.
- **Separator-free tier and skip patterns match at any depth.** `matchPath` falls back to
  the basename when a pattern has no `/`, so `LICENSE*` drops `docs/LICENSE.md` as well as
  the root file. That is intended; adding a pattern without a separator is a decision about
  every directory, not just the root.
- **A failed view discards the item's successful ones (`internal/state/checkpoint.go`,
  `internal/pipeline/views.go`).** An item's guards are written in one transaction, so failing on
  the eighth view throws away the seven generations that succeeded and the retry pays for all
  eight. The atomicity is correct — a stored view claims generation, embedding and upsert all
  happened — but there is no level-2 equivalent of the derivation cache, so nothing survives the
  failure. Cached derivations are what make a fragment-stage retry nearly free; a view-stage retry
  is full price. If view failures become routine, the fix is a generation cache keyed by view name,
  composed-input hash and enricher signature, not a weakening of the checkpoint.
- **A list prompt needs a countable bound (`prompts/github/repo/aliases.tmpl`).** "At most 40
  lines" was not kept: numbered input categories invited a headed, sectioned answer past the token
  budget. State the output shape as a hard requirement with a line count, a per-line word count and
  an explicit "nothing else". A view whose output shape is a list is the one most likely to
  overrun, because prose prompts are bounded by paragraph counts the model does respect.
- **A thinking model spends the output budget before it answers
  (`internal/model/options.go`, `config.yaml`).** DeepSeek V4 enables thinking by default,
  reasoning tokens come out of `max_tokens` and are billed at the output rate, and `temperature`,
  `top_p` and `seed` have no effect while it is on. So a 1024-token budget was consumed by
  reasoning before the answer began — every view failed with `finish_reason: "length"` — and the
  determinism this pipeline's cache keys claim did not exist. `models.generator.request_options`
  is the generic fix: an opaque map merged into the request body, so any provider parameter works
  without new code. It feeds the generator signature, because a parameter that changes output must
  invalidate what was cached under the previous value; keys the client sets itself (`model`,
  `messages`, `temperature`, `seed`, `max_tokens`, `stream`) are refused at config load rather
  than silently overriding a budget the truncation guards depend on. Note that viper lowercases
  map keys, so a provider parameter spelled in camelCase cannot be expressed here.
- **Diagnose a `length` failure by its reasoning tokens, not by the prompt
  (`internal/model/generator.go`).** `completion_tokens_details.reasoning_tokens` is decoded for
  exactly this: "the budget went on thinking" and "the answer was too long" are the same error
  with different fixes, and the first cannot be fixed by shortening a prompt. The error message
  names the split and recommends accordingly.
- **A prompt that asks for more than `max_output_tokens` fails every large item
  (`prompts/github/repo/*.tmpl`, `internal/model/generator.go`).** The shipped view prompts asked
  for "3-6 paragraphs" of "clear, detailed description" against a 1024-token budget, so DeepSeek
  hit the cap, returned `finish_reason: length`, and the generator failed the item — correctly, a
  summary truncated mid-word must not become a vector. Every item died on `role`, its first view,
  and never reached the other seven: 50 items, 2,657 derivations paid for, zero views stored. The
  budget and the prompt are one setting in two files; when either moves, check the other. Prefer
  shortening the prompt: `max_output_tokens` is in the fragment enricher's signature too, so
  raising it can invalidate cached derivations when items are processed; a view prompt edit
  similarly changes that view's signature. Full unchanged-item rebuilds use the explicit flag.
- **`compose.max_chars` must leave room for the prompt template
  (`internal/config/validate_lists.go`).** It was equal to `models.generator.max_input_chars`, and
  the generator rejects rather than truncates an oversized prompt, so an item that composed to the
  cap failed on the template's own bytes — 120,000 of document became a 121,570-character prompt.
  Validation now demands a margin of `min(8192, max_input_chars/8)`; the fraction is what keeps the
  rule usable for a small local model. The default `compose.max_chars` is 100000 for the same
  reason, a deviation from the 120000 in the design document recorded in `docs/progress.md`.
- **The artifact high-water mark is what makes many producers safe
  (`internal/pipeline/drain.go`, `internal/state/consumed.go`).** Reading only the newest
  committed run loses every other submission when producers commit one run per item — a CI job
  announcing its own repository — and loses it silently, because a partial run proves nothing
  about the items it omits, so nothing is deleted and nothing complains. `inget_state.consumed_artifacts`
  records how far a datatype has consumed and `inget run` drains the rest oldest first. The mark
  advances only behind a pass with no failed item, not narrowed by `--only` or `--limit`, and not
  interrupted; anything else means read this run again, which is one reconcile and a retry. Three
  bounds: an unmarked datatype consumes the latest run only, so adopting the mark does not replay
  a month of retained runs; the upsert is guarded so the mark cannot rewind and replay paid work;
  and an undrained backlog older than `retention.runs` is collected by gc, so the drain has to
  keep up with retention.
- **A plan over a backlog is a lower bound, not an upper bound
  (`internal/pipeline/run.go`).** Every other estimate in this codebase is deliberately
  conservative, but a drain processes every pending run while the estimate prices only the oldest.
  `Plan.PendingArtifactRuns` is what says so, and a backlog greater than one logs a warning. Do
  not "fix" this by summing an estimate per pending run: state does not advance during a plan, so
  an item appearing in several runs would be priced several times.
- **A path cannot tell you a file is a heap dump (`internal/source/github/content.go`).**
  `keepPath` runs against the tree before anything is transferred, which is what makes it cheap,
  and it is blind to `java_pid26365.hprof`, `lighthouse-bundle.js` and `strings/words.txt` — 40%
  of one 50-repository corpus, each file costing a derivation to summarise as "a list of words".
  So a second filter judges shape once the bytes are in hand: non-text content at any size, and
  above 256 KB a longest line over 5,000 bytes (minified or bundled), a mean line under 16 bytes
  (a wordlist, an export), or a bulk extension (`.json`, `.txt`, `.xml`, `.yaml`, `.sql`), where
  large size means records rather than a large document. The mean-line bound is low because real
  code averages lower than it looks: a 275 KB hand-written Rust file in the sample corpus averages
  34 bytes a line, and a bound near 40 dropped it. It runs on the whole file before splitting, so
  one dump is one decision rather than sixteen. Thresholds are loose on purpose: a false negative
  costs one summary, a false positive silently drops something a person wrote. Excluded files stay
  listed with `meta.excluded` and their fingerprint, so the exclusion is visible in the manifest
  and the next change is still detectable. Measured on the 50-repository sample: 119.5 MB of
  fragment content down to 88.3 MB, with no source file among the exclusions.
- **`fragment_enricher.max_input_chars` truncates, and it is the pipeline's main cost control
  (`internal/enrich/fragment.go`).** It fed only the enricher signature until derivation was
  metered: a 1 MB file was sent whole, and `models.generator.max_input_chars` then *rejected* the
  prompt, so the run failed instead of costing money. Both halves were wrong. A summary of the
  first 8,000 characters describes what a file is, and the rest buys a summary of the same
  length. Because the behaviour changed, the fragment enricher's schema version is 2: derivations
  cached under version 1 came from untruncated content.
- **Secret findings are fully redacted (`internal/source/github/secrets.go`).** The detector
  is built with `Redact = 100`, so a finding carries no plaintext. Lowering it would put
  live credentials into memory that log lines and warnings could reach. Rule IDs are what
  gets recorded; content that matched is excluded from the artifact but keeps its
  fingerprint, so its next change is still detectable.

- **Prompt templates are read from disk, and that is what shapes the release artifacts
  (`internal/enrich/prompt.go`).** Migrations are embedded; prompts are not, because a prompt is
  meant to be edited and its bytes feed the enricher signature. Three consequences: the archives
  ship `prompts/` and `config.yaml`, the image copies `prompts/` and sets `WORKDIR /opt/inget` so
  the relative paths in a mounted config resolve, and a Homebrew install — a cask installs
  binaries only — has neither, which is what the cask's caveats say. Embedding them with
  `go:embed` would close that gap and would also make the shipped bytes unmodifiable, so it is a
  design decision, not a packaging fix.
- **Nothing is compiled inside the Dockerfile (`Dockerfile`, `.goreleaser.yaml`).** goreleaser
  cross-compiles first and hands the build a context holding only the binaries plus whatever
  `extra_files` lists, so the image carries the same bytes as the archives. Adding a builder
  stage would produce a second, differently-built binary under the same version. It also has no
  `RUN` instruction, which is why the release workflow needs no QEMU: nothing executes for the
  target platform. Add one and multi-arch builds start needing emulation.
- **The base image is pinned by index digest, not by tag.** `nonroot` moves. Refresh it with
  `docker buildx imagetools inspect gcr.io/distroless/static-debian13:nonroot` and take the
  top-level `Digest`, not a per-platform one, or the multi-arch build loses an architecture.
- **Released binaries are unsigned, and the cask works around it.** The post-install hook clears
  `com.apple.quarantine`, without which macOS reports "inget is damaged and cannot be opened".
  That bypasses Gatekeeper; the honest fix is an Apple Developer certificate and notarization,
  which costs money the project does not spend. Do not remove the hook without adding signing.

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
- **`inget eval` reads stored text and never generates (`internal/eval/eval.go`, D14).** It
  takes an `Embedder` and no `Generator`, deliberately: generation is not reproducible on
  either candidate provider, so regenerating per run would fold that variance into every
  metric and confound the `--embedder` comparison the whole decision exists to serve. Adding
  a generator to the harness would make its numbers unusable for the one job they have.
- **Eval retrieval is computed inside the sample, not against a destination
  (`internal/eval/vector.go`).** The sample is re-embedded by the embedder under test, whose
  vectors do not belong in a destination bound to another model (D7), and the gate must run
  without a reachable vector database. Two consequences: scores compare across runs only at a
  fixed `eval.sample_size`, and the sample is drawn in digest order rather than at random so
  that two runs score the same items.
- **Self-retrieval excludes the query's own vector (`internal/eval/metrics.go`).** A query
  identical to a document always ranks that document first, so including it would report a
  perfect score forever. What is measured is whether an item's views retrieve *each other*
  before another item's, which is why a single-view item is reported as unscorable rather than
  as a hit. Every metric with a zero denominator is skipped, and a skipped metric is neither a
  pass nor a breach — but an unscorable corpus is not a pass, because "the pipeline never ran"
  must not read as "quality is fine".
- **Repeated texts are sent to the embedder once (`internal/model/embedder.go`).** Measured on
  2026-08-02: TEI 1.9.3 on Metal returned a wrong — internally consistent, unrelated to the
  text — vector for an input that appeared more than once in one request, nondeterministically,
  while the same text sent alone embedded correctly. Distinct inputs, including mixed lengths,
  were unaffected. `Embed` therefore dedupes before batching and fans the vector back out. Do
  not remove that: identical text must embed identically, and eval's distinctiveness metric
  reads 0.12 instead of 0 on a degenerate corpus when it does not.
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
- **Vectors cross the wire as text (`internal/destination/row.go`).** An embedding is sent
  as `[0.1,0.2,…]` and cast to the column's type server-side. The binary protocol would be
  about four times smaller, but halfvec is IEEE 754 half precision and hand-rolling float32
  to float16 — subnormals, overflow, round-to-nearest-even — produces vectors that are
  quietly slightly wrong, which nothing detects because nothing fails: search answers just
  get worse. This is also why `BulkLoad`'s staging column is `text` and the cast happens in
  the merge, so `CopyFrom` never speaks halfvec at all.
- **The destination schema is a rendered template (`migrations/destination/pgvector/`).**
  A vector column's width and storage type come from config and PostgreSQL cannot
  parameterise a type modifier, so the `.sql` file is a Go template and is not runnable by
  `psql` as it stands. Two consequences to preserve when editing it: index names are derived
  from the table name, and the extension and the model registry are `IF NOT EXISTS`, because
  one database can hold several destinations. Each destination also gets its own goose
  version table (`<table>_goose_version`) — with one shared table the second destination
  would read version 1 as already applied and create nothing.
- **`CREATE TEMP TABLE (LIKE …)` drops defaults.** `BulkLoad` needs `INCLUDING DEFAULTS`:
  `LIKE` alone copies `NOT NULL` but not `DEFAULT now()`, so `updated_at` has no value and
  the COPY fails. Indexes are deliberately not copied.
- **D7 is enforced at the write path, not just at `AssertModel`.** `Upsert` and `BulkLoad`
  check every row's model, signature and width against the binding, and refuse to write at
  all if `AssertModel` was never called. Removing that check would let a batch assembled
  from two embedders land in one table, which is the failure the registry exists to prevent
  and which no query would report.
- **`hnsw.iterative_scan` is what makes one index enough (D9).** `Search` sets it per
  transaction. Below pgvector 0.8.0 the setting does not exist and a filtered search loses
  recall silently rather than failing, so `Migrate` refuses an older extension. Do not drop
  either the version check or the `SET LOCAL`.
- **wrapcheck exempts this module's own internal packages (`.golangci.yml`).** They wrap at
  their own boundaries and their messages are asserted by tests, so a second wrap in a
  caller repeats the phrase instead of adding to it. Errors from outside the module are
  still checked everywhere; do not widen the glob further.
- **Cost.** `inget plan` exists so no run spends money unexpectedly. Any change that can
  increase LLM calls must be visible there first.
- **Guards are written after the work they attest to (`internal/pipeline/process.go`,
  `internal/state/checkpoint.go`).** An item's fingerprint, fragment fingerprints and view
  hashes all claim that generation, embedding and upsert already happened, so they go in one
  `CheckpointItem` transaction after the upsert returns. Writing any of them earlier — the
  natural-looking "persist the item, then process its views" order — makes an item that
  failed halfway reconcile as unchanged on the next run, and nothing regenerates it until an
  unrelated fragment happens to change. Derivations are the deliberate exception: they are a
  cache, not a guard, and are written as they are produced so a later failure does not
  discard paid-for tokens.
- **The shutdown channel belongs to the signal handler (`internal/pipeline/workers.go`).**
  `runWorkers` reads it from the context the caller wired to `NotifyShutdown`. A pool that
  creates its own channel compiles, passes every test that calls `ShuttingDown` directly,
  and never drains on SIGTERM, because nothing ever closes the channel it is watching.
  `TestInterruptedRunResumesWithoutDuplicatingWork` is the regression guard. `ShuttingDown` is
  exported because `inget reindex` observes the same channel through the same context.
- **Row metadata is assembled in one place (`internal/pipeline/metadata.go`).**
  `AnnotateMetadata` is exported because two callers must agree on it exactly: the pipeline
  writing a row and `inget reindex` rewriting that row from state. A reindex that assembled
  metadata differently would quietly change what a metadata filter matches, and the only symptom
  would be search results that used to be there. The one part reindex cannot reproduce is a
  metadata-injected reference payload, which is not persisted; the pass logs that once per
  datatype that declares one, and `inget run` restores those values on the item's next change.
- **Views compose their own scope (`internal/pipeline/views.go`).** Each view is composed
  over only the fragments its `depends_on` globs match, via `delta.MatchesAny`. Composing the
  whole item once and handing it to every view puts a repository's entire contents in front of
  a prompt asking about its build files, and makes every level-2 hash move whenever any
  fragment moves — `delta.ViewInputHash` names its argument `scopedComposedHash` for that
  reason. `TestCascadeScopesViewInputToDependencies` asserts the stack view never sees the
  README.
- **Vector reuse requires byte-identical text (`internal/pipeline/views.go`).**
  `reuseVector` keeps a stored vector only when the new generated text hashes to the same
  `EmbeddedHash` as the stored vector. Any textual change, however small, forces a
  re-embed. A changed embedder model or signature also always forces a re-embed. The level-2
  input hash (stage 7) is what protects the only paid operation (generation).
- **`models.generator.concurrency` is one shared budget (`internal/pipeline/limiter.go`).**
  The fan-out is nested — the item pool spawns fragment derivations — so the permit set is
  run-scoped and both stages draw from it. Applying the limit at each level instead
  multiplies into `concurrency²` requests against a provider configured for `concurrency`.
  Concurrent derivations of the same fragment are collapsed with `singleflight`, because two
  items holding the same file at the same path is ordinary and the persisted cache cannot help
  while both are in flight.
- **A fragment prompt sees only its key and its content
  (`internal/enrich/enricher.go`).** `FragmentTemplateData` carries no item metadata by
  design: a derivation is cached under `delta.FragmentCacheKey(key, fingerprint, signature)`,
  so anything else a prompt could read would be an input the key does not distinguish, and two
  repositories holding the same file would share one summary written about the first of them.
  Adding a field to that struct means adding it to the cache key.
- **Prompt templates carry an injection guard, and it is part of the signature
  (`prompts/`).** Ingested content is untrusted input (OWASP GenAI LLM01:2025, indirect
  injection). Every template delimits it with `<UNTRUSTED_DATA>`, states that it is data, and
  shows a worked example of an embedded instruction being described rather than obeyed;
  `internal/enrich/prompts_test.go` fails if a template loses any of that. The guard text is
  duplicated per file rather than shared through a partial: prompt bytes feed the enricher
  signature, so each template's guard is versioned with the prompt it protects. Editing a
  guard regenerates that view for every item, which is the correct consequence of changing a
  prompt.
- **The plan estimate probes the derivation cache per fragment
  (`internal/pipeline/estimate.go`).** That is affordable when producing the estimate is the
  point, and wasteful ahead of a run that is about to look the same keys up anyway, so only
  dry-run mode calls `estimateWork`. Signature changes are surfaced on both paths, at one
  query per view. Estimates are upper bounds: the level-2 guard can still skip a view whose
  scoped composition turns out unchanged, and that cannot be known without deriving, and every
  call is priced at the full output budget rather than the shorter completion it will return.
  An upper bound is not a licence to price work the run will not perform, so input is bounded by
  the same truncation the run applies — `fragment_enricher.max_input_chars` per fragment,
  `compose.max_chars` per view — and a fragment with no stored blob costs nothing, because
  nothing is sent for it and composition drops it.
- **`inget reindex` re-embeds and never regenerates (`internal/reindex/`).** The text in
  `state.views.text` is by definition what each stored vector was made from, so an embedder
  change needs no generator, no artifacts and no tokens. A *prompt* change is the other
  direction — it changes what the text should say — and belongs to `inget run`, whose cascade
  already regenerates a view whose enricher signature moved and whose cost `inget plan` reports
  first. Adding generation here would be a second implementation of stages 5–11 with no
  artifacts to read, and it would spend money from a command nobody expects to.
- **Reindex is the only caller allowed to rebind, and rebinding is why it exists
  (`internal/destination/pgvector_rebind.go`).** `AssertModel` refuses a disagreement on
  purpose, which means an embedder change stops *every* write until the registry moves — that
  deadlock is what `RebindModel` breaks. Between the rebind and the end of the pass the table
  holds two vector spaces; that window is unavoidable, so the rebind logs at warn level and the
  pass that follows is not optional. `cmd/inget/reindex.go` opens destinations with
  `openDestinationUnbound` for the same reason; no other command may.
- **The prune deletes by "not written by the current binding", so its scope is load-bearing
  (`internal/reindex/pass.go`).** It runs only after a full-scope pass that completed: a
  view-restricted pass would delete the views it deliberately skipped, and an interrupted one
  would delete the items it had not reached yet. A datatype with no stored views prunes nothing
  at all and says so — pruning there would empty the table because state is empty, which is a
  second outage rather than a recovery. `--prune-unconfigured` is the only way to reach rows of
  datatypes configuration no longer declares, and it demands a complete pass over everything.
- **Reindex records its own binary name (`internal/reindex/reindex.go`).** Run rows are keyed
  by `inget-reindex`, not `inget`, so a half-finished reindex is never adopted as a resumable
  pipeline run or the reverse. The two compute different work sets from the same datatype, and
  adopting one for the other would silently skip whatever the other had already marked done.
- **Collection order is the correctness argument, and it holds every lock
  (`internal/gc/`).** Runs first, then blobs, then derivations: a blob's retention predicate is
  "no retained run and no live fragment references it", so computing it against directories that
  are about to disappear keeps their blobs alive for another cycle. Collection takes both locks
  of every datatype — `<datatype>` and `fetch:<datatype>` — because "unreferenced" is a global
  claim and a fetch that has uploaded blobs but not yet committed its manifest would otherwise
  have them collected out from under it. That is also why `state gc` takes no datatype argument:
  collecting one datatype's runs would delete blobs another still points at.
- **An uncommitted run directory contributes no retained blobs (`internal/gc/phases.go`).**
  Its records cannot be read, so what a dead fetch left behind is collected. That is correct and
  recoverable — content addressing means the next fetch re-uploads exactly what it needs — but
  it is a deliberate asymmetry with phase 1, which does retain young uncommitted directories.
- **`_COMMIT` is deleted first (`internal/artifact/gc.go`).** Deleting a run directory mirrors
  the commit protocol in reverse, so an interrupted deletion leaves a directory every consumer
  already ignores rather than a committed run whose shards have started disappearing. `Blobs`
  also skips any object under `blobs/` whose name is not a digest: a shared store may hold
  things that are not ours to collect.
- **`state show` is the only read that returns a timestamp (`internal/state/inspect.go`).**
  PostgreSQL stores `timestamptz` and SQLite stores `CURRENT_TIMESTAMP` text, so the columns are
  scanned untyped and normalized by `asTime` rather than by a second set of statements. Do not
  add a timestamp to any other method: every guard is compared for inequality and run history is
  ordered by ULID, so nothing else needs one.
- **`ForceUnlock` cannot steal a postgres lock, and that is the answer rather than a gap
  (`internal/state/lock.go`).** An advisory lock lives in its holder's session and dies with it,
  so a held one belongs to a live process; `state unlock` probes and reports `ErrLocked` instead
  of terminating the backend. Only sqlite has a stale row to remove. Relatedly, `Store.Close`
  blocks while a lock is held, because the lock pins a connection — release before closing, in
  tests as well as in commands.
