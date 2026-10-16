-- +goose Up
-- Each pair gets a number in its project's capture order (ADR-0042). The
-- number lives in a side table so that numbering existing pairs writes small
-- rows instead of rewriting every request row with its bodies (ADR-0035). A
-- declared INTEGER PRIMARY KEY survives VACUUM and table rebuilds, and
-- AUTOINCREMENT never hands out a deleted number again.
CREATE TABLE IF NOT EXISTS pair_number (
    number INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT NOT NULL UNIQUE REFERENCES request(id) ON DELETE CASCADE
);

-- Existing pairs are numbered in id order, which is capture order because
-- ids are UUIDv7.
INSERT INTO pair_number (number, request_id)
SELECT row_number() OVER (ORDER BY id), id FROM request;

-- +goose Down
DROP TABLE IF EXISTS pair_number;
