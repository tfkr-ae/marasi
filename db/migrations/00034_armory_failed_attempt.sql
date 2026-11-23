-- +goose Up
-- A failed attempt (GLOSSARY) is a request an Armory run built but did not
-- send, because a payload left one of its messages unable to encode. It is
-- kept with its run, and deleted with it, but it is not a pair. payloads is
-- a JSON array of {"position", "value"} ordered by position.
CREATE TABLE IF NOT EXISTS armory_failed_attempt (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    payloads TEXT NOT NULL,
    error TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    FOREIGN KEY (run_id) REFERENCES armory_run(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_armory_failed_attempt_run_id ON armory_failed_attempt(run_id, id);

-- +goose Down
DROP INDEX IF EXISTS idx_armory_failed_attempt_run_id;
DROP TABLE IF EXISTS armory_failed_attempt;
