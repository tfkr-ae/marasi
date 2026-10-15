-- +goose Up
-- Notes and metadata have their own small index (ADR-0038), keyed on
-- traffic_index_key like the body index, so that editing a note or metadata
-- never re-tokenises request and response bodies. Body index rows written
-- before this migration still hold note and metadata text; searches read the
-- body index only through its head and body columns, and new body rows put
-- NULL in the old columns. This migration does no backfill; the
-- annotation_index background job indexes the pairs it is missing.
CREATE VIRTUAL TABLE IF NOT EXISTS traffic_annotation_index USING fts5(
    note,
    metadata,
    content = '',
    contentless_delete = 1,
    tokenize = 'trigram case_sensitive 0'
);

-- +goose Down
DROP TABLE IF EXISTS traffic_annotation_index;
