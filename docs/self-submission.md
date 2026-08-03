# Self-submission: a repository that indexes itself

A worked example rather than a contract. It asserts observable behaviour — flags, environment
variable names, JSON fields — so a change to any of those should update it.

`inget-fetch` is the producer, and nothing about it assumes it runs beside the consumer. Point
it at shared artifact storage and a shared state store and it can run anywhere, including inside
the CI of the repository it is indexing. The repository announces itself on push; a scheduled
`inget run` elsewhere enriches whatever has arrived.

This needs no new code and no HTTP endpoint — there is no inget server, by design. The artifact
store *is* the submission interface, and the envelope in
[`artifact-envelope.md`](artifact-envelope.md) is its contract.

## The workflow

```yaml
name: index
on:
  push:
    branches: [main]

jobs:
  submit:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write          # only if you assume an AWS role by OIDC
    steps:
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: ${{ secrets.INGET_ARTIFACTS_ROLE }}
          aws-region: us-east-1

      - name: Install inget-fetch
        run: |
          curl -fsSL -o inget.tar.gz \
            https://github.com/maxwell-cudlitz/inget/releases/download/v0.1.0/inget_Linux_x86_64.tar.gz
          tar -xzf inget.tar.gz inget-fetch config.yaml prompts

      - name: Submit this repository
        env:
          INGET_GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          INGET_STATE_DSN: ${{ secrets.INGET_STATE_DSN }}
          INGET_ARTIFACTS__URL: s3://inget-artifacts/prod
          LOG_FORMAT: json
        run: |
          ./inget-fetch --source github --datatype github/repo \
            --only "$GITHUB_REPOSITORY" \
            | tee report.json | jq '{items, fragments, blobs_written, warnings}'
```

There is no checkout step: the connector fetches the tarball through the API, so it reads the
commit GitHub has rather than whatever a checkout action left on disk. `GITHUB_TOKEN` with
`contents: read` is enough for the repository the workflow runs in, and `--only` bypasses the
source's `domain` filters, so the same `config.yaml` serves every repository that copies this
workflow.

Everything the connector does happens in that job: the tree walk, the noise filter, the
content-shape filter, secret scanning, sub-file splitting, and the commit protocol. What lands in
`s3://inget-artifacts/prod` is one committed run carrying one item, and the LLM has not been
called and no vector has been written.

## What the workflow must not get wrong

**Scope.** `--only` forces `scope: partial`, which is what makes this safe: a partial run says
nothing about the items it does not carry. Committing `scope: full` from a single-repository job
would tombstone every other repository in the datatype. Do not override it.

**Credentials.** The job needs write access to the artifact bucket and read/write on the state
store, because `inget-fetch` reads item fingerprints to skip unchanged work. It needs no
generator key, no embedder and no destination — those belong to the consumer. Reaching the state
store from a GitHub-hosted runner means exposing Postgres to the internet; prefer a self-hosted
runner inside the network, or a managed instance with TLS and an allowlist.

**Concurrency.** Parallel submissions do not conflict. Each writes its own run directory under a
distinct ULID, and blobs are content-addressed with an existence check, so two repositories
sharing a file store it once. `inget-fetch` takes no datatype lock; only the consumer does.

## The consumer side

```bash
inget plan github/repo | jq '{pending_artifact_runs, total_items, added, estimate}'
inget run github/repo
```

`inget run` drains the backlog: it consumes every committed run newer than the datatype's
high-water mark, oldest first, and advances the mark behind each pass that finished cleanly.
Without the mark it could only read the newest run, and fifty repositories announcing themselves
in an hour would leave forty-nine submissions unread — not deleted, since a partial run proves
nothing about absence, just never looked at.

`inget plan` reports the whole backlog in `pending_artifact_runs` but prices only the run it
names in `artifact_run_id`, and warns when there is more than one. The estimate is therefore a
lower bound on a drain of several runs, unlike the single-run case where it is an upper bound.

Three things stop the mark advancing, each of which means "read this run again next time":

- a pass with a failed item, so the failure retries rather than being skipped forever;
- a pass narrowed by `--only` or `--limit`, which cannot speak for the whole run;
- an interrupted drain, whose remaining backlog survives to the next invocation.

Two bounds worth knowing. A datatype with no mark recorded — every state store from before the
mark existed — consumes the latest run only, so upgrading does not replay a month of retained
runs; the mark starts tracking from the first clean pass. And `inget state gc` collects run
directories older than `retention.runs` whether or not they were consumed, so a backlog must be
drained inside the retention window. A nightly consumer against a 30-day window has ample
margin; a consumer that stops for a month does not.
