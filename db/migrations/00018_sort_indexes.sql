-- +goose Up
-- Traffic lists sort by any ledger column (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md). Each sort walks an
-- index on (key, id); host and content_type already have one.
CREATE INDEX IF NOT EXISTS idx_request_method_id ON request(method, id);
CREATE INDEX IF NOT EXISTS idx_request_path_id ON request(path, id);
CREATE INDEX IF NOT EXISTS idx_request_status_code_id ON request(status_code, id);

-- A pair with no response has no length, so it sorts last. Older binaries
-- left the column's '0' default on such pairs, and stored NULL for a
-- response with no Content-Length, which sorts as 0.
UPDATE request SET length = NULL WHERE responded_at IS NULL AND length IS NOT NULL;
UPDATE request SET length = '0' WHERE responded_at IS NOT NULL AND (length IS NULL OR length = '');

-- The size sort, the length query field and cursors all use this exact
-- expression, so SQLite matches the index.
CREATE INDEX IF NOT EXISTS idx_request_length_id ON request(CAST(nullif(length, '') AS INTEGER), id);

-- +goose Down
DROP INDEX IF EXISTS idx_request_length_id;
UPDATE request SET length = '0' WHERE length IS NULL;
DROP INDEX IF EXISTS idx_request_status_code_id;
DROP INDEX IF EXISTS idx_request_path_id;
DROP INDEX IF EXISTS idx_request_method_id;
