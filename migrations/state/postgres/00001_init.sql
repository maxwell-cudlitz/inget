-- The inget_state schema, verbatim from the "State schema" section of
-- docs/feature-design.md. Every guard level of the invalidation cascade is a table here.
--
-- The schema itself is created by internal/state.Migrate before goose runs, so that
-- goose's own version table lands inside inget_state rather than in public. Table names
-- are qualified here because DDL is read by humans; runtime statements rely on the
-- search_path the connection pool sets.
--
-- "binary" is quoted because it is a reserved word in PostgreSQL.

-- +goose Up
CREATE TABLE inget_state.items (
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

CREATE TABLE inget_state.fragments (
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

CREATE TABLE inget_state.derivations (
    cache_key   TEXT PRIMARY KEY,             -- hash(frag fingerprint ‖ signature)
    datatype    TEXT NOT NULL,
    item_id     TEXT NOT NULL,
    frag_key    TEXT NOT NULL,
    signature   TEXT NOT NULL,
    output      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_hit_at TIMESTAMPTZ
);

CREATE INDEX derivations_item_idx ON inget_state.derivations (datatype, item_id, frag_key);

CREATE TABLE inget_state.views (
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

CREATE TABLE inget_state.refs (
    from_datatype  TEXT NOT NULL,
    from_item_id   TEXT NOT NULL,
    ref_name       TEXT NOT NULL,
    to_kind        TEXT NOT NULL,             -- 'inget' | 'http' | ...
    to_key         TEXT NOT NULL,
    to_fingerprint TEXT,
    resolved_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (from_datatype, from_item_id, ref_name, to_key)
);

CREATE INDEX refs_reverse_idx ON inget_state.refs (to_kind, to_key);

CREATE TABLE inget_state.runs (
    run_id      TEXT PRIMARY KEY,             -- ULID
    "binary"    TEXT NOT NULL,                -- inget | inget-fetch
    datatype    TEXT,
    scope       TEXT NOT NULL,                -- full | partial
    status      TEXT NOT NULL,                -- running | ok | failed | interrupted
    config_hash TEXT NOT NULL,
    stats       JSONB NOT NULL DEFAULT '{}',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE inget_state.work (
    run_id     TEXT NOT NULL,
    datatype   TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    status     TEXT NOT NULL,                 -- pending | claimed | done | failed
    attempts   SMALLINT NOT NULL DEFAULT 0,
    error      TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, datatype, item_id)
);

CREATE INDEX work_pending_idx ON inget_state.work (run_id, status);

CREATE TABLE inget_state.signatures (
    scope      TEXT PRIMARY KEY,              -- 'enricher:github/repo:file'
    signature  TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE inget_state.signatures;
DROP TABLE inget_state.work;
DROP TABLE inget_state.runs;
DROP TABLE inget_state.refs;
DROP TABLE inget_state.views;
DROP TABLE inget_state.derivations;
DROP TABLE inget_state.fragments;
DROP TABLE inget_state.items;
