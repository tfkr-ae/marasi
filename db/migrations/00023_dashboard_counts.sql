-- +goose Up
-- The dashboard's pair and intercepted counts are kept as the project is
-- written, so that reading them does not scan request (ADR-0043).
CREATE TABLE IF NOT EXISTS dashboard_count (
    name TEXT PRIMARY KEY,
    value INTEGER NOT NULL
) WITHOUT ROWID;

-- Every pair has one pair number (ADR-0042), so the pair count follows
-- pair_number. 'pairs' counts the numbers up to 'pairs_through'; a read adds
-- the numbers above it, which the repository folds in when it opens and
-- closes the project. Capture therefore writes nothing more. Deleting a pair
-- cascades to its number, so the delete trigger also counts pairs a binary
-- older than this migration deletes.
INSERT INTO dashboard_count (name, value) SELECT 'pairs', count(*) FROM pair_number;
INSERT INTO dashboard_count (name, value) SELECT 'pairs_through', coalesce(max(number), 0) FROM pair_number;
INSERT INTO dashboard_count (name, value) VALUES ('intercepted', 0);

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS pair_number_uncounted AFTER DELETE ON pair_number
WHEN OLD.number <= (SELECT value FROM dashboard_count WHERE name = 'pairs_through')
BEGIN
    UPDATE dashboard_count SET value = value - 1 WHERE name = 'pairs';
END;
-- +goose StatementEnd

-- The intercepted pairs: those whose metadata has intercepted = true. The
-- repository adds and removes a pair as it writes the pair's metadata, and
-- the intercepted_count background job adds the pairs it has not written.
-- A set rather than a bare count means a pair added twice counts once.
CREATE TABLE IF NOT EXISTS pair_intercepted (
    request_id TEXT PRIMARY KEY REFERENCES request(id) ON DELETE CASCADE
) WITHOUT ROWID;

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS pair_intercepted_counted AFTER INSERT ON pair_intercepted
BEGIN
    UPDATE dashboard_count SET value = value + 1 WHERE name = 'intercepted';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS pair_intercepted_uncounted AFTER DELETE ON pair_intercepted
BEGIN
    UPDATE dashboard_count SET value = value - 1 WHERE name = 'intercepted';
END;
-- +goose StatementEnd

-- The pairs the intercepted_count job has still to check: those with an id
-- above above and below below, where NULL is no bound.
CREATE TABLE IF NOT EXISTS intercepted_count_scan (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    above TEXT,
    below TEXT
);
INSERT INTO intercepted_count_scan (id, above, below) VALUES (1, NULL, NULL);

-- +goose Down
DROP TABLE IF EXISTS intercepted_count_scan;
DROP TABLE IF EXISTS pair_intercepted;
DROP TRIGGER IF EXISTS pair_number_uncounted;
DROP TABLE IF EXISTS dashboard_count;
