# Implementation Progress

Handoff record for the one-step-per-session cadence. A session should be able to orient
from this file plus the named design line ranges, without reading all 1353 lines of
`feature-design.md`.

## Session protocol

1. Read `AGENTS.md`, then this file.
2. Read the step's entry in `docs/implementation-plan.md` and only the design sections
   listed in the map below.
3. Implement, then make `make build test lint` green.
4. Update the status table, any deviations, and any decisions made.
5. Commit as `step N: <goal>`.

## Status

| Step | Goal | State |
|---|---|---|
| 1 | Project skeleton | **done** |
| 2 | Configuration | not started |
| 3 | Artifact envelope and blob store | not started |
| 4 | State store | not started |
| 5 | Delta engine | not started |
| 6 | Model clients | not started |
| 7 | pgvector destination | not started |
| 8 | Enrichment pipeline | not started |
| 9 | GitHub connector | not started |
| 10 | Monday connector | not started |
| 11 | Reference resolution | not started |
| 12 | Quality harness | not started |
| 13 | Reindex and operations | not started |
| 14 | Release and documentation | not started |

Sizing note: steps 5, 8, 9 and 10 carry more surface than one session should hold. Plan on
splitting each into an implementation session and a test-hardening session, so expect
roughly 18 sessions rather than 14. Steps 1–8 need no network and no credentials; schedule
9 and 10 for when `INGET_GITHUB_TOKEN` and `INGET_MONDAY_TOKEN` are available.

## Design section map

`feature-design.md` line ranges to read per step. Everything else can stay unread.

| Step | Sections | Lines |
|---|---|---|
| 1 | Architecture, Repository Layout, Observability, D15 | 51–118, 1307–1342, 1286–1306, 539–584 |
| 2 | Configuration, D2 (hashing) | 752–940, 157–180 |
| 3 | `artifact-envelope.md` in full, D5, D6 | all, 258–295 |
| 4 | State schema, D13 | 587–688, 493–515 |
| 5 | D1, D2, D3, D4, Enrichment Pipeline | 121–257, 1213–1240 |
| 6 | D10, API/Interface | 366–419, 941–1103 |
| 7 | D7, D8, D9, Destination schema | 296–365, 689–751 |
| 8 | D11, Enrichment Pipeline, Edge Cases | 420–452, 1213–1264 |
| 9 | `github/repo`, D6, Security considerations | 1144–1165, 280–295, 1265–1285 |
| 10 | `monday/item` | 1166–1212 |
| 11 | D12 | 453–492 |
| 12 | D14, Testing Strategy | 516–538, 1343–1366 |
| 13 | Edge Cases, `artifact-envelope.md` GC | 1241–1264, 235–247 |
| 14 | Non-Goals, Repository Layout | 1367–1375, 1307–1342 |

These offsets are valid for `feature-design.md` at 1375 lines. Editing that file shifts
everything below the edit, so regenerate the map with
`grep -n '^#\{2,3\} ' docs/feature-design.md` whenever the design changes.

## Step 1 record

Implemented: module `github.com/maxwellcudlitz/inget`; `internal/logging` (slog JSON/text,
`LOG_LEVEL`/`LOG_FORMAT`/`LOG_DESTINATION`/`LOG_REDACT`, key-pattern redaction);
`internal/cli` (shared root, `version` with ldflags stamps and build-info fallback, single
outermost error handler); both `cmd/` entrypoints; `Makefile`; `.golangci.yml`
(golangci-lint v2 schema, verified with `golangci-lint config verify`); CI workflow;
Apache-2.0 `LICENSE`; `.gitignore`.

Verified: `bin/inget` and `bin/inget-fetch` build, both print a version, `go vet` clean,
`golangci-lint run` reports 0 issues, `go test -race ./...` passes, `gofmt` clean. CI has
not been observed running — there is no remote push yet.

Deviations from the design:

- Added `internal/cli`, which the design's layout does not list. Two binaries need
  identical root scaffolding; duplicating it across both mains would violate DRY.
- Deferred: config-driven log options. `PersistentPreRunE` currently calls
  `logging.Setup(logging.Default())`; step 2 replaces the argument with the loaded `log`
  block. Environment overrides already work, so behavior is not blocked.

Choices made where the plan was silent:

- Version metadata is injected with `-ldflags -X` and falls back to
  `debug.ReadBuildInfo`, so `go run` and `go install` builds still report truthfully.
- `make lint` warns and skips when `golangci-lint` is absent rather than failing; CI
  enforces it. `make lint-install` installs the pinned v2.12.2.
- CI additionally checks `gofmt` and that `go mod tidy` is a no-op.
- Pinned: cobra v1.10.1, golangci-lint v2.12.2, actions/checkout v7, actions/setup-go v7,
  golangci-lint-action v9.

## Library decisions (approved, folded into D15)

Approved 2026-08-01 and written into `feature-design.md` D15 and the corresponding plan
steps, so no session needs to re-derive them.

| Step | Concern | Selection | Version at approval |
|---|---|---|---|
| 3 | Run identifiers | `oklog/ulid/v2` | v2.1.2, Apache-2.0 |
| 5 | Glob matching with `**` | `bmatcuk/doublestar/v4` | v4.10.0, MIT |
| 5 | Edit distance for D4 drift | `agnivade/levenshtein` | v1.2.1, MIT |
| 9 | Secret detection in fetched content | `zricethezav/gitleaks/v8` | v8.30.1, MIT |

Pin these when the step that needs them lands, not before, so `go.mod` stays honest about
what is actually used.

Deliberately kept in-tree:

- **Log attribute redaction** (`internal/logging/redact.go`). Generic redactors match
  `token`, `key` and `secret` as substrings and would redact `input_tokens`, `cache_key`,
  `related_keys` and `signature` — the exact fields the cascade and `inget plan` exist to
  expose. The custom allowlist is the entire job. The redaction tests assert these
  negatives; do not widen the patterns.
- **Config and signature hashing** (step 2). Length-prefixed serialization of sorted
  key/value pairs, not canonical JSON. The only Go RFC 8785 implementation is
  unmaintained, and length prefixing removes float and unicode formatting ambiguity.

Caveat carried into step 9: gitleaks reaches its regex engine through a WASM runtime
rather than CGO by default. Confirm `CGO_ENABLED=0` builds still work; if not, drop the
dependency for an in-tree ruleset.

## Still open

Nothing. Every library question raised through step 14 is decided and recorded in D15.
Raise new ones here rather than deciding them inside a step.
