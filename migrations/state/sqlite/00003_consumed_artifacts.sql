-- Which artifact run a datatype has finished consuming. See the postgres copy of this
-- migration for the rationale.

-- +goose Up
CREATE TABLE consumed_artifacts (
    datatype    TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL,
    consumed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE consumed_artifacts;
