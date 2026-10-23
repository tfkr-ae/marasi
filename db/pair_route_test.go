package db

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/tfkr-ae/marasi/domain"
)

// pairRouteMigrationVersion is the migration that adds route snapshots.
const pairRouteMigrationVersion = 26

func TestPairRouteMigration(t *testing.T) {
	t.Run("should leave a pair captured before the migration with no route", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "project.db")
		conn, err := sqlx.Connect("sqlite", path)
		if err != nil {
			t.Fatalf("connecting: %v", err)
		}
		migrationsFS, err := fs.Sub(embedMigrations, "migrations")
		if err != nil {
			t.Fatalf("migrations fs: %v", err)
		}
		migrateMu.Lock()
		provider, err := goose.NewProvider(goose.DialectSQLite3, conn.DB, migrationsFS)
		if err == nil {
			_, err = provider.UpTo(context.Background(), pairRouteMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", pairRouteMigrationVersion-1, err)
		}
		id := uuid.Must(uuid.NewV7())
		if _, err := conn.Exec(`INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at, status, status_code, response_raw, responded_at)
			VALUES(?, 'https', 'GET', 'example.com', '/', '', ?, '200 OK', 200, '', ?)`, id, time.Now(), time.Now()); err != nil {
			t.Fatalf("inserting: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}

		repo := openProject(t, path)
		row, err := repo.GetRequestResponseRow(id)
		if err != nil {
			t.Fatalf("GetRequestResponseRow() error = %v", err)
		}
		if row.Route != nil {
			t.Fatalf("\nwanted:\nno route\ngot:\n%+v", row.Route)
		}
		for _, query := range []string{`upstream_proxy = ""`, `upstream_proxy = "*"`} {
			count, err := repo.CountTraffic(domain.TrafficCountQuery{Text: query})
			if err != nil {
				t.Fatalf("CountTraffic(%s) error = %v", query, err)
			}
			if count.Count != 0 {
				t.Fatalf("\nwanted:\n%s to match nothing\ngot:\n%d", query, count.Count)
			}
		}
	})
}
