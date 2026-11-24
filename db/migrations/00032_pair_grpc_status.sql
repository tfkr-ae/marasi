-- +goose Up
-- A gRPC pair records the grpc-status its response ended with, read from
-- its trailers or from a trailers-only response head, so traffic lists show
-- it and queries filter on it (grpc_status). It is NULL for a pair that is
-- not gRPC, and for pairs stored before this column: they are not
-- backfilled. The response write sets it.
ALTER TABLE request ADD COLUMN grpc_status INTEGER;

-- +goose Down
ALTER TABLE request DROP COLUMN grpc_status;
