-- +goose Up
-- The traffic index holds the searchable text of each request/response pair
-- (ADR-0026). Its rowid is the request row's rowid. The traffic repository
-- writes it in the same transaction as the pair. This migration does no
-- backfill; pairs missing from the index are indexed in the background.
CREATE VIRTUAL TABLE IF NOT EXISTS traffic_fts USING fts5(
    request_head,
    request_body,
    response_head,
    response_body,
    note,
    metadata,
    content = '',
    contentless_delete = 1,
    tokenize = 'trigram case_sensitive 0'
);

-- +goose Down
DROP TABLE IF EXISTS traffic_fts;
