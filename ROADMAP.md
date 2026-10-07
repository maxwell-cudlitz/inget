# Roadmap

## Query API (planned)

A third application is needed to serve as the production query API. `inget query` exists as an operator tool for verifying index contents, but it is not an application-facing service.

The query API should:

- Accept natural-language queries and return ranked results
- Reuse the optional OpenAI-compatible LLM reranker implemented in `inget query`, or
  add a dedicated cross-encoder adapter when retrieval evaluation supports it
- Expose filtering by datatype, view, and metadata
- Be deployable independently of the indexing pipeline

The indexing pipeline (`inget-fetch`, `inget run`) and the quality gate (`inget eval`) remain separate concerns from serving queries at request time.

Optional CLI reranking is implemented; the independent query service and its metadata
filtering remain planned.

## Blob eviction after enrichment (planned, security)

Raw source content is currently retained in the artifact blob store for re-enrichment without re-fetching. This means proprietary code from private repositories sits on disk (or in S3) in compressed plaintext, bounded only by GC retention (default 30 days).

The secret scanner (`gitleaks`) catches known patterns, but custom secrets, internal URLs, and the source code itself remain exposed.

Planned: a blob eviction mode that deletes raw content once all fragment derivations and views are committed. Tradeoffs:

- **Pro:** No persistent storage of raw source code at rest. Eliminates the main data-at-rest exposure.
- **Con:** Prompt or model changes require a full re-fetch from the source (rate-limited, slow for large corpora).

This should be a per-source or per-datatype config flag (e.g., `retention.evict_blobs_after_enrichment: true`) so that operators can opt in for sensitive sources while keeping blobs for repos where iteration speed matters more than secrecy.

Until implemented, mitigations:
- Shorten `retention.runs` to minimize the exposure window
- Encrypt the artifact store at the infrastructure layer (S3 SSE-KMS, encrypted volumes)
- Restrict access to the artifact path/bucket

## Multi-granularity summaries (planned, retrieval quality)

Generate both a brief and a detailed summary per fragment or view, embedding each at different granularities. Research suggests that multi-scale representations improve retrieval by matching queries of varying specificity — a short query ("websocket library") matches the brief summary better, while a detailed query ("handles reconnection with exponential backoff and jitter") matches the longer one.

Implementation options:

- Two fragment derivation prompts per file: a one-line description and the current full summary
- Two view variants per view: a concise abstract (2–3 sentences) and the existing detailed generation
- Each granularity gets its own embedding and row in the destination, searchable independently or merged at query time

This directly addresses the self-retrieval weakness on narrow views (`stack`, `operations`) where short, generic summaries across repos embed too similarly. A brief summary captures *what* while a detailed one captures *how*, giving the embedder two chances to separate items.
