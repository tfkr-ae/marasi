package db

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// AppendNote gives every pair in sel with no note the note text, and adds a
// blank line and text to the note of every pair that has one. It returns the
// number of notes written. An empty text returns domain.ErrEmptyNote.
func (repo *Repository) AppendNote(sel domain.Selection, text string) (int, error) {
	if text == "" {
		return 0, domain.ErrEmptyNote
	}
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		ids, err := selectedIDs(tx, `SELECT id FROM (`+selected+`)`, args)
		if err != nil {
			return 0, err
		}
		query := `INSERT INTO notes (request_id, note, created_at)
			SELECT id, ?, CURRENT_TIMESTAMP FROM (` + selected + `) WHERE true
			ON CONFLICT(request_id) DO UPDATE SET
				note = CASE WHEN note IS NULL OR note = '' THEN excluded.note
					ELSE note || char(10, 10) || excluded.note END,
				created_at = CURRENT_TIMESTAMP`
		count, err := execCount(tx, "appending notes", query, append([]any{text}, args...)...)
		if err != nil {
			return 0, err
		}
		return count, reindexNotes(tx, ids)
	})
}

// ClearNotes deletes the note of every pair in sel and returns the number of
// notes deleted.
func (repo *Repository) ClearNotes(sel domain.Selection) (int, error) {
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		ids, err := selectedIDs(tx, `SELECT request_id FROM notes WHERE request_id IN (`+selected+`)`, args)
		if err != nil {
			return 0, err
		}
		count, err := execCount(tx, "clearing notes", `DELETE FROM notes WHERE request_id IN (`+selected+`)`, args...)
		if err != nil {
			return 0, err
		}
		return count, reindexNotes(tx, ids)
	})
}

// selectedIDs runs query, which selects one id column, in tx.
func selectedIDs(tx *sqlx.Tx, query string, args []any) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	if err := tx.Select(&ids, query, args...); err != nil {
		return nil, fmt.Errorf("reading selected pairs: %w", err)
	}
	return ids, nil
}

// reindexNotes rewrites the annotation index rows of pairs whose note
// changed. Their bodies did not change, so the body index is left alone.
func reindexNotes(tx *sqlx.Tx, ids []uuid.UUID) error {
	for _, id := range ids {
		if err := indexAnnotations(tx, id); err != nil {
			return err
		}
	}
	return nil
}
