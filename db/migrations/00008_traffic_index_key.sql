-- +goose Up
-- The traffic index is keyed on traffic_index_key, not on the request row's
-- implicit rowid (ADR-0026). request has a TEXT primary key, so VACUUM or a
-- migration that rebuilds the table can renumber its rowids, and an index keyed
-- on them would then match the wrong pairs. An INTEGER PRIMARY KEY keeps its
-- values. A key is created in the same transaction that replaces its index
-- row, so a reused key never carries a deleted pair's text, and searches join
-- through this table, so an index row without a key matches nothing.
--
-- traffic_fts from 00007 is left in place: a binary built at 00007 still
-- writes to it when it opens a project, and must not write into this index.
-- Pairs missing from the new index are indexed in the background.
CREATE TABLE IF NOT EXISTS traffic_index_key (
    id INTEGER PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE REFERENCES request(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE IF NOT EXISTS traffic_index USING fts5(
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
DROP TABLE IF EXISTS traffic_index;
DROP TABLE IF EXISTS traffic_index_key;
