-- +goose Up
-- Deleting a pair cascades to every table that references it (ADR-0034).
-- These tables have primary keys that lead with another column, so without
-- an index on request_id each deleted pair scans them.
CREATE INDEX IF NOT EXISTS idx_logs_request_id ON logs(request_id);
CREATE INDEX IF NOT EXISTS idx_finding_requests_request_id ON finding_requests(request_id);
CREATE INDEX IF NOT EXISTS idx_test_case_requests_request_id ON test_case_requests(request_id);
CREATE INDEX IF NOT EXISTS idx_armory_entry_request_id ON armory_entry(request_id);

-- An exact host filter walks this index newest first instead of scanning
-- the ledger (ledger benchmark cases 7 and 8), and counting pairs per
-- content type reads this one instead of the table (case 20). A
-- (status_code, id) index did not help case 10, so there is none.
CREATE INDEX IF NOT EXISTS idx_request_host_id ON request(host, id);
CREATE INDEX IF NOT EXISTS idx_request_content_type_id ON request(content_type, id);

-- +goose Down
DROP INDEX IF EXISTS idx_request_content_type_id;
DROP INDEX IF EXISTS idx_request_host_id;
DROP INDEX IF EXISTS idx_armory_entry_request_id;
DROP INDEX IF EXISTS idx_test_case_requests_request_id;
DROP INDEX IF EXISTS idx_finding_requests_request_id;
DROP INDEX IF EXISTS idx_logs_request_id;
