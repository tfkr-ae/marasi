-- +goose Up
-- The WebSocket extension negotiation is stored once per connection.
-- deflate holds the negotiated permessage-deflate parameters as JSON, or NULL
-- when the connection does not use deflate. removed_extensions is a JSON array
-- of the WebSocket extensions the proxy removed from the client's offer.
ALTER TABLE websocket_connections ADD COLUMN deflate TEXT;
ALTER TABLE websocket_connections ADD COLUMN removed_extensions TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE websocket_connections DROP COLUMN removed_extensions;
ALTER TABLE websocket_connections DROP COLUMN deflate;
