-- +goose Up
-- Deleting traffic (docs/adr/2026-10-14-deleting-a-pair-deletes-everything-it-owns.md) removes the pairs' rows from both contentless
-- search indexes. By default FTS5 rewrites every segment whose rows are 10%
-- or more deleted, in the transaction that crosses the threshold, so one
-- bulk delete of a tenth of a project rewrote the whole body index: 2.7 s and
-- 350 MB of WAL for 10k pairs out of 100k (ledger case 25). With deletemerge
-- off, deleted rows stay as tombstones that searches skip, and the ordinary
-- automerge drops them when it merges their segment.
INSERT INTO traffic_index (traffic_index, rank) VALUES ('deletemerge', 0);
INSERT INTO traffic_annotation_index (traffic_annotation_index, rank) VALUES ('deletemerge', 0);

-- +goose Down
INSERT INTO traffic_annotation_index (traffic_annotation_index, rank) VALUES ('deletemerge', 10);
INSERT INTO traffic_index (traffic_index, rank) VALUES ('deletemerge', 10);
