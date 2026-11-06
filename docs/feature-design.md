# Feature Design: inget

## Overview

`inget` is a config-driven, one-shot, open-source pipeline that ingests records from
arbitrary data sources, enriches them into multiple semantic views, and embeds those
views across N logical indices in one or more vector destinations.

It ships as two binaries sharing a single root config:

- **`inget-fetch`** talks to source APIs (GitHub, Monday.com), applies a configured
  domain, and writes immutable, content-addressed artifacts to a blob store
  (local filesystem, S3, or GCS).
- **`inget`** reads those artifacts, reconciles them against persisted state,
  enriches only what changed, embeds only what materially changed, and upserts
  vectors into destinations (PostgreSQL + pgvector by default).

The design goal that shapes everything else: **a single changed file in a single
repository must not cause a whole repository to be re-summarized or re-vectorized.**
Every stage is guarded by a content-addressed cache key, so work is skipped at the
earliest possible point.

The tool holds no durable state of its own. All state lives in an external database.
All model endpoints default to local services. Both binaries are safe to run under a
Kubernetes CronJob, a systemd timer, or a webhook-triggered job.

---

## Glossary

This vocabulary is normative. The terms below are deliberately distinct because they
are routinely conflated, and conflating them is what makes incremental pipelines hard
to reason about.

| Term | Meaning |
|---|---|
| **Source** | A connector family: `github`, `monday`. Owns auth, enumeration, rate limits. |
| **Datatype** | A concrete record kind within a source: `github/repo`, `monday/item`. Owns metadata shape, fragmenting, ID scheme, and default views. |
| **Item** | One record of a datatype. The unit of identity. |
| **Fragment** | An addressable sub-unit of an item carrying a content fingerprint: a file in a repo, a column in a Monday item, a comment. **The unit of incremental change.** |
| **View** | A named semantic facet of an item that becomes exactly one embedding: `role`, `surface`, `stewardship`. These are the "N indices". (Often called *chunks*; that term is avoided here because it also means text windows.) |
| **Destination** | A physical vector store instance: a pgvector table, later a Qdrant collection. |
| **Splitter** | A deterministic text-window splitter for unstructured long prose. Distinct from fragments. **Deferred; not in v1.** |
| **Derivation** | A cached artifact produced from one fragment by one enricher, keyed by fragment fingerprint plus enricher signature. |
| **Signature** | A hash covering every input that can change an output: model ID, prompt template bytes, truncation limits, schema version. |
| **Run** | One invocation of either binary, identified by a ULID. |
| **Scope** | Whether a run enumerated the entire domain (`full`) or a caller-supplied subset (`partial`). |

---

## Architecture

```
                    ┌──────────────────────┐
                    │  config.yaml (root)  │
                    │  + config.local.yaml │
                    │  + INGET_* env       │
                    └───────┬──────┬───────┘
                            │      │
        ┌───────────────────┘      └───────────────────┐
        │                                              │
┌───────▼─────────────────┐              ┌─────────────▼───────────────┐
│      inget-fetch        │              │           inget             │
│                         │              │                            │
│  connector registry     │              │  artifact reader           │
│   ├── github            │              │  delta reconciler          │
│   └── monday            │              │  reference resolver        │
│  domain enumeration     │              │  enricher registry         │
│  adaptive rate limiter  │              │   ├── llm                  │
│  fragmenter             │              │   └── passthrough          │
│  level-0 skip (state)   │              │  view generator            │
│                         │              │  embedder client           │
│                         │              │  destination registry      │
│                         │              │   └── pgvector             │
└──────────┬──────────────┘              └─────────┬──────────────────┘
           │ writes                                │ reads
           ▼                                       ▼
   ┌───────────────────────────────────────────────────────┐
   │  Blob store  (file:// | s3:// | gs://)                │
   │    blobs/<sha256 sharded>        immutable content    │
   │    runs/<source>/<datatype>/<run_id>/manifest.json    │
   │    runs/.../records-NNNNN.jsonl.zst                   │
   │    runs/.../_COMMIT                                   │
   └───────────────────────────────────────────────────────┘

   ┌───────────────────────────┐   ┌──────────────────────────┐
   │ State (Postgres default)  │   │ Destination (pgvector)   │
   │  items, fragments,        │   │  inget_vectors           │
   │  derivations, views,      │   │  inget_model_registry    │
   │  refs, runs, work,        │   │  HNSW halfvec(1024)      │
   │  signatures               │   │                          │
   └───────────────────────────┘   └──────────────────────────┘

   ┌───────────────────────────┐   ┌──────────────────────────┐
   │ Generator (OpenAI-compat) │   │ Embedder (OpenAI-compat) │
   │  local proxy → Kimi K3 /  │   │  local TEI / Ollama      │
   │  DeepSeek V4              │   │  Qwen3-Embedding-0.6B    │
   └───────────────────────────┘   └──────────────────────────┘
```

### Why two binaries

Fetching and enriching have opposite operational profiles. Fetching is
network-latency-bound, needs source-specific credentials, and is governed by
third-party rate limits. Enriching is cost- and compute-bound, needs model
credentials, and is governed by token budgets. Splitting them means:

- A fetch failure (expired token, API outage) cannot waste LLM spend.
- Artifacts are re-processable without re-fetching, so prompt iteration is free of
  API cost and rate limits.
- `inget` has no source credentials at all, shrinking its blast radius.
- The blob store is a natural audit trail and replay log.
- Either side scales independently: many fetchers, one enricher, or vice versa.

The cost is a wire contract between them, specified in `docs/artifact-envelope.md`.

---

## Design Decisions

### D1. Staged content-addressed invalidation cascade

**Decision.** Four guard levels, each keyed by a hash of all of its inputs. Work at
level *n* is skipped when its key matches persisted state.

| Level | Guard key | Skips | Where |
|---|---|---|---|
| 0 · item | source-supplied item fingerprint (`pushed_at`, `updated_at`, ETag) | the network fetch itself | `inget-fetch` |
| 1 · fragment | `hash(fragment.fingerprint ‖ enricher.signature)` | per-fragment enrichment | `inget` |
| 2 · view input | `hash(scoped composed document ‖ view prompt signature)` | view generation (the expensive LLM call) | `inget` |
| 3 · embedding | `hash(view text actually embedded)` | embedding + destination upsert | `inget` |

**Rationale.** This is the early-cutoff property from incremental build systems.
Salsa (rust-analyzer) documents that "even if one input to a query changes, the result
may be the same. Early cutoff takes advantage of that, and re-uses results which
depend on the AST but not on the original source file." Nix RFC 0062 introduced
content-addressed derivations for exactly this reason: input-addressing over-invalidates,
forcing rebuilds of downstream artifacts whose bytes did not change. Applying the same
staging to an LLM pipeline converts per-item cost into per-change cost.

Concretely, for a one-file commit to a 5,000-file repository with 8 views:

| strategy | file derivations | view generations | embeddings |
|---|---|---|---|
| no incrementality | 5,000 | 8 | 8 |
| per-fragment cache only | 1 | 8 | 8 |
| staged cascade (this design) | 1 | ~2 | ~2 |

**Trade-offs.** More persisted state (one row per fragment per item), and more places
for a stale cache to hide. Mitigated by D2.

**Reference.**
- https://rust-analyzer.github.io/blog/2023/07/24/durable-incrementality.html
- https://github.com/NixOS/rfcs/blob/master/rfcs/0062-content-addressed-paths.md
- https://bazel.build/versions/8.0.0/basics/hermeticity

### D2. Signature completeness is mandatory

**Decision.** Every cache key includes a `Signature()` covering the complete input
set: model ID, exact prompt template bytes, truncation limits, enricher options, and
envelope schema version. Signatures are persisted in `signatures`. A changed signature is
reported by `inget plan` and `inget run`, but default runs keep their item-delta work set.
Only `--rebuild-on-signature-change` widens the work set to every live item in a full-scope
artifact; `inget plan` estimates that opt-in rebuild before it is run. The signature is
recorded only after an unrestricted rebuild succeeds, so a failed or limited pass remains
detectable and can be retried.

An opt-in `--indexed-only` selector restricts that widening to items with successful live
checkpoints, before `--only` and `--limit`. Missing historical signature rows are also
treated conservatively as unknown for an explicit rebuild of existing checkpoints.
Default runs still do not widen work when a signature is changed or unknown.

`--cached-fragments-only` preflights the selected artifact pass and disables fragment
generation throughout execution. By default it requires the current exact level-1 key.
An explicit `--cached-fragment-signatures` allowlist can fall back to older derivations
with the same file path and fingerprint, in supplied order after the current signature.
Those outputs are not relabeled or copied under the current signature, and a pass using
the allowlist does not record the current fragment scope as globally rebuilt. The composed
text continues to determine level-2 hashes. No migration or change to normal cache lookup
semantics is involved.

**Rationale.** This is the single most common failure in content-addressed pipelines.
Keying a per-fragment summary on the git blob SHA alone omits the prompt and the model
from the key, so editing the prompt or switching models leaves every cached summary
permanently stale with no signal that anything is wrong. The prompt/model signature remains
part of every cache key, and the signature-change warning makes stale output visible; the
operator chooses when to pay for updating unchanged items. Turborepo's documented failure
mode is identical: "a variable that is not declared is simply not part of the hash, and
that single fact explains most of the [cache miss] failures." Bazel calls the property
hermeticity and treats undeclared inputs as a correctness bug rather than an
optimization gap.

**Trade-offs.** Prompt edits can trigger mass re-enrichment, which is expensive. The default
avoids widening work automatically; the explicit flag opts into that cost after `inget plan`
shows the rebuild estimate.

**Reference.**
- https://computingforgeeks.com/turborepo-cache-misses-environment-variables/
- https://bazel.build/versions/8.0.0/basics/hermeticity

### D3. Views declare fragment dependencies

**Decision.** Each view declares `depends_on`, a list of glob patterns over fragment
keys. The level-2 guard hashes only the composed document *scoped to matching
fragments*. A view whose globs match no changed fragment is not regenerated at all.

```yaml
views:
  - name: role
    depends_on: ["**"]
  - name: stack
    depends_on: ["go.mod", "go.sum", "package.json", "pyproject.toml",
                 "requirements.txt", "Cargo.toml", "pom.xml", "*.tf", "Dockerfile*"]
  - name: stewardship
    depends_on: ["CODEOWNERS", ".github/**", "docs/**", "README*", "AGENTS.md"]
```

**View taxonomy.** Views are framed as *questions a searcher asks*, not as attributes a
record possesses. This matters for two reasons: a question maps directly onto a query
someone will actually type, and it produces a natural dependency scope, because the
files that answer "what does this expose?" are a different set from those that answer
"how is it operated?" The `github/repo` set:

| View | Question it answers |
|---|---|
| `role` | What part does this play in the wider system, and who uses it? |
| `surface` | What does it expose — APIs, endpoints, CLI commands, schemas? |
| `internals` | How is it built, and what are its moving parts? |
| `stack` | What languages, frameworks, and runtimes does it use? |
| `integrations` | What external systems does it talk to, in which direction? |
| `stewardship` | Who maintains it, how actively, and how is it consumed? |
| `operations` | How is it built, configured, deployed, and run? |
| `aliases` | What else might someone call this? |

`surface` and `internals` are deliberately separate. "Which service exposes a health
endpoint" and "how is that service structured" are distinct retrieval intents, and
folding them together produces a view that answers neither precisely. Splitting them
also yields much tighter dependency globs for `surface`, so it regenerates rarely.

Datatypes declare their own set; nothing about the count or these names is fixed by the
code. `monday/item` uses `substance` (what is being asked for) and `progress` (where it
stands).

**Rationale.** Without scoping, any change to any fragment changes the composed
document and regenerates every view. Scoping is the difference between 8 LLM calls and
~2 per commit. It is the direct mechanism by which partial changes avoid re-vectorizing
a whole source's output.

**Trade-offs.** Real staleness risk: a view whose globs are too narrow will not update
when something relevant changed outside its globs. Defaults therefore start
conservative (`role` depends on everything), tightening is opt-in per view, and
`inget reindex --views X` exists to repair a view after a glob correction. Glob
patterns are part of the view signature, so tightening a glob is itself a signature
change that invalidates and rebuilds the view once.

### D4. LLM non-determinism is handled explicitly, not ignored

**Decision.** Two layers: `temperature: 0` and a fixed `seed` where the provider
supports it; and the state table records `embedded_hash` separately from the current
`text`, so the vector and the text it came from are always known to be consistent. A
view is re-embedded only when its text is not byte-identical to the text behind the
stored vector. An embedder model or signature change forces a re-embed regardless of
text.

**Rationale.** Level 3 is defeated by generative non-determinism: the same input can
produce cosmetically different output, forcing a pointless re-embed and destination
write. D3 is the real fix (do not regenerate at all when the composed input is
unchanged); the `embedded_hash` identity check is the backstop for views that do
regenerate. Measured on 2026-08-02, DeepSeek V4 Flash is not reproducible at
temperature 0 with a fixed seed (two byte-identical requests returned 487 and 298
characters), confirming that non-determinism is real; the identity check is what
prevents it from causing pointless work.

**Trade-offs.** Every regeneration that produces even a cosmetically different string
pays for a re-embed. Since embedding is a local TEI server on Metal, this cost is
negligible. The level-2 input hash (stage 7) is what protects the only paid operation
(generation).

**Reference.** https://blogsystem5.substack.com/p/bazel-remote-caching

### D5. Immutable, content-addressed artifacts with an atomic commit marker

**Decision.** `inget-fetch` writes fragment content to a content-addressed blob store
(`blobs/<sha256 sharded>`), item records to size-targeted JSONL shards (~128 MiB
uncompressed, zstd), and a `manifest.json`. A zero-byte `_COMMIT` object is written
**last**; readers ignore any run directory lacking it. Nothing is ever mutated.

**Rationale.** Content addressing means unchanged file content is never re-uploaded —
`inget-fetch` does an existence check and skips. Size-targeted shards avoid the
small-object problem on object storage; AWS's S3 Tables compaction targets 512 MiB and
will not go below 64 MiB precisely because many small objects inflate request cost and
make listing O(n). The `_COMMIT` marker is Iceberg's optimistic-commit pattern reduced
to its minimum: readers only ever observe complete runs, so a crashed fetch is inert
rather than corrupting.

**Trade-offs.** Blobs accumulate and need garbage collection (`inget state gc`).
Two-phase visibility means a crashed run leaves orphan objects until GC.

**Reference.**
- https://iceberg.apache.org/spec/
- https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-maintenance.html

### D6. Manifests carry a scope flag

**Decision.** Every manifest declares `scope: full | partial`. A full, untruncated
producer enumeration permits explicit tombstones for items absent from the enumerated
domain. Records carry fetched changes and may omit unchanged or failed items; record
absence never proves deletion. The consumer applies only manifest tombstones, and only
under full, untruncated scope without consumer `--only` or a reached `--limit`.

**Rationale.** Without this, a webhook-triggered run carrying one changed Monday item
would be indistinguishable from "every other item was deleted", and `inget` would
delete the entire index. This single field is what makes the same pipeline safe for
both scheduled full syncs and event-driven incremental updates — the stated
requirement that the service be callable "periodically or with a webhook passing
specific update information."

**Trade-offs.** Deletions are only detected by `full` runs, so a purely webhook-driven
deployment needs a periodic full reconciliation to collect tombstones.

### D7. One embedder per destination, enforced

**Decision.** A `inget_model_registry` row binds each destination table to exactly one
`(model, dims, embedder_signature)`. Writes that disagree fail hard with remediation
guidance. Every vector row additionally records `model`, `dims`, and `signature`.

**Rationale.** Vectors from different models occupy different spaces; comparing them
produces silently meaningless rankings. Per-row provenance exists to *detect* mixing,
not to permit it. Recording it per row also makes `inget reindex` able to migrate
incrementally and verify completion.

**Trade-offs.** Changing embedding model requires a full reindex of the destination.
Made explicit and scriptable rather than accidental.

### D8. 1024 dimensions and `halfvec` storage

**Decision.** Pin the vector contract at 1024 dimensions stored as
`halfvec(1024)`. Models with different native widths use Matryoshka (MRL) truncation
to reach 1024.

**Rationale.** Three constraints converge. pgvector can only build HNSW indexes up to
2,000 dimensions for `vector` and 4,000 for `halfvec`, so wide models
(Gemini Embedding's native 3,072) are not indexable as `vector` at all. `halfvec`
halves storage at equivalent recall — Neon's 1M-vector benchmark at m=32,
ef_construction=256 found float16 matched float32 recall while halving storage and
index size and cutting build time 23%. And 1024 is the native width of the selected
local model, so the common path needs no truncation.

At the 500k-item target with 8 views (4M vectors): `halfvec(1024)` is 2,056 bytes
per row ≈ **8.2 GB** heap, versus 4,104 bytes ≈ 16.4 GB for `vector(1024)`.

Candidate embedders and how they reach 1024: Qwen3-Embedding-0.6B — native 1024, MRL
`[128…2560]`. Qwen3-Embedding-4B/8B — MRL truncate. Gemini Embedding — MRL truncate
from 3072. nomic-embed-text-v1.5 / EmbeddingGemma — native 768, would require pinning
the contract to 768 instead. bge-m3 — native 1024 but no MRL, so 1024 only.

**Trade-offs.** MRL truncation costs some quality relative to a model's native width.
Fixed dimensionality makes swapping models a reindex rather than a config change, but
D7 already requires that.

**Reference.**
- https://docs.pgedge.com/pgvector/v0-8-2/hnsw/
- https://neon.tech/blog/dont-use-vector-use-halvec-instead-and-save-50-of-your-storage-cost

### D9. All views in one table, discriminated, with iterative index scans

**Decision.** One `inget_vectors` table with `datatype`, `view_name`, and
`granularity` discriminator columns; a single HNSW index over `embedding`; B-tree
indexes on the discriminators. Queries set `hnsw.iterative_scan = relaxed_order`.
Table partitioning by `view_name` is the documented escape hatch once any single view
exceeds ~2M vectors.

**Rationale.** Before pgvector 0.8.0, HNSW applied filters *after* the index scan, so
`WHERE view_name = 'stewardship'` with the default `ef_search = 40` returned only the
handful of the 40 candidates that happened to match — recall collapsed for selective
filters. pgvector 0.8.0's iterative index scans rescan the graph until enough rows
pass the filter or `hnsw.max_scan_tuples` is hit, which removes the need for one index
per view. pgvector's own filtering documentation lists partial indexes for "few
distinct values" and partitioning for "many distinct values"; with a single global
index plus iterative scan there is no per-view DDL and views can be added from config
without a migration.

**Trade-offs.** Iterative scans can visit many more tuples for highly selective
filters, raising p99 latency. `hnsw.max_scan_tuples` bounds it. Partitioning remains
available.

**Reference.**
- https://docs.pgedge.com/pgvector/v0-8-2/iterative-index-scans/
- https://docs.pgedge.com/pgvector/v0-8-2/filtering/

### D10. Generation is hosted and OpenAI-compatible; embedding is local

**Decision.** One OpenAI-compatible HTTP driver serves both roles, differing only by
`base_url`, `model`, and API key. Defaults point at local endpoints. Neither selected
generation provider offers embeddings, so embedding defaults to a local
OpenAI-compatible server (HuggingFace TEI, Infinity, or Ollama) running
Qwen3-Embedding-0.6B at 1024 dimensions.

| Role | Driver | Default | Alternatives |
|---|---|---|---|
| Generator | `openai` | `http://localhost:4000/v1` (local proxy) | Kimi K3 `https://api.moonshot.ai/v1`, DeepSeek V4 `https://api.deepseek.com` |
| Embedder | `openai` | `http://localhost:8080/v1` (TEI) | Ollama `http://localhost:11434/v1`, any hosted OpenAI-compatible endpoint |

Verified provider facts as of 2026-07-31:

- **Kimi K3** — model ID `kimi-k3`, 1,048,576-token context, OpenAI-compatible
  `/v1/chat/completions`, `Authorization: Bearer`. $3.00/M input, $0.30/M on cache hit,
  $15.00/M output. Automatic context caching. Limits are per-tier across concurrency,
  RPM, TPM, TPD. **No embeddings endpoint.**

  Two constraints measured against the live API on 2026-08-02 that make it unusable as
  this pipeline's default. It rejects every temperature but 1 —
  `invalid temperature: only 1 is allowed for this model` — and `wire.go` sends
  `temperature` unconditionally, so the configured 0 makes every call a hard 400. It also
  emits reasoning tokens ahead of content: a one-sentence prompt spent 509 reasoning
  tokens and returned empty content with `finish_reason: length` at `max_tokens` 512,
  and needed 2048 to finish, so `max_output_tokens: 1024` is below its floor. Choosing it
  means giving up the determinism the delta cache assumes.
- **DeepSeek V4** — `deepseek-v4-pro` ($0.435/M in, $0.87/M out) and
  `deepseek-v4-flash` ($0.14/M in, $0.28/M out), both 1M context, 384K max output.
  Automatic disk context caching at 1/50 of input price ($0.0028/M cache-hit on flash,
  $0.003625/M on pro; re-verified 2026-08-02, and the pro list price is $1.74/$3.48 with
  a promotional discount currently producing the figures above). Concurrency-based limits
  (500 and 2,500 respectively). Legacy `deepseek-chat` / `deepseek-reasoner` aliases were
  retired 2026-07-24 and must not be used. **No embeddings endpoint.**

  Measured against the live API on 2026-08-02 with `wire.go`'s exact request shape:
  `temperature: 0` and `seed` are both accepted, and `max_output_tokens: 1024` is
  comfortable — a two-sentence answer finished with `finish_reason: stop` at 73–109
  completion tokens, of which only 25 were reasoning tokens, so it does not exhaust its
  output budget the way kimi-k3 does.

  **But temperature 0 plus a fixed seed is not reproducible.** Two byte-identical requests
  returned materially different text (487 and 298 characters, different wording). This
  contradicts the assumption behind `wire.go` sending both fields unconditionally, and it
  is a property of the provider, not the client — the most likely cause is batch-dependent
  routing in a mixed-expert model, which no request parameter controls. Consequences for
  D4 and D14 are recorded there.
- **Qwen3-Embedding-0.6B** — Apache 2.0, native 1024 dims, MRL
  `[128,256,384,512,768,1024,1536,2048,2560]`, 32K sequence length, MTEB English v2
  70.70, MTEB Code v1 75.41.

  **Duplicate inputs in one request are not safe on TEI 1.9.3 (Metal).** Measured
  2026-08-02: a text appearing more than once in one `/v1/embeddings` call returned a
  vector whose cosine against the same text embedded alone was 0.25 — consistent across the
  duplicates, unrelated to the correct vector — and it is nondeterministic, with the same
  pair returning correct vectors on two attempts and wrong ones on a third. Distinct
  inputs, including mixed lengths in either order, matched their single-text embeddings at
  cosine 1.000000, so this is not padding or last-token pooling. `internal/model` therefore
  sends each distinct text once and fans the vector back out, which is what "identical text
  embeds identically" requires regardless of the server.

**Rationale.** A single driver for two hosted providers plus three local servers is
justified because all five speak the same wire format; a provider-specific client per
vendor would be duplicated code with no added capability. Local-by-default satisfies
the requirement that calls go to local services where possible, and makes the tool
usable with no API keys and no network.

On sufficiency of local embedding: this pipeline embeds LLM-distilled English prose,
not raw source. Semantic retrieval over clean, topically-scoped prose is a
substantially easier task than retrieval over source files, and 768-dimension models
have proven adequate for it; the selected local model is 1024 dimensions and scores
higher on both English and code retrieval. The published gap between top open weights
and hosted leaders (Gemini Embedding, Voyage) is 1–3 MTEB points, inside the range
where leaderboard overfitting and domain mismatch dominate. The design therefore makes
this measurable rather than assumed — see D14.

`models.view_generator` is an optional complete Generator role for repository views.
Without it, views use `models.generator` as before; fragments always use the latter.
Each role contributes its own generation settings to its own enricher signatures and
its own output budget and prices to planning. Composition/input validation uses the
effective view generator. Worker concurrency and the shared request semaphore remain
controlled by `models.generator.concurrency`. This permits richer repository views
without invalidating already derived file summaries. View output must fit the configured
embedder's input limit; transports must not silently index only a prefix of stored text.

**Trade-offs.** A cold build of 4M vectors on CPU is on the order of a day; on a
GPU-backed TEI it is 1–2 hours; at hosted rates (~$0.15/M tokens) roughly $155.
Embedding is ~3% of total pipeline cost, dominated by generation, so swapping to a
hosted embedder for cold builds is cheap. The swap requires a reindex per D7.

**Reference.**
- https://platform.kimi.ai/docs/api/overview
- https://api-docs.deepseek.com/quick_start/pricing
- https://huggingface.co/Qwen/Qwen3-Embedding-0.6B
- https://huggingface.co/docs/text-embeddings-inference/quick_tour

### D11. Multi-view enrichment over naive chunking

**Decision.** The primary enrichment strategy generates several independent topical
views per item, each embedded separately, rather than splitting raw content into
fixed-size windows.

**Rationale.** The measured evidence favors multi-granularity semantic views over
naive chunking. RAPTOR (ICLR 2024) builds recursive abstractive summaries and improves
QuALITY accuracy by 20 points absolute when paired with GPT-4. Anthropic's contextual
retrieval — prepending an LLM-generated context line to each chunk — cut top-20
retrieval failures 35% alone, 49% combined with BM25, and 67% with reranking.
Dense-X / propositional retrieval (EMNLP 2024) reports that proposition-level units
significantly outperform passage- and sentence-level dense retrieval. Naive fixed-size
chunking is the weakest baseline in all of this work.

`passthrough` (embed fragment or item content directly, no LLM) is included in v1 for
three reasons: it is the control arm of the quality harness, so the value of LLM
enrichment is measured rather than assumed; it is the economical choice for
high-volume low-value-per-item datatypes; and a one-implementation interface is not an
interface.

**Trade-offs.** LLM views cost money and lose verbatim detail. Where verbatim recall
matters, `granularity: fragment` embeds natural raw spans without inventing window
boundaries. A tempting shortcut is to concatenate a summary and raw content into one
truncated vector; that is deliberately avoided, because it dilutes the summary and
truncates the raw text, yielding the weaknesses of both. Two views is the correct
expression.

**Reference.**
- https://arxiv.org/abs/2401.18059
- https://www.anthropic.com/engineering/contextual-retrieval
- https://arxiv.org/abs/2312.06648

### D12. Reference resolution with reverse-dependency invalidation

**Decision.** A datatype may declare `references` that pull data from other datatypes
or external systems into its enrichment input. Each resolved reference records a
reverse edge in `refs(to_kind, to_key) → (from_datatype, from_item_id)`. When a
referenced record changes, the referencing item's dependent views are invalidated.
Resolved keys are written into a per-record metadata field.

```yaml
references:
  - name: linked_repo
    resolver: inget              # cross-record reference into inget's own state
    datatype: github/repo
    key_from: "column:repo_url"
    fields: [description, view:role]
    inject_as: fragment          # fragment | metadata
  - name: ticket
    resolver: http               # generic external resolver
    endpoint: "${TICKETS_BASE_URL}/api/tickets/{key}"
    key_from: "column:ticket_id"
    fields: [number, title, state]
    inject_as: metadata
metadata_fields:
  related_keys: "${references.*.resolved_keys}"
```

**Rationale.** Cross-record context is what makes enrichment more than
per-record summarization: a Monday item referencing a repository should be searchable
by what that repository does. But a reference is an input, so signature completeness
(D2) requires it to participate in the cache key — otherwise a changed referent
leaves a stale referencing view forever. The reverse index is the standard
demand-driven-invalidation structure that Salsa uses to know which queries to
re-validate.

**Trade-offs.** Reference edges can fan out and cause invalidation storms; a change to
a widely-referenced repo invalidates every item referencing it. Bounded by
`max_reference_depth` (default 2), cycle detection, and a per-run cap on cascaded
invalidations that logs and defers the remainder to the next run. Resolvers are
read-only and must be idempotent.

### D13. State in Postgres; no durable local state

**Decision.** A `StateStore` interface with two implementations: `postgres` (default,
schema `inget_state`) and `sqlite` (pure-Go `modernc.org/sqlite`, for offline
development and tests). Concurrency is guarded by a Postgres advisory lock per
datatype. Progress is checkpointed per item in a `work` table so an interrupted run
resumes rather than restarts.

**Rationale.** 12-factor factor VI requires processes to be stateless with all state
in an external backing service, and the stated requirement is that no state be
maintained outside external services. Kubernetes' CronJob documentation is explicit
that jobs "must be idempotent" because the controller may create zero or two jobs for
a given tick; the advisory lock plus `concurrencyPolicy: Forbid` covers both
directions. Per-item checkpointing plus SIGTERM handling means a pod evicted 80%
through a 500k-item run resumes at 80%, not 0%.

**Trade-offs.** Postgres becomes a hard dependency for any non-trivial use.
Acceptable, since pgvector already makes it one.

**Reference.**
- https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/
- https://12factor.net/processes

### D14. Retrieval quality is a measured gate, not an assumption

**Decision.** `inget eval` iterates every configured datatype and view, embeds a
sample, and scores five metrics, reporting **per datatype** rather than as one
aggregate:

| Metric | Measures | Default threshold |
|---|---|---|
| self-retrieval | a view's own text retrieves that item at rank 1 | ≥ 0.80 |
| distinctiveness | mean pairwise distance between different items | ≥ 0.05 |
| view coverage | fraction of configured views producing non-empty text | ≥ 0.90 |
| view distinctiveness | mean pairwise distance between views of one item | ≥ 0.05 |
| top-3 metadata retrieval | a metadata-derived query finds the item in top 3 | ≥ 0.60 |

Adding a datatype adds coverage with no test changes. `--embedder` overrides the
configured embedder so two models can be A/B compared on the real corpus.

**The harness embeds stored view text; it does not regenerate views.** `inget eval`
reads `state.views.text` — by definition the text each stored vector was produced
from — and re-embeds it. It never calls the generator.

This is forced by a provider property measured on 2026-08-02: generation is not
reproducible. DeepSeek V4 Flash returned materially different text (487 and 298
characters) for two byte-identical requests at `temperature: 0` with a fixed seed, and
Kimi K3 refuses `temperature: 0` altogether. Regenerating views per eval run would
therefore fold that variance into every metric, which defeats a threshold gate — a
score could cross the line because the wording moved, not because retrieval changed.
It would also invalidate the `--embedder` comparison the same decision depends on,
since generator noise would be confounded with the embedder being tested.

The cost is that eval no longer exercises generation. That is the correct division:
generation is covered by `inget run` and by the generator client's own guards, while
eval measures the embedding and retrieval behaviour it is named for, over a fixed
corpus, with the only variable being the embedder under test.

**Rationale.** MTEB deltas of a few points do not reliably transfer to a specific
domain, and per-datatype reporting matters because the embedder's contribution depends
on the enricher: with `llm`, the embedder sees clean prose and is not the bottleneck;
with `passthrough`, the embedder does the semantic work. Reporting one aggregate would
hide that.

### D15. Library selections

| Concern | Selection | Why |
|---|---|---|
| CLI | `spf13/cobra` | Required by Go conventions; subcommand tree fits the verb surface. |
| Config | `spf13/viper` | Required by Go conventions. Layered YAML + env. |
| Blob store | `gocloud.dev/blob` | One `*blob.Bucket` over `file://`, `s3://`, `gs://`; wraps aws-sdk-go-v2 and cloud.google.com/go/storage, so native performance with portable code and no per-backend code. |
| Postgres | `jackc/pgx/v5` | Pure Go, native protocol, `CopyFrom` implements binary COPY. |
| pgvector types | `pgvector/pgvector-go` | Official `vector`/`halfvec` codecs for pgx. |
| Migrations | `pressly/goose` | Embeddable as a library (not CLI-only), supports SQL and Go migrations. |
| Compression | `klauspost/compress/zstd` | Pure Go zstd; better ratio and speed than gzip for JSONL shards. |
| Concurrency | `golang.org/x/sync/errgroup` | `SetLimit` gives bounded worker pools with error propagation; no third-party pool needed. |
| Rate limiting | `golang.org/x/time/rate` | Token bucket; wrapped by an adaptive limiter that calls `SetLimit` from response headers. |
| Retry | `cenkalti/backoff` | Exponential backoff with full jitter, context-aware. |
| HTTP retry | `hashicorp/go-retryablehttp` | Transport-level retry for API clients. |
| SQLite | `modernc.org/sqlite` | Pure Go, no CGO, keeps static cross-compiled binaries. |
| Glob matching | `bmatcuk/doublestar/v4` | `path.Match` semantics plus `**`, which view dependency globs require (D3). Extending `path.Match` by hand is a known source of subtle mismatches, and wrong glob scoping corrupts the cascade in both directions. |
| Secret detection | `zricethezav/gitleaks/v8` | Scanning fetched content for committed credentials needs a maintained multi-provider ruleset with entropy checks, not a bespoke pattern list (see Security considerations). Module path retains the original `zricethezav` prefix although the repository moved. |
| Run identifiers | `oklog/ulid/v2` | Run IDs are ULIDs so that lexical ordering is chronological ordering, which is how readers resolve `latest`. Correct monotonic generation under a shared entropy source is not worth reimplementing. |
| Logging | stdlib `log/slog` | Structured JSON, no dependency. |
| Testing | stdlib `testing` | Per conventions; table-driven. |

Two related concerns deliberately stay in-tree.

**Log attribute redaction** (`internal/logging`) is key-pattern matching against this
codebase's own vocabulary, which is the opposite of a generic problem. Off-the-shelf
redactors treat `token`, `key` and `secret` as substrings and would therefore mask
`input_tokens`, `cache_key`, `related_keys` and `signature` — precisely the fields cost
accounting and the invalidation cascade exist to expose. The custom allowlist is the
entire job, so a dependency would add surface without removing work. This is distinct
from secret *detection* in fetched content, which is delegated to gitleaks above.

**Config and signature hashing** serializes sorted key/value pairs with length prefixes
rather than adopting canonical JSON (RFC 8785). The only Go JCS implementation is
unmaintained, and length prefixing sidesteps the float and unicode formatting ambiguity
that makes canonical JSON a poor foundation for a hash the whole cascade depends on.

Every dependency is pure Go, so binaries cross-compile statically for the Docker image
and Homebrew tap. Exact versions are pinned in `go.mod`. gitleaks reaches its regex
engine through a WASM runtime rather than CGO by default; step 9 must confirm the
default build stays CGO-free, and drop the dependency for an in-tree ruleset if it does
not.

---

## Data Model

### State schema (`inget_state`)

```sql
CREATE TABLE items (
    datatype      TEXT NOT NULL,
    item_id       TEXT NOT NULL,
    source_name   TEXT NOT NULL,
    fingerprint   TEXT NOT NULL,              -- level-0 token
    composed_hash TEXT,                       -- level-2 input (unscoped)
    metadata      JSONB NOT NULL DEFAULT '{}',
    last_run_id   TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ,
    PRIMARY KEY (datatype, item_id)
);

CREATE TABLE fragments (
    datatype     TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    frag_key     TEXT NOT NULL,
    fingerprint  TEXT NOT NULL,               -- level-1 token
    blob_ref     TEXT,                        -- sha256 in blob store
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    tier         SMALLINT NOT NULL DEFAULT 4,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    missing_runs SMALLINT NOT NULL DEFAULT 0, -- GC counter
    PRIMARY KEY (datatype, item_id, frag_key)
);

CREATE TABLE derivations (
    cache_key  TEXT PRIMARY KEY,              -- hash(frag fingerprint ‖ signature)
    datatype   TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    frag_key   TEXT NOT NULL,
    signature  TEXT NOT NULL,
    output     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_hit_at TIMESTAMPTZ
);
CREATE INDEX derivations_item_idx ON derivations (datatype, item_id, frag_key);

CREATE TABLE views (
    datatype      TEXT NOT NULL,
    item_id       TEXT NOT NULL,
    view_name     TEXT NOT NULL,
    input_hash    TEXT NOT NULL,              -- level-2 guard (scoped)
    text          TEXT NOT NULL,
    embedded_hash TEXT,                       -- level-3 guard
    model         TEXT,
    dims          SMALLINT,
    signature     TEXT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (datatype, item_id, view_name)
);

CREATE TABLE refs (
    from_datatype  TEXT NOT NULL,
    from_item_id   TEXT NOT NULL,
    ref_name       TEXT NOT NULL,
    to_kind        TEXT NOT NULL,             -- 'inget' | 'http' | ...
    to_key         TEXT NOT NULL,
    to_fingerprint TEXT,
    resolved_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (from_datatype, from_item_id, ref_name, to_key)
);
CREATE INDEX refs_reverse_idx ON refs (to_kind, to_key);  -- invalidation lookup

CREATE TABLE runs (
    run_id      TEXT PRIMARY KEY,             -- ULID
    binary      TEXT NOT NULL,                -- inget | inget-fetch
    datatype    TEXT,
    scope       TEXT NOT NULL,                -- full | partial
    status      TEXT NOT NULL,                -- running | ok | failed | interrupted
    config_hash TEXT NOT NULL,
    stats       JSONB NOT NULL DEFAULT '{}',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE work (                            -- resumable checkpointing
    run_id   TEXT NOT NULL,
    datatype TEXT NOT NULL,
    item_id  TEXT NOT NULL,
    status   TEXT NOT NULL,                   -- pending | claimed | done | failed
    attempts SMALLINT NOT NULL DEFAULT 0,
    error    TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, datatype, item_id)
);
CREATE INDEX work_pending_idx ON work (run_id, status);

CREATE TABLE signatures (                      -- mass-invalidation detection
    scope      TEXT PRIMARY KEY,              -- 'enricher:github/repo:file'
    signature  TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Work is claimed with `SELECT ... FOR UPDATE SKIP LOCKED` so multiple workers can share
a run without coordination.

### Destination schema (pgvector)

```sql
CREATE EXTENSION IF NOT EXISTS vector;        -- requires pgvector >= 0.8.0

CREATE TABLE inget_vectors (
    id           TEXT PRIMARY KEY,            -- sha256(datatype‖item_id‖view‖frag_key)
    datatype     TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    view_name    TEXT NOT NULL,
    granularity  TEXT NOT NULL DEFAULT 'item',-- item | fragment
    frag_key     TEXT,
    text         TEXT NOT NULL,
    embedding    halfvec(1024) NOT NULL,
    model        TEXT NOT NULL,
    dims         SMALLINT NOT NULL,
    signature    TEXT NOT NULL,
    metadata     JSONB NOT NULL DEFAULT '{}',
    related_keys TEXT[] NOT NULL DEFAULT '{}',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX inget_vectors_facet_idx   ON inget_vectors (datatype, view_name);
CREATE INDEX inget_vectors_item_idx    ON inget_vectors (datatype, item_id);
CREATE INDEX inget_vectors_meta_idx    ON inget_vectors USING gin (metadata);
CREATE INDEX inget_vectors_related_idx ON inget_vectors USING gin (related_keys);

-- Created after bulk load; pgvector documents index-after-load as faster.
CREATE INDEX inget_vectors_hnsw_idx ON inget_vectors
    USING hnsw (embedding halfvec_cosine_ops) WITH (m = 24, ef_construction = 200);

CREATE TABLE inget_model_registry (            -- enforces D7
    table_name TEXT PRIMARY KEY,
    model      TEXT NOT NULL,
    dims       SMALLINT NOT NULL,
    signature  TEXT NOT NULL,
    bound_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Query shape used by `inget query` and documented for consumers:

```sql
SET LOCAL hnsw.iterative_scan = relaxed_order;
SET LOCAL hnsw.ef_search = 100;
SELECT id, item_id, view_name, text, metadata,
       1 - (embedding <=> $1::halfvec(1024)) AS score
FROM inget_vectors
WHERE datatype = $2 AND ($3::text IS NULL OR view_name = $3)
ORDER BY embedding <=> $1::halfvec(1024)
LIMIT $4;
```

Bulk load uses `pgx.CopyFrom` into a staging table then
`INSERT … SELECT … ON CONFLICT (id) DO UPDATE`, because COPY does not support
`ON CONFLICT`. Incremental runs use batched `INSERT … ON CONFLICT`.

Index build tuning for a cold load: `maintenance_work_mem` 8–16 GB,
`max_parallel_maintenance_workers` = cores/2. Supabase measured a 1M×1536 HNSW build
dropping from 1h27m single-threaded to 9.5 min with 16 workers.

---

## Configuration

One root `config.yaml`, read identically by both binaries. Precedence, highest wins:
**environment variables → `config.local.yaml` → `config.yaml`**.

Environment nesting uses double underscores: `INGET_MODELS__GENERATOR__MODEL` maps to
`models.generator.model`. Keys are pre-registered from the decoded struct so viper's
`AutomaticEnv` can see them.

**Documented limitation.** Viper cannot address list elements by key, so
`sources[0].domain.orgs` is not overridable by environment variable. Scalar settings
are env-overridable; list-valued settings are overridden through the
`config.local.yaml` layer. This is stated rather than worked around, because a
synthetic index syntax would be a bespoke convention nobody expects.

Secrets never appear in config. Config names the environment variable holding a
secret (`token_env`, `api_key_env`, `dsn_env`) and the process reads it at startup.

```yaml
version: 1

log:
  level: info          # LOG_LEVEL: debug|info|warn|error
  format: json         # LOG_FORMAT: json|text
  destination: stderr  # LOG_DESTINATION: stderr|stdout

artifacts:
  url: "file://./.inget/artifacts"    # or s3://bucket/prefix, gs://bucket/prefix
  shard_target_bytes: 134217728        # 128 MiB uncompressed per JSONL shard
  blob_max_bytes: 1048576              # per-fragment content cap
  max_fragments_per_item: 2000         # excess is dropped, recorded, and warned
  compression: zstd

retention:
  runs: 30d                            # run directories kept before gc
  missing_runs: 3                      # runs a fragment may be absent before its
                                       # derivations are collected

enrich:
  max_reference_depth: 2                # reference resolution depth bound
  max_cascade_per_run: 5000             # cascaded invalidations; remainder deferred

eval:                                  # quality harness thresholds (D14)
  sample_size: 20                      # items with stored views sampled per datatype
  thresholds:                          # floors; a breach exits non-zero
    self_retrieval: 0.80
    distinctiveness: 0.05
    view_coverage: 0.90
    view_distinctiveness: 0.05
    metadata_top3: 0.60

state:
  driver: postgres                     # postgres | sqlite
  dsn_env: INGET_STATE_DSN
  # path: ./.inget/state.db            # sqlite only

models:
  generator:
    driver: openai
    base_url: "http://localhost:4000/v1"
    model: "deepseek-v4-flash"
    api_key_env: INGET_GENERATOR_API_KEY
    temperature: 0
    seed: 1
    max_output_tokens: 1024
    max_input_chars: 120000
    concurrency: 8
    timeout: 120s
    price_per_mtok_in: 0.14            # for `inget plan` cost estimates
    price_per_mtok_out: 0.28
  embedder:
    driver: openai
    base_url: "http://localhost:8080/v1"
    model: "Qwen/Qwen3-Embedding-0.6B"
    dimensions: 1024
    truncate_dims: 0                   # MRL target; 0 = native
    batch_size: 64
    concurrency: 4
    timeout: 60s

destinations:
  - name: local-pgvector
    driver: pgvector
    dsn_env: INGET_PGVECTOR_DSN
    table: inget_vectors
    storage: halfvec                   # halfvec | vector
    hnsw: { m: 24, ef_construction: 200 }
    ef_search: 100
    granularity: item                  # item | fragment
    batch_size: 500

sources:
  - name: github
    driver: github
    auth:
      token_env: INGET_GITHUB_TOKEN
      api_url: "https://api.github.com"
    domain:
      orgs: ["maxwell-cudlitz"]
      repos: []
      topics: []
      visibility: all                  # all | public | private
      include_archived: false
      include_forks: false
      max_inactive_days: 365
    limits:
      requests_per_second: 10
      max_concurrent: 4
      tarball_max_bytes: 33554432

  - name: monday
    driver: monday
    auth:
      token_env: INGET_MONDAY_TOKEN
      api_url: "https://api.monday.com/v2"
      api_version: "2026-07"
    domain:
      workspaces: []
      boards: [1234567890]
      include_updates: true
      include_subitems: false
    limits:
      complexity_reserve: 250000       # keep this much budget in hand
      max_concurrent: 8
      page_size: 500

datatypes:
  - name: github/repo
    source: github
    enricher: llm
    fragment_enricher:
      enabled: true
      prompt: prompts/github/repo/fragment.tmpl
      max_input_chars: 8000
    compose:
      order: tier                      # tier | path
      max_chars: 120000
    destinations: [local-pgvector]
    views:
      - name: role
        prompt: prompts/github/repo/role.tmpl
        depends_on: ["**"]
      - name: surface
        prompt: prompts/github/repo/surface.tmpl
        depends_on: ["cmd/**","**/api/**","**/routes/**","**/handlers/**",
                     "*.proto","**/*.graphql","openapi*","swagger*",
                     "**/main.*","**/index.*","README*"]
      - name: internals
        prompt: prompts/github/repo/internals.tmpl
        depends_on: ["**"]
      - name: stack
        prompt: prompts/github/repo/stack.tmpl
        depends_on: ["go.mod","go.sum","package.json","pyproject.toml",
                     "requirements.txt","Cargo.toml","pom.xml","build.gradle*",
                     "*.tf","Dockerfile*","docker-compose*","Makefile"]
      - name: integrations
        prompt: prompts/github/repo/integrations.tmpl
        depends_on: ["*.tf","helm/**","charts/**","k8s/**",
                     ".github/workflows/**","**/client*","**/config*"]
      - name: stewardship
        prompt: prompts/github/repo/stewardship.tmpl
        depends_on: ["CODEOWNERS",".github/**","docs/**","README*","AGENTS.md"]
      - name: operations
        prompt: prompts/github/repo/operations.tmpl
        depends_on: ["Dockerfile*","docker-compose*","Makefile","helm/**",
                     "k8s/**",".github/workflows/**","config*","deploy/**"]
      - name: aliases
        prompt: prompts/github/repo/aliases.tmpl
        depends_on: ["**"]

  - name: monday/item
    source: monday
    enricher: llm
    fragment_enricher:
      enabled: false                   # columns are already short; no per-column LLM
    destinations: [local-pgvector]
    references:
      - name: linked_repo
        resolver: inget
        datatype: github/repo
        key_from: "column:repo_url"
        fields: [description, "view:role"]
        inject_as: fragment
    metadata_fields:
      related_keys: "${references.*.resolved_keys}"
    views:
      - name: substance
        prompt: prompts/monday/item/substance.tmpl
        depends_on: ["**"]
      - name: progress
        prompt: prompts/monday/item/progress.tmpl
        depends_on: ["column:status","column:date*","column:person*","update:*"]
```

---

## API / Interface

Interfaces are defined by their consumers, in the package that uses them.

```go
// ── inget-fetch: source connectors ───────────────────────────────────────────

// Ref identifies a source item plus an optional cheap change token.
// An empty Fingerprint means "unknown"; the item is always fetched.
type Ref struct {
    ID          string
    Fingerprint string
}

// Fragment is an addressable sub-unit of an item.
// Key is stable within the item: "cmd/root.go", "column:status", "update:8891".
type Fragment struct {
    Key         string
    Fingerprint string
    Content     []byte
    Tier        int               // composition priority
    Meta        map[string]string
}

// Item is the identity and metadata of one record.
type Item struct {
    ID          string
    Fingerprint string
    Metadata    map[string]string
}

// Connector enumerates and fetches items for one source.
// List streams refs so a 500k-item domain is never materialized.
type Connector interface {
    Name() string
    Datatypes() []string
    List(ctx context.Context, datatype string, q ListQuery, yield func(Ref) error) error
    Fetch(ctx context.Context, datatype string, ref Ref) (Item, []Fragment, error)
}

// ListQuery scopes enumeration for partial / webhook-driven runs.
type ListQuery struct {
    Only  []string   // explicit item IDs; implies scope=partial
    Since time.Time  // source-side filter where supported
    Limit int
}

// ── shared ───────────────────────────────────────────────────────────────────

// BlobStore is the content-addressed store plus run directories.
type BlobStore interface {
    PutBlob(ctx context.Context, sha string, r io.Reader) error
    HasBlob(ctx context.Context, sha string) (bool, error)
    GetBlob(ctx context.Context, sha string) (io.ReadCloser, error)
    PutObject(ctx context.Context, key string, r io.Reader) error
    GetObject(ctx context.Context, key string) (io.ReadCloser, error)
    List(ctx context.Context, prefix string) ([]string, error)
}

// StateStore persists every guard level. Implementations: postgres, sqlite.
type StateStore interface {
    Lock(ctx context.Context, key string) (release func() error, err error)

    ItemFingerprints(ctx context.Context, datatype string) (map[string]string, error)
    PutItem(ctx context.Context, datatype string, it Item, composedHash string) error
    Tombstone(ctx context.Context, datatype string, ids []string) error

    Fragments(ctx context.Context, datatype, itemID string) (map[string]FragmentState, error)
    PutFragments(ctx context.Context, datatype, itemID string, f []FragmentState) error

    Derivation(ctx context.Context, cacheKey string) (string, bool, error)
    PutDerivation(ctx context.Context, d Derivation) error

    ViewState(ctx context.Context, datatype, itemID string) (map[string]ViewState, error)
    PutViewState(ctx context.Context, datatype, itemID string, v ViewState) error

    PutRefs(ctx context.Context, from RefEdgeSource, edges []RefEdge) error
    ReferencedBy(ctx context.Context, kind, key string) ([]ItemKey, error)

    Signature(ctx context.Context, scope string) (string, error)
    PutSignature(ctx context.Context, scope, signature string) error

    StartRun(ctx context.Context, r Run) error
    FinishRun(ctx context.Context, runID, status string, stats any) error
    EnqueueWork(ctx context.Context, runID, datatype string, ids []string) error
    ClaimWork(ctx context.Context, runID string, n int) ([]string, error)
    CompleteWork(ctx context.Context, runID, datatype, itemID string, err error) error
}

// ── inget: enrichment ────────────────────────────────────────────────────────

// Delta is the reconciliation of incoming fragments against persisted state.
type Delta struct {
    Added, Modified, Unchanged []Fragment
    Deleted                    []string
}

// FragmentEnricher derives a small artifact from one fragment.
// Signature must cover model, prompt bytes, and limits.
type FragmentEnricher interface {
    Derive(ctx context.Context, f Fragment) (string, error)
    Signature() string
}

// Composer deterministically reduces fragment derivations to a document.
// Scope restricts input to fragments matching a view's depends_on globs.
type Composer interface {
    Compose(it Item, derived map[string]string, scope []string) (string, error)
}

// Enricher turns one item plus its delta into named views and metadata.
type Enricher interface {
    Name() string
    Signature() string
    Enrich(ctx context.Context, in EnrichInput) (EnrichOutput, error)
}

type EnrichInput struct {
    Item       Item
    Delta      Delta
    Derived    map[string]string   // frag key → cached or fresh derivation
    References map[string][]Record // resolved reference payloads
    Prior      map[string]ViewState
}

type EnrichOutput struct {
    Views       map[string]string   // view name → generated text
    Metadata    map[string]string   // additions, incl. related_keys
    Derivations []Derivation        // newly computed, for caching
    Skipped     map[string]string   // view name → reason (guard hit)
}

// Resolver fetches a referenced record by key.
type Resolver interface {
    Name() string
    Resolve(ctx context.Context, key string, fields []string) (Record, error)
}

// ── inget: models and destinations ───────────────────────────────────────────

type Generator interface {
    Generate(ctx context.Context, prompt string) (text string, usage Usage, err error)
    Signature() string
}

type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Model() string
    Dims() int
    Signature() string
}

type Destination interface {
    Name() string
    Migrate(ctx context.Context) error
    AssertModel(ctx context.Context, model string, dims int, sig string) error
    Upsert(ctx context.Context, batch []Vector) error
    DeleteItem(ctx context.Context, datatype, itemID string) error
    Count(ctx context.Context, datatype, view string) (int64, error)
    Search(ctx context.Context, q []float32, o SearchOpts) ([]Hit, error)
}
```

### CLI surface

```
inget-fetch [flags]
  --source NAME          restrict to one source (default: all)
  --datatype NAME        restrict to one datatype (default: all for source)
  --only ID[,ID...]      explicit item IDs; forces --scope partial
  --event-file PATH      webhook payload; extracts item IDs, forces partial
  --since TIMESTAMP      source-side filter where supported
  --scope full|partial   default full; partial suppresses tombstones
  --limit N              cap items fetched (smoke tests)
  --dry-run              enumerate and report; write nothing

inget run [flags]
  --datatype NAME        restrict to one datatype
  --from RUN_ID|latest   artifact run to consume (default: latest committed)
  --views a,b            restrict view generation
  --only ID[,ID...]      restrict to specific items
  --force LEVEL          fragments|views|embeddings|all — bypass guards
  --limit N
  --dry-run

inget plan [--only ID] [--limit N] [--estimate-profile PATH]  work and cost estimate
inget migrate [--destination NAME] create/upgrade state and destination schemas
inget reindex [--datatype] [--views] [--destination]
inget state show | gc | unlock
inget query "text" [--datatype] [--view] [-k N]    verification only
inget eval [--datatype] [--embedder ...]           quality harness (D14)
inget version
```

`inget plan` reports distinct uncached fragment derivations, view generations, prompt
input allowances, maximum output tokens, and cost from `price_per_mtok_*`. Signature
changes are surfaced before spending. An optional measured profile adds approximate
`estimate.expected` generation cost; see [cost-estimates.md](cost-estimates.md).

---

## Source Connectors

### `github` / `github/repo`

Native `net/http` against the GitHub API with a token from the environment. No
dependency on the `gh` CLI.

- **Enumerate**: `GET /orgs/{org}/repos?per_page=100&type=all` paginated, plus explicit
  `repos:` and `topics:` (search API) entries. Filters: archived, forks, visibility,
  `pushed_at` older than `max_inactive_days`.
- **Level-0 fingerprint**: `pushed_at`. Unchanged means no fetch at all.
- **Fragment fingerprints**: `GET /repos/{o}/{r}/git/trees/{branch}?recursive=1` yields
  a blob SHA per path — an exact content hash for free, without downloading content.
- **Content**: `GET /repos/{o}/{r}/tarball/{branch}` streamed once per repo, extracted
  in memory. Only fragments whose blob SHA is absent from state need their content
  retained; the rest are recorded as unchanged and their blobs are not re-uploaded.
- **Filtering**: skip binary extensions, dependency and build
  directories, lockfiles, test and mock paths; classify surviving files into tiers
  (docs → entrypoints → config → source → other) used for composition order.
- **Oversized files** are split into sub-file fragments (`path#0`, `path#1`), each
  independently fingerprinted, so incrementality survives below file granularity.
- **Rate limits**: honor `x-ratelimit-remaining` / `x-ratelimit-reset`, plus secondary
  rate limits (`retry-after`). Conditional requests with `ETag` on metadata endpoints.

### `monday` / `monday/item`

GraphQL against `https://api.monday.com/v2` with `Authorization: <token>` (no
`Bearer` prefix) and a pinned `API-Version: 2026-07`. Required scopes: `boards:read`,
`updates:read`.

- **Enumerate**: `boards(limit, page)` for offset pagination over the account or
  configured workspaces, then per board `items_page(limit: 500)` returning an opaque
  cursor, continued via the root-level `next_items_page(cursor:)` until `cursor` is
  null. Cursors expire 60 minutes after the initial `items_page` call, so enumeration
  of a very large board must checkpoint and restart pagination rather than hold a
  cursor across a long run.
- **Level-0 fingerprint**: item `updated_at`.
- **Fragments**: one per column value (`column:<id>`, fingerprinted by a hash of the
  raw `value` JSON), plus one per update/comment (`update:<id>`, fingerprinted by
  `updated_at`) when `include_updates` is set. Board column schema
  (`columns { id title type settings_str }`) is fetched once per board and cached, so
  column titles can be composed into readable text.
- `text` versus `value`: `value` (raw JSON) is authoritative and is what gets
  fingerprinted; `text` is the human-readable rendering and is what gets composed into
  prompts. Columns where `text` is empty fall back to typed fragments.
- **Rate limiting** is complexity-metered, not request-counted. The limiter is
  reactive rather than predictive: every response carries IETF headers

  ```
  RateLimit-Policy: "minuteRate";q=5000;w=60, "concurrency";q=250, "complexityMinute";q=5000000;w=60
  RateLimit: "minuteRate";r=4999, "concurrency";r=249, "complexityMinute";r=4950000;t=45
  ```

  where `r` is remaining and `t` is seconds to reset. The adaptive limiter parses these
  and calls `rate.Limiter.SetLimit`; when remaining complexity drops below
  `complexity_reserve` it sleeps `t` seconds. Queries also request the `complexity`
  block (`before`, `query`, `after`, `reset_in_x_seconds`) so cost per query shape is
  learned and logged. Distinct 429 codes are handled separately:
  `COMPLEXITY_BUDGET_EXHAUSTED`, `maxConcurrencyExceeded`, `DAILY_LIMIT_EXCEEDED`,
  `IP_RATE_LIMIT_EXCEEDED`; all honor `Retry-After`. A single query may not exceed 5M
  complexity, so page size and field selection are bounded at build time.
- **Webhooks** are out of scope for these binaries (both are one-shot), but the
  `--event-file` flag consumes a webhook payload — extracting `event.pulseId` — so a
  trivial external receiver can trigger a partial run. Monday's webhook URL
  verification (echo the `challenge` field) belongs to that receiver.
- **Deletions** are detected by `full` runs. `activity_logs` are available as a change
  feed with roughly 90 days of retention and could later drive cheaper incremental
  enumeration; not used in v1.

---

## Enrichment Pipeline

Per item, in order. Each stage may short-circuit.

1. **Read** the item record from the artifact shard; fragment content is fetched from
   the blob store only when needed by a later stage.
2. **Reconcile** incoming fragments against `fragments` state → `Delta`.
3. **Resolve references** (D12): look up declared referents, record reverse edges,
   collect `related_keys`. A changed referent marks dependent views for regeneration.
4. **Derive** per-fragment artifacts for `Added` and `Modified` fragments only;
   `Unchanged` fragments read from `derivations` by cache key. Bounded concurrency via
   `errgroup.SetLimit(models.generator.concurrency)`.
5. **Compose**, per view, over only the fragments matching that view's `depends_on`
   globs, ordered by tier then path, truncated to `compose.max_chars`. Hash the result.
6. **Generate views** whose scoped composed hash or signature differs from
   `views.input_hash`. Others are skipped and reported in `EnrichOutput.Skipped`.
7. **Annotate metadata**: static and templated fields plus `related_keys`.
8. **Embed** views whose text changed from the text behind the stored vector.
9. **Upsert** to each configured destination in batches; delete vectors for tombstoned
   items and removed fragments.
10. **Checkpoint** the item in `work` and persist all guard state in one transaction.

Prompts are Go `text/template` files under `prompts/<source>/<datatype>/`, never
inlined in code. Template data receives item metadata, the composed document, resolved
references, and the view name.

---

## Edge Cases & Failure Modes

| Failure | Handling |
|---|---|
| Partial run mistaken for mass deletion | `scope` flag in the manifest (D6); tombstones only from `full` runs. |
| Crashed fetch leaves half a run | `_COMMIT` written last; readers skip uncommitted runs; `state gc` reclaims orphan blobs. |
| Monday cursor expires mid-enumeration (60 min) | Checkpoint board progress; restart pagination from the board rather than failing the run. |
| Monday complexity budget exhausted | Reactive limiter reads `RateLimit` header, sleeps `t`; distinct handling per 429 code; honors `Retry-After`. |
| GitHub secondary rate limit | Backoff with jitter on `retry-after`; concurrency capped by `limits.max_concurrent`. |
| Prompt or model change silently reuses stale cache | Signature completeness (D2); mismatch invalidates and logs a `WARN` with affected count. |
| LLM non-determinism causes pointless re-embeds | `temperature: 0`, seed; non-determinism confirmed (DeepSeek V4 Flash, 2026-08-02); the `embedded_hash` identity check is the backstop (D4). |
| Embedding model changed under an existing index | `inget_model_registry` rejects the write with instructions to run `reindex` (D7). |
| Vector width mismatch | Enforced by the `halfvec(1024)` column type and `AssertModel`. |
| Concurrent runs corrupt state | Postgres advisory lock per datatype; `concurrencyPolicy: Forbid` in the CronJob. |
| Pod evicted mid-run | SIGTERM stops new claims, finishes in-flight items, marks the run `interrupted`; per-item `work` rows let the next run resume. |
| Reference cycles | `max_reference_depth` (default 2) plus visited-set cycle detection. |
| Invalidation storm from a widely-referenced record | Per-run cap on cascaded invalidations; remainder deferred to the next run and logged. |
| Item deleted then recreated with the same ID | Fingerprint mismatch forces full re-derivation; `deleted_at` cleared. |
| Oversized item or fragment | `blob_max_bytes`, `compose.max_chars`, and `max_fragments_per_item` (default 2000) cap it; truncation is recorded in the record and logged. |
| Fragment absent for several runs | `missing_runs` counter; derivations dropped after a grace period by `state gc`. |
| Blob store eventual consistency | Content addressing makes reads idempotent; `HasBlob` false negatives only cost a redundant upload. |
| Clock skew on `updated_at` fingerprints | Fingerprints are compared for inequality, never ordered; a skewed timestamp causes a redundant fetch, never a skipped change. |
| Cost blowout | `inget plan` reports estimated tokens and cost before any spend; full signature rebuilds require `--rebuild-on-signature-change`. |

### Security considerations

- **Prompt injection from ingested content.** Repository files and Monday comments are
  untrusted input that flows into LLM prompts. Content is wrapped in explicit
  delimiters, prompts instruct the model to treat it as data, enrichment output is
  never interpreted as instructions and drives no tool calls, and output is validated
  for shape and length before storage. This is mitigation, not elimination; it is
  documented as a residual risk.
- **Secrets in fetched content.** Repository tarballs can contain credentials.
  Artifacts must live in a private bucket with encryption at rest; the noise filter
  drops common secret-bearing paths, and a configurable `redact` pattern list can
  exclude more. Artifacts are not a safe place for public sharing.
- **Credential handling.** Secrets are read only from environment variables named by
  config. Config dumps and logs redact any value whose key matches a secret pattern.
  `inget` holds no source credentials at all.
- **Network exposure.** Neither binary listens on a socket. There is no server and
  therefore no authentication surface. A future webhook receiver would introduce one
  and must be designed with authentication from the start.

---

## Observability

`log/slog` with a JSON handler, one line per event, including `run_id`, `datatype`,
`item_id`, stage, duration, and guard outcome. `LOG_LEVEL` and `LOG_FORMAT=text`
control verbosity and human-readability.

**Destination: stderr by default**, configurable to stdout via
`log.destination` / `LOG_DESTINATION`. This deviates from the request for stdout, for
one reason: both binaries emit machine-readable data on stdout (`inget plan --json`,
`inget query --json`, `--dry-run` reports), and interleaving logs would corrupt that
for any Unix-style consumer. Repository conventions also specify stdout for program
output and stderr for diagnostics. Setting `LOG_DESTINATION=stdout` restores the
requested behavior in one variable when nothing is piping stdout.

Per-run summary written to `runs.stats` and logged at completion: items seen, skipped
per guard level, derivations computed versus cached, view generations, embeddings,
upserts, deletes, tokens in/out, estimated cost, wall time per stage. No metrics
endpoint and no tracing in v1.

---

## Repository Layout

```
inget/
├── cmd/
│   ├── inget/main.go
│   └── inget-fetch/main.go
├── internal/
│   ├── config/        loading, precedence, validation, secret indirection
│   ├── logging/       slog setup, redaction
│   ├── artifact/      envelope schema, manifest, shard reader/writer, blob store
│   ├── source/        connector registry
│   │   ├── github/
│   │   └── monday/
│   ├── delta/         reconciliation, hashing, signatures, glob scoping
│   ├── state/         StateStore interface, postgres, sqlite
│   ├── enrich/        pipeline stages, llm + passthrough enrichers, composer, refs
│   ├── model/         generator + embedder clients (OpenAI-compatible)
│   ├── destination/   registry, pgvector
│   ├── ratelimit/     adaptive limiter, header parsers
│   └── pipeline/      orchestration, worker pool, checkpointing, signals
├── migrations/        goose SQL migrations (state + destination)
├── prompts/           Go text/template prompt files per datatype
├── docs/              feature-design.md, artifact-envelope.md, implementation-plan.md
├── deploy/            docker-compose (Postgres+pgvector, TEI), k8s CronJob examples
├── config.yaml
├── Makefile
├── AGENTS.md
└── README.md
```

Source files stay under 250 lines; packages stay single-purpose. Note this follows the
Go convention of `cmd/` plus `internal/` rather than the generic `src/` layout.

---

## Testing Strategy

**Unit tests** (stdlib `testing`, table-driven) cover every branching function:
delta reconciliation across added/modified/unchanged/deleted permutations; signature
computation completeness; glob scoping; config precedence including env nesting;
envelope round-trip; adaptive limiter header parsing;
tier classification and noise filtering.

**Fakes, not mocks.** A deterministic `Generator` returning a hash-derived string and
an `Embedder` returning a hash-derived unit vector let the whole pipeline run in CI
with no API keys, no GPU, and no network. This is what makes cascade behavior testable:
assert that a one-fragment change produces exactly one derivation, two view
generations, and two upserts.

**Integration tests**, gated on an environment variable, run against
`deploy/docker-compose.yaml` (Postgres + pgvector, TEI) and cover migrations, HNSW
search, `ON CONFLICT` upsert, model-registry rejection, and resumability after
simulated SIGTERM.

**Quality harness** (`inget eval`, D14) is the acceptance gate for any new datatype or
prompt change, reported per datatype, with `--embedder` for A/B comparison.

---

## Non-Goals for v1

- An application-facing search service or any HTTP server. `inget query` remains an
  operator verification tool; optional OpenAI-compatible LLM reranking is an authorized
  CLI extension (2026-10-07), documented below.
- The `splitter` enricher. Deferred until a datatype has genuinely unstructured long
  documents, and it should then be contextualized chunking rather than naive splitting.
- Webhook receiver. `--event-file` is the integration point for an external one.
- Destinations beyond pgvector. The interface exists; Qdrant is the expected second.
- Metrics and tracing.
- Datatypes beyond `github/repo` and `monday/item`.

## Query reranking extension (2026-10-07)

The optional `query.rerank` block controls CLI retrieval only: enabled (default false),
50 view candidates per datatype, 200 Unicode characters per candidate preview, and
`prompts/query/rerank.tmpl`. `models.reranker` is an independent optional Generator
configuration using the same D10 OpenAI-compatible transport. It must be complete when
enabled or declared; it never inherits or changes indexing generator parameters.

`--rerank` overrides the enable flag, including `--rerank=false`. Retrieval requests
`max(limit, candidates)` rows per datatype, preserves D7 model assertions, merges by
vector similarity and groups by datatype/item, retaining the best view. At most
`max(limit, candidates)` grouped items are ranked. Pools smaller than two items need no
ranking request; larger pools are ranked even when they fit within the output limit.
With ranking enabled, `max(limit, candidates)` must not exceed 1000. Identity and
best-view text form a bounded preview; a disk prompt encloses the JSON-escaped query and previews as untrusted data.
Query prompts are the exception to the per-source prompt layout because their input
spans datatypes; they are not cached indexing derivations.

The chat response must be one JSON object containing a complete permutation of the
supplied one-based IDs under `ranking`. Only order changes: vector scores are retained
and a successful ranking adds `rank` to output. Runtime ranking failures emit a stderr
warning and return grouped vector order; cancellation of the command propagates.
Configuration/prompt errors fail before spending on query embedding. The reranker
timeout covers the entire operation, including retries. No state or vector writes are
performed, and no indexing signature or artifact hash includes query configuration.
The D14 evaluator continues to evaluate vector retrieval, not query-time ranking.


## Terminal progress extension, 2026-10-07

The CLI may display ephemeral human progress on stderr in addition to structured diagnostic
logs. `--progress auto` is the default and activates only on a terminal; `plain` emits periodic
non-ANSI snapshots and `off` preserves existing output. Redirected stderr receives the existing
log stream in auto mode. Program data remains on stdout. This explicitly extends Observability
without adding a service, persisted metrics, configuration fields, or cache/signature inputs.

`internal/progress` provides a context observer shared by fetch and ingestion. With no observer,
events are no-ops. Concurrent event updates and normal redacted diagnostic writes are serialized
by the optional display; model or source content and credentials never enter progress events.
Counts record observed outcomes, not planned spend. Fetch totals remain unknown until listing
ends. Ingestion resets counters per artifact and keeps actual resumed completion counts. Output
failure cannot change ingestion results. Lifecycle completion, failure, interruption and
cancellation leave an honest final display, and the command closes its refresh goroutine.

The existing pinned pure-Go `mattn/go-isatty` v0.0.23 and `golang.org/x/sys` v0.47.0 dependencies
are promoted to direct use for terminal detection and dimensions, extending D15 for this
optional display. Rendering, events, synchronization and timing use the standard library.
