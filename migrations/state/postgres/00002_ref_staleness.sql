-- Reference staleness (D12).
--
-- stale_depth is the invalidation mark the cascade sets on the edges that point at a record
-- whose content changed. NULL means the edge is current: to_fingerprint is the digest the
-- referring item last consumed. A non-NULL value is the cascade depth at which the mark was
-- made, which is what makes a reference cycle terminate — each hop marks at depth+1 and a
-- mark at enrich.max_reference_depth cascades no further.
--
-- The mark lives on the edge rather than in a queue table because that makes deferral free:
-- an item the cap left out of this run keeps its mark, so the next run finds it with the same
-- query. It is cleared by the referring item's own re-resolution, which rewrites its whole
-- edge set inside the item's checkpoint transaction.

-- +goose Up
ALTER TABLE inget_state.refs ADD COLUMN stale_depth INTEGER;

CREATE INDEX refs_stale_idx ON inget_state.refs (from_datatype, from_item_id)
    WHERE stale_depth IS NOT NULL;

-- +goose Down
DROP INDEX inget_state.refs_stale_idx;
ALTER TABLE inget_state.refs DROP COLUMN stale_depth;
