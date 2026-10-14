-- +goose Up
-- Each background job saves its state here (ADR-0039), so opening a project
-- reads whether a job is complete instead of scanning every pair. Rows are
-- written by the binary's job registry when it opens the project, not by this
-- migration, so a binary that adds a job needs no migration of its own.
--
-- high_water is the newest pair id the job has accounted for. Only binaries
-- that know the job advance it as they write pairs, so a pair newer than it
-- was written by a binary that does not maintain the job, and resets the job
-- on the next open. total is NULL until the job has counted its work.
CREATE TABLE IF NOT EXISTS background_job (
    name TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    complete INTEGER NOT NULL DEFAULT 0,
    done INTEGER NOT NULL DEFAULT 0,
    total INTEGER,
    high_water TEXT,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS background_job;
