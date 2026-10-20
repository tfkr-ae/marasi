-- The proxy inflates permessage-deflate messages on each leg (docs/adr/2026-10-11-proxy-terminates-permessage-deflate-per-leg.md), so
-- payload is always the uncompressed message. compressed records whether it
-- was compressed on the wire; rows stored before this were not inflated and
-- are left as uncompressed.

-- +goose Up
ALTER TABLE websocket_messages ADD COLUMN compressed INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE websocket_messages DROP COLUMN compressed;
