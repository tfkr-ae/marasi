-- +goose Up
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS note_deleted
AFTER DELETE ON notes
FOR EACH ROW
BEGIN
    UPDATE request
    SET metadata = json_remove(metadata, '$.has_note')
    WHERE id = OLD.request_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS note_deleted;
-- +goose StatementEnd
