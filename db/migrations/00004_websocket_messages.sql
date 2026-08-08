-- +goose Up
CREATE TABLE IF NOT EXISTS websocket_connections (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL DEFAULT 'open',
    transport TEXT NOT NULL DEFAULT 'ws',
    host TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    closed_at DATETIME,
    close_code INTEGER NOT NULL DEFAULT 0,
    close_reason TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ws_conns_request_id
    ON websocket_connections(request_id);


CREATE TABLE IF NOT EXISTS websocket_messages (
    id TEXT PRIMARY KEY,
    connection_id TEXT NOT NULL,
    direction TEXT NOT NULL,
    opcode INTEGER NOT NULL,
    fin INTEGER NOT NULL DEFAULT 1,
    payload BLOB,
    is_binary INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    metadata JSON NOT NULL DEFAULT '{}',
    FOREIGN KEY (connection_id) REFERENCES websocket_connections(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ws_msgs_connection_id
    ON websocket_messages(connection_id);

CREATE INDEX IF NOT EXISTS idx_ws_msgs_created_at
    ON websocket_messages(connection_id, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_ws_msgs_created_at;
DROP INDEX IF EXISTS idx_ws_msgs_connection_id;
DROP TABLE IF EXISTS websocket_messages;

DROP INDEX IF EXISTS idx_ws_conns_request_id;
DROP TABLE IF EXISTS websocket_connections;

