-- +goose Up
-- A streamed pair (GLOSSARY) records each streamed direction's message
-- framing and, once it ended, its ending, so traffic lists and queries tell
-- a streamed pair apart while it is live, before its response row is
-- written. The proxy writes a direction's framing once the direction is
-- live and its ending when the pair ends (StreamRepository.RecordStreamedPair).
-- Each is NULL for a direction that is not streamed. Pairs stored before
-- these columns keep their record in metadata.streamed, which the response
-- write merged into metadata, so it backfills them.
ALTER TABLE request ADD COLUMN request_framing TEXT;
ALTER TABLE request ADD COLUMN response_framing TEXT;
ALTER TABLE request ADD COLUMN request_stream_ending TEXT;
ALTER TABLE request ADD COLUMN response_stream_ending TEXT;
UPDATE request SET
    request_framing = json_extract(metadata, '$.streamed.request.framing'),
    response_framing = json_extract(metadata, '$.streamed.response.framing'),
    request_stream_ending = json_extract(metadata, '$.streamed.request.ending'),
    response_stream_ending = json_extract(metadata, '$.streamed.response.ending')
WHERE json_valid(metadata) AND json_type(metadata, '$.streamed') = 'object';

-- +goose Down
ALTER TABLE request DROP COLUMN response_stream_ending;
ALTER TABLE request DROP COLUMN request_stream_ending;
ALTER TABLE request DROP COLUMN response_framing;
ALTER TABLE request DROP COLUMN request_framing;
