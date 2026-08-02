-- Reference staleness (D12). See the postgres copy of this migration for the rationale.

-- +goose Up
ALTER TABLE refs ADD COLUMN stale_depth INTEGER;

CREATE INDEX refs_stale_idx ON refs (from_datatype, from_item_id)
    WHERE stale_depth IS NOT NULL;

-- +goose Down
DROP INDEX refs_stale_idx;
ALTER TABLE refs DROP COLUMN stale_depth;
