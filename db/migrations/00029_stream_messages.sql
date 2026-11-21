-- Stream messages of streamed pairs (GLOSSARY: Streamed pair, Stream
-- message), as websocket_messages holds a WebSocket connection's messages.
-- request_id is the pair. Deleting the pair deletes its messages
-- (docs/adr/2026-10-14-deleting-a-pair-deletes-everything-it-owns.md), so the
-- index leads with request_id; ids are UUIDv7, so (request_id, id) also
-- reads a pair's messages in the order they arrived. flags is NULL for a
-- framing without a flags byte. payload is never full-text indexed.

-- +goose Up
CREATE TABLE IF NOT EXISTS stream_messages (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    direction TEXT NOT NULL,
    framing TEXT NOT NULL,
    flags INTEGER,
    payload BLOB,
    compressed INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    metadata JSON NOT NULL DEFAULT '{}',
    FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_stream_messages_request_id
    ON stream_messages(request_id, id);

-- +goose Down
DROP INDEX IF EXISTS idx_stream_messages_request_id;
DROP TABLE IF EXISTS stream_messages;
