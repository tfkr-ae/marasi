-- +goose Up

CREATE TABLE IF NOT EXISTS armory_template (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    raw_template TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS armory_run (
    id TEXT PRIMARY KEY,
    template_id TEXT NOT NULL,
    template_snapshot TEXT NOT NULL,
    use_https BOOLEAN NOT NULL DEFAULT FALSE,
    wordlists JSON NOT NULL DEFAULT '[]',
    status TEXT NOT NULL CHECK (status IN ('draft', 'in_progress', 'complete', 'failed', 'cancelled')),
    attack_type TEXT NOT NULL CHECK (attack_type IN ('harpoon', 'broadside', 'tandem', 'maelstrom')),
    max_concurrent INTEGER NOT NULL CHECK (max_concurrent > 0),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    finished_at DATETIME,
    FOREIGN KEY (template_id) REFERENCES armory_template(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_armory_run_template_id ON armory_run(template_id);

CREATE TABLE IF NOT EXISTS armory_entry (
    run_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    PRIMARY KEY (run_id, request_id),
    FOREIGN KEY (run_id) REFERENCES armory_run(id) ON DELETE CASCADE,
    FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE
);

-- +goose Down

DROP TABLE IF EXISTS armory_entry;
DROP INDEX IF EXISTS idx_armory_run_template_id;
DROP TABLE IF EXISTS armory_run;
DROP TABLE IF EXISTS armory_template;
