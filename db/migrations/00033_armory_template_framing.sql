-- +goose Up
-- An Armory template that names a request message framing (grpc, grpc-web
-- or connect) has a message-form body (GLOSSARY): each request message in
-- its text form under a separator, encoded and enveloped for every request
-- a run sends. A run keeps the framing with its template snapshot. NULL is
-- a template with no framing, rendered as raw text as before, so existing
-- rows need no backfill.
ALTER TABLE armory_template ADD COLUMN request_framing TEXT;
ALTER TABLE armory_run ADD COLUMN template_framing TEXT;

-- +goose Down
ALTER TABLE armory_run DROP COLUMN template_framing;
ALTER TABLE armory_template DROP COLUMN request_framing;
