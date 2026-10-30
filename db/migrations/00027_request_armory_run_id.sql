-- +goose Up
-- The Armory reads and counts a run's traffic with the query
-- metadata.armory_run_id = "<run id>", which this index serves instead of
-- every pair being read. The query field uses this exact expression, so
-- SQLite matches the index. Malformed metadata gives NULL rather than an
-- error, so an insert never fails on the index, and pairs with no run id
-- are left out of it.
CREATE INDEX IF NOT EXISTS idx_request_armory_run_id
    ON request(CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$."armory_run_id"') END, id)
    WHERE CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$."armory_run_id"') END IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_request_armory_run_id;
