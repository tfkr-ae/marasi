package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	"roci.dev/fracdex"
)

func init() {
	goose.AddMigrationContext(upLaunchpadEntryOrder, downLaunchpadEntryOrder)
}

// upLaunchpadEntryOrder rebuilds launchpad_request with a position column
// that holds each link's entry-order key, a fractional key in its
// launchpad's entry order (ADR-0052), and backfills each launchpad's links in
// request id order, which is capture order.
func upLaunchpadEntryOrder(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE launchpad_request_ordered (
			request_id TEXT,
			launchpad_id TEXT,
			position TEXT NOT NULL,
			PRIMARY KEY (request_id, launchpad_id),
			FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE,
			FOREIGN KEY (launchpad_id) REFERENCES launchpad(id) ON DELETE CASCADE
		)`)
	if err != nil {
		return fmt.Errorf("creating ordered launchpad_request: %w", err)
	}

	links, err := launchpadLinksInCaptureOrder(ctx, tx)
	if err != nil {
		return err
	}
	for launchpadID, requestIDs := range links {
		keys, err := fracdex.NKeysBetween("", "", uint(len(requestIDs)))
		if err != nil {
			return fmt.Errorf("generating entry order for launchpad %s: %w", launchpadID, err)
		}
		for i, requestID := range requestIDs {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO launchpad_request_ordered (request_id, launchpad_id, position) VALUES (?, ?, ?)`,
				requestID, launchpadID, keys[i])
			if err != nil {
				return fmt.Errorf("backfilling launchpad %s: %w", launchpadID, err)
			}
		}
	}

	for _, statement := range []string{
		`DROP TABLE launchpad_request`,
		`ALTER TABLE launchpad_request_ordered RENAME TO launchpad_request`,
		`CREATE UNIQUE INDEX launchpad_request_position ON launchpad_request (launchpad_id, position)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("replacing launchpad_request: %w", err)
		}
	}
	return nil
}

// launchpadLinksInCaptureOrder returns each launchpad's linked request ids
// in request id order.
func launchpadLinksInCaptureOrder(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT launchpad_id, request_id FROM launchpad_request ORDER BY launchpad_id, request_id`)
	if err != nil {
		return nil, fmt.Errorf("reading launchpad links: %w", err)
	}
	defer rows.Close()
	links := make(map[string][]string)
	for rows.Next() {
		var launchpadID, requestID string
		if err := rows.Scan(&launchpadID, &requestID); err != nil {
			return nil, fmt.Errorf("reading launchpad link: %w", err)
		}
		links[launchpadID] = append(links[launchpadID], requestID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading launchpad links: %w", err)
	}
	return links, nil
}

// downLaunchpadEntryOrder rebuilds launchpad_request without an entry-order key,
// dropping every launchpad's entry order.
func downLaunchpadEntryOrder(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE launchpad_request_unordered (
			request_id TEXT,
			launchpad_id TEXT,
			PRIMARY KEY (request_id, launchpad_id),
			FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE,
			FOREIGN KEY (launchpad_id) REFERENCES launchpad(id) ON DELETE CASCADE
		)`,
		`INSERT INTO launchpad_request_unordered (request_id, launchpad_id) SELECT request_id, launchpad_id FROM launchpad_request`,
		`DROP TABLE launchpad_request`,
		`ALTER TABLE launchpad_request_unordered RENAME TO launchpad_request`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("removing launchpad entry order: %w", err)
		}
	}
	return nil
}
