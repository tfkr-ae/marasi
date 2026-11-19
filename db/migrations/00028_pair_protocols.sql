-- +goose Up
-- Each pair records its protocol on each leg (GLOSSARY Protocol): the client
-- protocol and the origin protocol, 'http/1.1' or 'h2'. Every pair captured
-- before HTTP/2 was HTTP/1.1 on both legs, so the default backfills them
-- without rewriting the table. The request insert writes both; the response
-- write replaces the origin protocol with the one that carried the pair.
-- Traffic lists sort by either, so each has an index on (key, id), as the
-- other sort keys do (00018_sort_indexes.sql).
ALTER TABLE request ADD COLUMN client_protocol TEXT NOT NULL DEFAULT 'http/1.1';
ALTER TABLE request ADD COLUMN origin_protocol TEXT NOT NULL DEFAULT 'http/1.1';
CREATE INDEX IF NOT EXISTS idx_request_client_protocol_id ON request(client_protocol, id);
CREATE INDEX IF NOT EXISTS idx_request_origin_protocol_id ON request(origin_protocol, id);

-- +goose Down
DROP INDEX IF EXISTS idx_request_origin_protocol_id;
DROP INDEX IF EXISTS idx_request_client_protocol_id;
ALTER TABLE request DROP COLUMN origin_protocol;
ALTER TABLE request DROP COLUMN client_protocol;
