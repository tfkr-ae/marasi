-- +goose Up
-- Capture no longer writes the search indexes in the pair's own transaction
-- (docs/adr/2026-10-09-traffic-index-builds-in-the-background.md). It records the pair here instead, and the repository indexes
-- the recorded pairs in batches, within about a second and before any text
-- query reads the index. A pair recorded here when the process stops is
-- indexed by the next binary that opens the project.
CREATE TABLE IF NOT EXISTS traffic_index_pending (
    request_id TEXT PRIMARY KEY REFERENCES request(id) ON DELETE CASCADE
) WITHOUT ROWID;

-- +goose Down
-- An older binary indexes only pairs with no index key, and only while its
-- index jobs are incomplete, so the pending pairs lose their keys and the
-- jobs start again.
DELETE FROM traffic_index_key WHERE request_id IN (SELECT request_id FROM traffic_index_pending);
UPDATE background_job SET complete = 0
WHERE name IN ('traffic_index', 'annotation_index')
AND EXISTS (SELECT 1 FROM traffic_index_pending);
DROP TABLE IF EXISTS traffic_index_pending;
