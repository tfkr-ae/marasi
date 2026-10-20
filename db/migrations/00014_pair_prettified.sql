-- +goose Up
-- Prettified copies of a pair's request and response leave its metadata
-- (docs/adr/2026-10-14-prettified-bodies-leave-metadata.md). New pairs store them here at capture. The prettified_move
-- background job moves the copies of existing pairs out of metadata, so until
-- it completes a read looks here first and then in metadata. This migration
-- moves nothing, so opening a large project stays fast.
CREATE TABLE IF NOT EXISTS pair_prettified (
    request_id TEXT PRIMARY KEY REFERENCES request(id) ON DELETE CASCADE,
    request TEXT,
    response TEXT
);

-- +goose Down
-- Copies that have moved go back into metadata, where an older binary reads
-- them.
UPDATE request SET metadata = json_set(coalesce(metadata, '{}'), '$."prettified-request"', p.request)
FROM pair_prettified p WHERE p.request_id = request.id AND p.request IS NOT NULL;
UPDATE request SET metadata = json_set(coalesce(metadata, '{}'), '$."prettified-response"', p.response)
FROM pair_prettified p WHERE p.request_id = request.id AND p.response IS NOT NULL;
DROP TABLE IF EXISTS pair_prettified;
