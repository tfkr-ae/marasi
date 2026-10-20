-- A stored WebSocket message is the whole message (docs/adr/2026-10-11-websocket-message-is-the-whole-message.md), so the
-- per-frame fin flag no longer describes it. frame_count records how many
-- frames carried it; rows stored before this were one frame each.

-- +goose Up
ALTER TABLE websocket_messages ADD COLUMN frame_count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE websocket_messages DROP COLUMN fin;

-- +goose Down
ALTER TABLE websocket_messages ADD COLUMN fin INTEGER NOT NULL DEFAULT 1;
ALTER TABLE websocket_messages DROP COLUMN frame_count;
