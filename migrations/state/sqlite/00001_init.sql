-- The inget_state schema for SQLite, the offline development and test driver (D13).
--
-- Same tables, columns and keys as migrations/state/postgres/00001_init.sql, with three
-- mechanical differences SQLite requires:
--
--   * no schema namespace, so table names are bare;
--   * JSONB becomes TEXT and TIMESTAMPTZ becomes TEXT holding CURRENT_TIMESTAMP's
--     'YYYY-MM-DD HH:MM:SS' UTC rendering, because SQLite has neither type and TEXT
--     affinity keeps what is written readable by what reads it;
--   * one extra table, locks, standing in for PostgreSQL's session advisory lock. A row
--     is the mutex. Unlike an advisory lock it does not disappear when the holder's
--     connection drops, so a killed process leaves it behind; `inget state unlock`
--     exists for that, and it is one more reason this driver is not the default.

-- +goose Up
CREATE TABLE items (
    datatype      TEXT NOT NULL,
    item_id       TEXT NOT NULL,
    source_name   TEXT NOT NULL,
    fingerprint   TEXT NOT NULL,              -- level-0 token
    composed_hash TEXT,                       -- level-2 input (unscoped)
    metadata      TEXT NOT NULL DEFAULT '{}',
    last_run_id   TEXT,
    first_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at    TEXT,
    PRIMARY KEY (datatype, item_id)
);

CREATE TABLE fragments (
    datatype     TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    frag_key     TEXT NOT NULL,
    fingerprint  TEXT NOT NULL,               -- level-1 token
    blob_ref     TEXT,                        -- sha256 in blob store
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    tier         INTEGER NOT NULL DEFAULT 4,
    last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    missing_runs INTEGER NOT NULL DEFAULT 0,  -- GC counter
    PRIMARY KEY (datatype, item_id, frag_key)
);

CREATE TABLE derivations (
    cache_key   TEXT PRIMARY KEY,             -- hash(frag fingerprint ‖ signature)
    datatype    TEXT NOT NULL,
    item_id     TEXT NOT NULL,
    frag_key    TEXT NOT NULL,
    signature   TEXT NOT NULL,
    output      TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_hit_at TEXT
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
    dims          INTEGER,
    signature     TEXT NOT NULL,
    updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (datatype, item_id, view_name)
);

CREATE TABLE refs (
    from_datatype  TEXT NOT NULL,
    from_item_id   TEXT NOT NULL,
    ref_name       TEXT NOT NULL,
    to_kind        TEXT NOT NULL,             -- 'inget' | 'http' | ...
    to_key         TEXT NOT NULL,
    to_fingerprint TEXT,
    resolved_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (from_datatype, from_item_id, ref_name, to_key)
);

CREATE INDEX refs_reverse_idx ON refs (to_kind, to_key);

CREATE TABLE runs (
    run_id      TEXT PRIMARY KEY,             -- ULID
    "binary"    TEXT NOT NULL,                -- inget | inget-fetch
    datatype    TEXT,
    scope       TEXT NOT NULL,                -- full | partial
    status      TEXT NOT NULL,                -- running | ok | failed | interrupted
    config_hash TEXT NOT NULL,
    stats       TEXT NOT NULL DEFAULT '{}',
    started_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TEXT
);

CREATE TABLE work (
    run_id     TEXT NOT NULL,
    datatype   TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    status     TEXT NOT NULL,                 -- pending | claimed | done | failed
    attempts   INTEGER NOT NULL DEFAULT 0,
    error      TEXT,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (run_id, datatype, item_id)
);

CREATE INDEX work_pending_idx ON work (run_id, status);

CREATE TABLE signatures (
    scope      TEXT PRIMARY KEY,              -- 'enricher:github/repo:file'
    signature  TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE locks (
    lock_key    TEXT PRIMARY KEY,
    holder      TEXT NOT NULL,                -- per-process ULID
    acquired_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE locks;
DROP TABLE signatures;
DROP TABLE work;
DROP TABLE runs;
DROP TABLE refs;
DROP TABLE views;
DROP TABLE derivations;
DROP TABLE fragments;
DROP TABLE items;
