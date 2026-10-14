package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upClientSettings, downClientSettings)
}

// legacyExclusionClient and legacyExclusionKey address the content-type
// exclusion that app.filters held (ADR-0037).
const (
	legacyExclusionClient = "marasi"
	legacyExclusionKey    = "legacy.content_type_exclusion"
)

// seededFilters is the app.filters default from 00001_db_setup.sql.
var seededFilters = []string{
	"audio/aac", "image/apng", "image/avif", "video/x-msvideo", "image/bmp",
	"audio/midi", "audio/x-midi", "image/gif", "image/vnd.microsoft.icon",
	"image/jpeg", "audio/mpeg", "video/mp4", "video/mpeg", "audio/ogg",
	"video/ogg", "image/png", "image/svg+xml", "image/tiff", "video/mp2t",
	"audio/wav", "audio/webm", "video/webm", "image/webp", "video/3gpp",
	"audio/3gpp", "video/3gpp2", "audio/3gpp2", "application/vnd.ms-fontobject",
	"font/otf", "font/ttf", "font/woff", "font/woff2", "application/x-abiword",
	"application/vnd.amazon.ebook", "application/x-cdf", "application/x-csh",
	"application/epub+zip", "application/vnd.apple.installer+xml",
	"application/vnd.oasis.opendocument.presentation",
	"application/vnd.oasis.opendocument.spreadsheet",
	"application/vnd.oasis.opendocument.text", "application/vnd.visio",
	"application/vnd.mozilla.xul+xml", "application/javascript",
	"application/octet-stream", "binary/octet-stream", "image/x-icon",
	"image/vnd.djvu", "image/x-portable-pixmap", "image/x-portable-bitmap",
	"audio/basic", "audio/aiff", "audio/x-aiff", "audio/x-wav",
	"audio/x-mpegurl", "audio/x-ms-wma", "video/quicktime", "video/x-flv",
	"video/x-ms-wmv", "video/x-msvideo", "application/pdf", "application/msword",
	"application/vnd.ms-excel", "application/vnd.ms-powerpoint",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/rtf", "application/x-rar-compressed",
	"application/x-7z-compressed", "application/zip", "application/x-tar",
	"application/gzip", "application/x-font-ttf", "application/x-font-woff",
	"text/css", "text/calendar", "text/vcard", "application/x-shockwave-flash",
	"application/x-bzip", "application/x-bzip2",
}

// upClientSettings creates client_setting, moves an edited app.filters to
// marasi/legacy.content_type_exclusion, and drops app.filters. A value with
// the same members as the seeded default is not moved.
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

	var filters sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT filters FROM app LIMIT 1`).Scan(&filters)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading app.filters: %w", err)
	}
	if value, moved := legacyExclusionValue(filters); moved {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO client_setting (client, key, value, updated_at) VALUES (?, ?, ?, ?)`,
			legacyExclusionClient, legacyExclusionKey, value, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("moving app.filters: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `ALTER TABLE app DROP COLUMN filters`); err != nil {
		return fmt.Errorf("dropping app.filters: %w", err)
	}
	return nil
}

// legacyExclusionValue returns the JSON to store for filters, and false when
// there is nothing to move: no value, or the seeded members in any order.
func legacyExclusionValue(filters sql.NullString) (string, bool) {
	if !filters.Valid {
		return "", false
	}
	var members []string
	if err := json.Unmarshal([]byte(filters.String), &members); err != nil {
		// Not a list of strings. Keep it rather than lose it.
		if json.Valid([]byte(filters.String)) {
			return filters.String, true
		}
		encoded, _ := json.Marshal(filters.String)
		return string(encoded), true
	}
	if sameMembers(members, seededFilters) {
		return "", false
	}
	return filters.String, true
}

func sameMembers(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// downClientSettings restores app.filters with the seeded default, puts back
// a moved value, and drops client_setting with every client's settings.
func downClientSettings(ctx context.Context, tx *sql.Tx) error {
	seeded, err := json.Marshal(seededFilters)
	if err != nil {
		return fmt.Errorf("encoding seeded filters: %w", err)
	}
	defaultLiteral := "'" + strings.ReplaceAll(string(seeded), "'", "''") + "'"
	if _, err := tx.ExecContext(ctx, `ALTER TABLE app ADD COLUMN filters JSON DEFAULT `+defaultLiteral); err != nil {
		return fmt.Errorf("restoring app.filters: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE app SET filters = (SELECT value FROM client_setting WHERE client = ?1 AND key = ?2)
		WHERE EXISTS (SELECT 1 FROM client_setting WHERE client = ?1 AND key = ?2)`,
		legacyExclusionClient, legacyExclusionKey)
	if err != nil {
		return fmt.Errorf("restoring moved filters: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE client_setting`); err != nil {
		return fmt.Errorf("dropping client_setting table: %w", err)
	}
	return nil
}
