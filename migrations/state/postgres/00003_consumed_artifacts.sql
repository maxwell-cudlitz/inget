-- Which artifact run a datatype has finished consuming.
--
-- Without this the consumer could only ever read the newest committed run, so a producer
-- topology that commits one run per item — a CI job announcing its own repository — lost
-- every submission but the last: the others were never deleted, because a partial run says
-- nothing about absence, they were simply never read. Recording the high-water mark lets
-- `inget run` drain every run newer than it, oldest first.
--
-- One row per datatype. The mark advances only after a run completed with no failed items,
-- and the upsert is guarded so it can only move forward: a concurrent or out-of-order write
-- cannot rewind it and cause work to be replayed.
--
-- run_id is the *artifact* run identifier from the envelope's manifest, not a row in
-- inget_state.runs. The two are different sequences — one produced by inget-fetch, one by
-- inget — and conflating them is the mistake this comment exists to prevent.

-- +goose Up
CREATE TABLE inget_state.consumed_artifacts (
    datatype    TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL,               -- ULID of the artifact run
    consumed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE inget_state.consumed_artifacts;
