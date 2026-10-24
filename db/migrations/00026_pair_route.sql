-- +goose Up
-- Each pair records the route that carried it: the upstream proxy's name and
-- its address as scheme://host:port without credentials. Direct is an empty
-- name and address. Pairs captured before this migration, and pairs with no
-- response yet, have NULL. The snapshot is written in the same UPDATE as the
-- response, which already rewrites the row, so these are columns on request
-- rather than a side table (docs/adr/2026-10-14-highlight-is-a-side-table.md).
-- It is not metadata, so a metadata replace never touches it.
ALTER TABLE request ADD COLUMN upstream_proxy_name TEXT;
ALTER TABLE request ADD COLUMN upstream_proxy_address TEXT;

-- +goose Down
ALTER TABLE request DROP COLUMN upstream_proxy_address;
ALTER TABLE request DROP COLUMN upstream_proxy_name;
