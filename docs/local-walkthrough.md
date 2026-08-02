# Walkthrough: 50 public repositories on a laptop, front to back

A complete local run against real data: bring up the compose stack, choose 50 popular but
reasonably sized public repositories, fetch them, enrich and embed them, then query the index —
first with `inget query`, then with `curl` and `psql` against the two localhost services the
stack exposes. Nothing here edits a repository file except, optionally, the no-spend variant at
the end.

**Before you start.** You need Docker with compose, `jq`, `curl`, a GitHub token, and an
OpenAI-compatible generator endpoint with its key. The token is required even for public
repositories: the API allows 60 requests an hour without one and 5000 with, and a token with no
scopes is enough. `config.yaml` expects the generator at `http://localhost:4000/v1`;
`config.local.yaml` is where you point it somewhere real. Run everything from the repository
root, because `config.yaml` names prompt templates by relative path and they are read from disk
rather than embedded.

**What this costs.** `inget run` calls a generator, and with `fragment_enricher.enabled: true`
the call count scales with the retained files across all 50 repositories rather than with the
number of repositories. Step 6 is `inget plan`, which prices it before anything is spent. Start
with `--limit 3` at step 5, read the estimate, then scale up.

---

## 1. The stack

TEI's CPU images are per-architecture and not multi-arch, so the tag has to match the machine
before anything starts — and because a failed pull aborts the whole `up`, getting it wrong
leaves postgres down as well:

```bash
case "$(uname -m)" in
  arm64|aarch64) export TEI_IMAGE_TAG=cpu-arm64-latest ;;   # no version-pinned arm64 tag exists
  *)             export TEI_IMAGE_TAG=cpu-1.9 ;;
esac
docker compose -f deploy/docker-compose.yaml up -d
docker compose -f deploy/docker-compose.yaml ps
```

Two services: `postgres` (pgvector 0.8+, holding both the `inget_state` schema and the vector
table) on `127.0.0.1:5432`, and `embedder` (Text Embeddings Inference serving
Qwen3-Embedding-0.6B) on `127.0.0.1:8080`. The first start downloads about 1.2 GB of weights;
wait for health before continuing:

```bash
curl -fsS http://localhost:8080/health && echo ok
curl -fsS http://localhost:8080/info | jq .        # the model it loaded and its input limit
```

On Apple Silicon, prefer the native build over that container: TEI has no Metal or MPS access
inside Docker and embeds on CPU cores only.

```bash
brew install text-embeddings-inference
text-embeddings-router --model-id Qwen/Qwen3-Embedding-0.6B --port 8090 --hostname 127.0.0.1
docker compose -f deploy/docker-compose.yaml up -d postgres      # then only postgres is needed
export INGET_MODELS__EMBEDDER__BASE_URL=http://127.0.0.1:8090/v1
```

If something already holds port 8080, set `TEI_HOST_PORT` and point the embedder's base URL at
the new port the same way.

## 2. Environment

```bash
export INGET_STATE_DSN='postgres://inget:inget@localhost:5432/inget?sslmode=disable'
export INGET_PGVECTOR_DSN="$INGET_STATE_DSN"
export INGET_GITHUB_TOKEN=ghp_...              # any token; no scopes needed for public data
export INGET_GENERATOR_API_KEY=sk-...          # whatever your generator endpoint wants
export LOG_FORMAT=text                         # easier to read than JSON while watching
```

`INGET_*_DSN` and `INGET_GENERATOR_API_KEY` hold values that configuration points at through
its `*_env` fields. Anything else prefixed `INGET_` overrides a config key, with `__` for
nesting: `INGET_MODELS__EMBEDDER__BASE_URL` sets `models.embedder.base_url`.

## 3. Schemas

```bash
make build
./bin/inget migrate
```

Creates the `inget_state` schema, the `inget_vectors` table with its HNSW index, and
`inget_model_registry`. Idempotent, so it is safe to re-run.

## 4. Choose the 50 repositories

The connector enumerates organisations, explicit `owner/name` entries, or topic searches — it
has no "most popular" mode, and `GET /search/repositories` with a `sort` is not something it
builds. So pick the list with `curl` and hand it over explicitly. Ten of the most-starred
repositories in each of five languages, each under 20 MB checked out:

```bash
for lang in go python typescript rust java; do
  curl -fsS -H 'Accept: application/vnd.github+json' \
       -H "Authorization: Bearer $INGET_GITHUB_TOKEN" \
       "https://api.github.com/search/repositories?q=stars:%3E5000+size:%3C20000+archived:false+language:$lang&sort=stars&order=desc&per_page=10" \
    | jq -r '.items[].full_name'
  sleep 2                       # the search endpoint is rate limited separately
done | tee /tmp/repos.txt | wc -l
```

`size:<20000` is the point of this query rather than decoration: `size` is the checkout size
in kilobytes, so the bound keeps every archive well inside `limits.tarball_max_bytes` (32 MiB).
Exceeding that cap is not an error — extraction stops there and the item is indexed partially
with a truncation warning — but a corpus of half-read repositories is a poor thing to measure
retrieval against. The per-language split is the other half: sorting by stars alone fills the
first page with curated link lists (`awesome-*`, `build-your-own-x`, `public-apis`) rather than
software, and views like `stack`, `surface` and `operations` have nothing to read in those.

What comes back is 50 real codebases across five languages, the largest about 20 MB checked
out — `gin-gonic/gin`, `junegunn/fzf`, `nektos/act`, `BurntSushi/ripgrep`,
`alacritty/alacritty`, `pmndrs/zustand`, `slab/quill`, `spf13/cobra`, `go-gorm/gorm`. Then:

```bash
export REPOS=$(paste -sd, - < /tmp/repos.txt)
```

## 5. Fetch

```bash
./bin/inget-fetch --source github --datatype github/repo --only "$REPOS" --limit 3 --dry-run
./bin/inget-fetch --source github --datatype github/repo --only "$REPOS" --limit 3
```

Start with three. When that looks right, drop `--limit`:

```bash
./bin/inget-fetch --source github --datatype github/repo --only "$REPOS" \
  | jq '{items, items_skipped_unchanged, items_failed, fragments, blobs_written, blobs_reused, warnings}'
```

One tree request and one tarball per repository, not one request per file. The run lands under
`.inget/artifacts/runs/github/github_repo/` as a ULID directory finished by a `_COMMIT` marker,
and a directory without that marker is invisible to every consumer. `--only` marks the run
partial, so no tombstones are issued — an item the run never looked at is not an item that was
deleted — and it bypasses the domain filters, which is why `archived:false` above is doing that
filtering instead. Two limits shape what lands: `blob_max_bytes` (1 MiB) drops individual
oversized files and `max_fragments_per_item` (2000) caps one repository's contribution,
recording the excess as a warning rather than failing.

```bash
ls .inget/artifacts/runs/github/github_repo/       # run directories, newest sorts last
du -sh .inget/artifacts                            # blobs are shared and written once
```

## 6. Price it before spending

```bash
./bin/inget plan github/repo | jq '{total_items, added, modified, unchanged, estimate}'
```

`plan` opens no destination, takes no lock and calls no model. `estimate.cost_usd` is
computed from the `price_per_mtok_*` figures in `config.yaml`, and `estimate.upper_bound` is
true because the level-2 guard can still skip a view whose scoped composition turns out
unchanged. If `estimate.fragment_derivations` is larger than you want to pay for, this is the
moment to fall back to the no-spend variant below.

## 7. Enrich, embed, upsert

```bash
./bin/inget run github/repo
```

Eight views per repository, each composed only over the fragments its `depends_on` globs
match, generated, embedded, and upserted into `inget_vectors`. Interrupt it with `SIGTERM`
and it drains what is in flight, marks the run interrupted, and resumes from the same place
next time — progress is checkpointed per item.

Run it a second time and it should report zero LLM calls and zero upserts. That is the
cascade holding, and it is the property worth checking first.

## 8. Did it come out searchable

```bash
./bin/inget eval github/repo | jq '{datatype, status, sampled_items, view_vectors, metrics}'
```

Five metrics per datatype, plus a per-view breakdown under `.views`, scored against the
floors in `config.yaml`; a breach exits non-zero. It re-embeds text already in state and
never generates, so it costs embedding time and nothing else. A `status` of `unscorable`
means there was nothing to measure — run steps 5 to 7 first.

## 9. Query it

Through the binary:

```bash
./bin/inget query "which repos search text fast"
./bin/inget query "terminal user interface library" --view stack --limit 5
./bin/inget query "http routing" --json | jq '.[] | {item_id, view, score}'
```

### Through curl on localhost

There is no inget HTTP server — an explicit non-goal, and neither binary listens on a socket.
What does listen on localhost is the embedder; postgres speaks its own protocol. So a
curl-driven query is two steps: `curl` turns text into a vector, and psql runs the same
nearest-neighbour statement `inget query` runs.

```bash
ask() {
  vec=$(curl -fsS http://localhost:8080/v1/embeddings \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg q "$1" '{model:"Qwen/Qwen3-Embedding-0.6B", input:$q}')" \
    | jq -c '.data[0].embedding')

  docker compose -f deploy/docker-compose.yaml exec -T postgres \
    psql -U inget -d inget -v ON_ERROR_STOP=1 -v vec="$vec" -v view="${2:-role}" -f - <<'SQL'
BEGIN;
-- Both settings are what inget sets per transaction. Without iterative_scan a filtered HNSW
-- search silently loses recall: it takes ef_search candidates first, then applies the WHERE.
SET LOCAL hnsw.iterative_scan = relaxed_order;
SET LOCAL hnsw.ef_search = 100;
SELECT item_id,
       view_name,
       round((1 - (embedding <=> :'vec'::halfvec(1024)))::numeric, 4) AS score
FROM inget_vectors
WHERE datatype = 'github/repo' AND view_name = :'view'
ORDER BY embedding <=> :'vec'::halfvec(1024)
LIMIT 10;
COMMIT;
SQL
}

ask "which repos search text fast"
ask "dependency injection and configuration" stack
```

The vector goes in as a psql variable rather than interpolated into the statement text, and
`halfvec(1024)` must match `destinations[].storage` and `models.embedder.dimensions`.

One hazard worth stating plainly: `inget query` asserts the destination's model binding before
searching and curl does not, so embedding a query with a different model than the index was
built with yields rankings that look ordinary and mean nothing. Check the binding, and look
around while you are in there:

```bash
./bin/inget state show | jq                        # what each cascade level holds
docker compose -f deploy/docker-compose.yaml exec -T postgres psql -U inget -d inget <<'SQL'
SELECT * FROM inget_model_registry;
SELECT view_name, count(*) FROM inget_vectors GROUP BY 1 ORDER BY 2 DESC;
SELECT item_id, metadata->>'language' AS lang FROM inget_vectors
  WHERE metadata @> '{"language":"Rust"}'::jsonb AND view_name = 'role' LIMIT 10;
SELECT left(text, 200) FROM inget_vectors WHERE view_name = 'role' LIMIT 1;
SQL
```

The metadata filter uses the GIN index, the same thing that makes `related_keys` — "what else
points at this repository" — a query rather than a scan.

## 10. Prove the incremental path

```bash
./bin/inget-fetch --source github --datatype github/repo --only "$REPOS"   # writes ~no blobs
./bin/inget run github/repo                                               # zero LLM calls
```

A repository whose last push has not moved costs one line of a listing response. One whose
push moved is re-fetched, but only the fragments that actually changed are re-derived, and
only the views whose globs match those fragments regenerate.

## Variant: no generator spend

Generation is what costs money. To exercise fetch → cascade → embed → upsert → query without
it, override the datatype in `config.local.yaml` to use the passthrough enricher. Remember
that a list in the local file replaces the whole list it names, so this block replaces every
datatype — `monday/item` included, which is fine since its connector is not implemented:

```yaml
datatypes:
  - name: github/repo
    source: github
    enricher: passthrough          # view text is the composed fragments, no LLM
    fragment_enricher:
      enabled: false               # no per-fragment LLM either
    compose:
      order: tier
      max_chars: 8000              # keep each view inside the embedder's input limit
    destinations: [local-pgvector]
    views:
      - {name: role, prompt: prompts/github/repo/role.tmpl, depends_on: ["**"]}
      - {name: stack, prompt: prompts/github/repo/stack.tmpl,
         depends_on: ["go.mod", "package.json", "Cargo.toml", "pom.xml", "pyproject.toml"]}
```

`prompt` is still required by validation — `views[].prompt` is unconditional — and still
ignored by the passthrough enricher. `max_chars: 8000` is the part that matters: passthrough
embeds the composed document verbatim, and the shipped 120000 characters is roughly 30k
tokens, at or past what this model accepts in one input. `INGET_GENERATOR_API_KEY` must still
be non-empty, because the client is constructed either way, but nothing calls it. Use this to
check wiring, never to judge quality: eval's distinctiveness metrics over passthrough text say
nothing about a prompt.

## Teardown

```bash
docker compose -f deploy/docker-compose.yaml down          # keeps the volumes
docker compose -f deploy/docker-compose.yaml down -v       # deletes the database and weights
rm -rf .inget                                              # the local artifact store
```

`inget state gc --dry-run` is the in-band way to reclaim space instead: it reports the run
directories, blobs and derivations it would collect without deleting anything.
