-- +goose Up
-- Responses in the order they were written (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md). A sorted walk reads
-- the highest seq when it starts, and a pair whose response is written
-- after that is read with the pairs that have no value until the walk ends,
-- so a backward walk never returns it twice. responded_at cannot order
-- them: it is stamped before the write waits for the connection. Rows are
-- only appended, so seq, the rowid, only grows. A deleted pair's rows stay;
-- no pair has its id again, so they never match.
CREATE TABLE IF NOT EXISTS pair_response_order (
    seq INTEGER PRIMARY KEY,
    request_id TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS pair_response_order;
