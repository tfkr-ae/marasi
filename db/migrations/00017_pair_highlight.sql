-- +goose Up
-- A pair's highlight lives in a side table, not on request (docs/adr/2026-10-14-highlight-is-a-side-table.md): a
-- request row holds both raw bodies, so a column growing from NULL would make
-- SQLite rewrite the whole record, and metadata would re-index the pair. A
-- pair has at most one highlight, from 1 to 8, and loses it when it is
-- deleted. marasi-app migrates its old metadata.highlight values itself.
CREATE TABLE IF NOT EXISTS pair_highlight (
    request_id TEXT PRIMARY KEY REFERENCES request(id) ON DELETE CASCADE,
    highlight INTEGER NOT NULL CHECK (highlight BETWEEN 1 AND 8)
) WITHOUT ROWID;

-- Serves highlight = n: the pairs with one highlight, in id order.
CREATE INDEX IF NOT EXISTS pair_highlight_by_highlight ON pair_highlight (highlight, request_id);

-- +goose Down
DROP TABLE IF EXISTS pair_highlight;
