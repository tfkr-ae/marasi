package migrations

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func init() {
	goose.AddMigrationContext(upMigrateToBlob, downMigrateRawToBlob)
}

func upMigrateToBlob(ctx context.Context, tx *sql.Tx) error {
	alterQuery := `
		ALTER TABLE request ADD COLUMN request_raw_blob BLOB;
	    ALTER TABLE request ADD COLUMN response_raw_blob BLOB;
	`
	_, err := tx.Exec(alterQuery)
	if err != nil {
		return fmt.Errorf("adding new blob columns : %w", err)
	}
	rows, err := tx.Query("SELECT id, request_raw, response_raw FROM request")
	if err != nil {
		return fmt.Errorf("getting all rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var requestRawText, responseRawText sql.NullString
		err := rows.Scan(&id, &requestRawText, &responseRawText)
		if err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		var requestRawBytes []byte
		if requestRawText.Valid && requestRawText.String != "" {
			requestRawBytes, err = base64.StdEncoding.DecodeString(requestRawText.String)
			if err != nil {
				return fmt.Errorf("decoding requestRawText for row %s : %w", id, err)
			}
		}

		var responseRawBytes []byte
		if responseRawText.Valid && responseRawText.String != "" {
			responseRawBytes, err = base64.StdEncoding.DecodeString(responseRawText.String)
			if err != nil {
				return fmt.Errorf("decoding responseRawText for row %s : %w", id, err)
			}
		}
		_, err = tx.Exec("UPDATE request SET request_raw_blob = ?, response_raw_blob = ? WHERE id = ?", requestRawBytes, responseRawBytes, id)
		if err != nil {
			return fmt.Errorf("updating row %s : %w", id, err)
		}
	}
	err = rows.Err()
	if err != nil {
		return fmt.Errorf("iterating rows: %w", err)
	}
	_, err = tx.Exec("ALTER TABLE request DROP COLUMN request_raw")
	if err != nil {
		return fmt.Errorf("dropping request raw column : %w", err)
	}
	_, err = tx.Exec("ALTER TABLE request DROP COLUMN response_raw")
	if err != nil {
		return fmt.Errorf("dropping response raw column : %w", err)
	}
	_, err = tx.Exec("ALTER TABLE request RENAME COLUMN request_raw_blob TO request_raw")
	if err != nil {
		return fmt.Errorf("renaming request_raw_blob column: %w", err)
	}
	_, err = tx.Exec("ALTER TABLE request RENAME COLUMN response_raw_blob TO response_raw")
	if err != nil {
		return fmt.Errorf("renaming response_raw_blob column: %w", err)
	}
	return nil
}

func downMigrateRawToBlob(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.Exec(`
        ALTER TABLE request ADD COLUMN request_raw_text TEXT;
        ALTER TABLE request ADD COLUMN response_raw_text TEXT;
    `)

	if err != nil {
		return fmt.Errorf("failed to add new text columns for rollback: %w", err)
	}

	rows, err := tx.Query("SELECT id, request_raw, response_raw FROM request")

	if err != nil {
		return fmt.Errorf("failed to query existing requests for rollback: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var requestRawBytes, responseRawBytes []byte

		if err := rows.Scan(&id, &requestRawBytes, &responseRawBytes); err != nil {
			return fmt.Errorf("failed to scan row for rollback: %w", err)
		}

		requestRawText := base64.StdEncoding.EncodeToString(requestRawBytes)
		responseRawText := base64.StdEncoding.EncodeToString(responseRawBytes)

		_, err = tx.Exec(
			"UPDATE request SET request_raw_text = ?, response_raw_text = ? WHERE id = ?",
			requestRawText,
			responseRawText,
			id,
		)
		if err != nil {
			return fmt.Errorf("failed to update row for id %s for rollback: %w", id, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("row iteration error during rollback: %w", err)
	}

	if _, err := tx.Exec(`ALTER TABLE request DROP COLUMN request_raw`); err != nil {
		return fmt.Errorf("failed to drop blob request_raw column for rollback: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE request DROP COLUMN response_raw`); err != nil {
		return fmt.Errorf("failed to drop blob response_raw column for rollback: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE request RENAME COLUMN request_raw_text TO request_raw`); err != nil {
		return fmt.Errorf("failed to rename request_raw_text column for rollback: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE request RENAME COLUMN response_raw_text TO response_raw`); err != nil {
		return fmt.Errorf("failed to rename response_raw_text column for rollback: %w", err)
	}

	return nil
}
