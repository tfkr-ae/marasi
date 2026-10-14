package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upClientSettings, downClientSettings)
}

func upClientSettings(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE client_setting (
			client TEXT NOT NULL,
			key TEXT NOT NULL,
			value JSON NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (client, key)
		)`)
	if err != nil {
		return fmt.Errorf("creating client_setting table: %w", err)
	}
	return nil
}

func downClientSettings(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE client_setting`); err != nil {
		return fmt.Errorf("dropping client_setting table: %w", err)
	}
	return nil
}
