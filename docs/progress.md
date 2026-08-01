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
| 2 | Configuration | **done** |
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

## Step 2 record

Implemented: `internal/config` (schema for the documented file, viper layering with
`config.local.yaml` beside the base file, `INGET_` prefix with `__` nesting, strict
decoding, day-aware `Duration`, validation, secret indirection, config and domain
hashing); root `config.yaml` with the documented defaults; `--config` / `INGET_CONFIG` on
both binaries; `logging.Options.Validate` so the config layer and the logger agree on the
permitted level, format and destination.

Files: `schema.go` (types), `lookup.go` (name lookups and derived values), `duration.go`,
`load.go` (layering, strict decode, list-element defaults), `log.go` (`LoadLog`),
`secret.go`, `flatten.go` (one reflection walk), `hash.go`, `validate.go` (globals plus
the shared accumulator), `validate_lists.go` (referential integrity, globs, dimension
bounds). Fixtures `testdata/minimal.yaml` and `testdata/reordered.yaml`.

Verified: `make build test lint` green; `golangci-lint run` 0 issues; `go mod tidy` a
no-op; `./bin/inget --config config.yaml` loads the shipped file, and an invalid log level
in it exits 1 with the reason.

Deviations from the design and the plan:

- **Env names are computed here, not by viper's key replacer.** `BindEnv` is called with
  an explicit second argument from `config.EnvName`, because viper applies
  `SetEnvKeyReplacer` on the `AutomaticEnv` path but not to the one-argument `BindEnv`
  form. The replacer is still set, so both paths agree.
- **doublestar pinned at step 2, not step 5.** Glob syntax validation is a step 2
  acceptance item, and validating with `path.Match` while the delta engine matches with
  doublestar would accept patterns the matcher later rejects. D15's "pin when the step
  needs it" is satisfied: the dependency is used now. `go-viper/mapstructure/v2` is also
  direct, for the decode hooks.
- **Driver, enricher and resolver names are not enum-validated.** They are registry keys
  owned by `internal/source`, `internal/enrich` and `internal/destination`; validating
  them in config would mean editing config to add an implementation. Config checks that
  they are named and non-empty. Closed sets that belong to the schema — log fields,
  compression, `state.driver`, storage, granularity, `compose.order`, `inject_as` — are
  enum-validated.
- **Source `domain` and `limits` stay raw maps.** Their keys differ per driver, so the
  connector decodes them. `DomainHash` hashes the map, which is what the manifest needs.
- **No in-code defaults for the global blocks.** Only list-element settings get defaults
  in code (`compose.order`, `compose.max_chars`, `drift_threshold`), because viper cannot
  address list elements and so no layer can supply them. Everything else is required by
  validation, which keeps one source of truth: the shipped `config.yaml`.

Choices made where the plan was silent:

- `drift_threshold` is `*float64` so an explicit `0` — re-embed on any textual change —
  stays distinguishable from an absent key. `normalize` fills the default in before
  hashing, so a hash always reflects effective behavior.
- Secrets are snapshotted into an unexported map at load and read with `cfg.Secret(ref)`.
  A missing variable is not a load failure; it fails at the point of use, so a GitHub-only
  run does not require `INGET_MONDAY_TOKEN`. `Config.String` is implemented because `fmt`
  prints unexported fields with `%+v`; a test asserts a formatted config cannot leak a
  value. The cost is that `%+v` no longer dumps the struct.
- `ConfigHash` covers the source entry, the datatype entry, and the artifact limits that
  change what a fetch produces. `artifacts.url` is excluded: moving the bucket does not
  change content. Encoding is length-prefixed sorted pairs with a type tag per value and
  floats in `strconv` `'x'` form, so no decimal or separator ambiguity can move a hash.
- Validation reports every problem via `errors.Join` rather than failing on the first, and
  paths in messages name list elements (`datatypes[github/repo].views[role]`) rather than
  indices.
- `version` still skips the logger hook, as step 1 decided, so `LoadLog` only matters for
  the other commands; it tolerates a missing file but not a malformed one.
- No `inget config` command was added. `config.Load` has no CLI caller yet — step 4 is the
  first command that needs the whole file, and `inget plan` (step 8) is where hashes and
  signatures become visible. Adding a command now would be surface the design does not
  specify.

Known limitation, asserted by a test rather than worked around: list elements are not
env-overridable (`INGET_SOURCES__0__NAME` does nothing), and overriding a list through
`config.local.yaml` replaces the element rather than merging into it.

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
what is actually used. `doublestar` is already pinned: step 2 validates dependency globs
with the same matcher step 5 will match with.

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
