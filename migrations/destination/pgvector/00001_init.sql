-- The pgvector destination schema, from the "Destination schema (pgvector)" section of
-- docs/feature-design.md.
--
-- This file is a Go text/template rather than directly runnable SQL. The vector column's
-- type and width, the table name, and the HNSW build parameters all come from the
-- destination's configuration block, and PostgreSQL DDL cannot parameterise a type
-- modifier. internal/destination renders the template and hands the result to goose, so
-- the DDL still lives in one readable place and still gets a migration version.
--
-- Two consequences of one database being able to hold more than one destination:
--
--   * Index names are derived from the table name, so two destinations in one database do
--     not collide on inget_vectors_facet_idx.
--   * The extension and the model registry are IF NOT EXISTS. The registry is shared on
--     purpose — its primary key is the table name, so one row per destination is exactly
--     what D7 needs — while each destination keeps its own goose version table.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE {{.Table}} (
    id           TEXT PRIMARY KEY,             -- sha256(datatype‖item_id‖view‖frag_key)
    datatype     TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    view_name    TEXT NOT NULL,
    granularity  TEXT NOT NULL DEFAULT 'item', -- item | fragment
    frag_key     TEXT,
    text         TEXT NOT NULL,
    embedding    {{.Storage}}({{.Dims}}) NOT NULL,
    model        TEXT NOT NULL,
    dims         SMALLINT NOT NULL,
    signature    TEXT NOT NULL,
    metadata     JSONB NOT NULL DEFAULT '{}',
    related_keys TEXT[] NOT NULL DEFAULT '{}',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX {{.Table}}_facet_idx   ON {{.Table}} (datatype, view_name);
CREATE INDEX {{.Table}}_item_idx    ON {{.Table}} (datatype, item_id);
CREATE INDEX {{.Table}}_meta_idx    ON {{.Table}} USING gin (metadata);
CREATE INDEX {{.Table}}_related_idx ON {{.Table}} USING gin (related_keys);

-- Built last: pgvector documents index-after-load as faster than loading into an existing
-- index. On an empty table the ordering costs nothing; `inget reindex` is what drops and
-- rebuilds this index around a cold load.
CREATE INDEX {{.Table}}_hnsw_idx ON {{.Table}}
    USING hnsw (embedding {{.Ops}}) WITH (m = {{.M}}, ef_construction = {{.EFConstruction}});

-- Enforces D7: one embedder per destination table.
CREATE TABLE IF NOT EXISTS {{.Registry}} (
    table_name TEXT PRIMARY KEY,
    model      TEXT NOT NULL,
    dims       SMALLINT NOT NULL,
    signature  TEXT NOT NULL,
    bound_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DELETE FROM {{.Registry}} WHERE table_name = '{{.Table}}';
DROP TABLE {{.Table}};
