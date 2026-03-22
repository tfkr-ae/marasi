-- +goose Up

CREATE TABLE IF NOT EXISTS test_cases (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL, 
    description TEXT,
    category TEXT,
    tags TEXT,
    note TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS findings (
    id TEXT PRIMARY KEY,
    test_case_id TEXT REFERENCES test_cases(id) ON DELETE SET NULL,
    title TEXT NOT NULL,
    cvss_vector TEXT,
    cvss_score REAL,
    severity TEXT NOT NULL,
    writeup TEXT,
    treatment_plan TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS test_case_requests (
    test_case_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    PRIMARY KEY (test_case_id, request_id),
    FOREIGN KEY(test_case_id) REFERENCES test_cases(id) ON DELETE CASCADE,
    FOREIGN KEY(request_id) REFERENCES request(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS finding_requests (
    finding_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    PRIMARY KEY (finding_id, request_id),
    FOREIGN KEY(finding_id) REFERENCES findings(id) ON DELETE CASCADE,
    FOREIGN KEY(request_id) REFERENCES request(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    test_case_id TEXT REFERENCES test_cases(id) ON DELETE CASCADE,
    finding_id TEXT REFERENCES findings(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    data BLOB,
    size_bytes INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (
        (test_case_id IS NOT NULL AND finding_id IS NULL) OR
        (test_case_id IS NULL AND finding_id IS NOT NULL)
    )
);

-- +goose Down

DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS finding_requests;
DROP TABLE IF EXISTS test_case_requests;
DROP TABLE IF EXISTS findings;
DROP TABLE IF EXISTS test_cases;
