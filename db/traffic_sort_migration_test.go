package db

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/tfkr-ae/marasi/domain"
)

// sortMigrationVersion is the migration that adds the sort indexes and
// clears the length of pairs with no response.
const sortMigrationVersion = 17

func TestSortMigration(t *testing.T) {
	t.Run("should sort pairs stored before it by length, with no response last", func(t *testing.T) {
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
			_, err = provider.UpTo(context.Background(), sortMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", sortMigrationVersion-1, err)
		}

		// As older binaries stored them: an in-flight pair takes the '0'
		// default, and a response with no Content-Length stores NULL.
		insert := func(length any, responded bool) uuid.UUID {
			t.Helper()
			time.Sleep(time.Millisecond)
			id := uuid.Must(uuid.NewV7())
			_, err := conn.Exec(`INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at) VALUES(?, 'https', 'GET', 'example.com', '/', '', ?)`, id, time.Now())
			if err != nil {
				t.Fatalf("inserting: %v", err)
			}
			if responded {
				_, err = conn.Exec(`UPDATE request SET status_code = 200, length = ?, responded_at = ? WHERE id = ?`, length, time.Now(), id)
				if err != nil {
					t.Fatalf("responding: %v", err)
				}
			}
			return id
		}
		inFlight := insert(nil, false)
		big := insert("100", true)
		noHeader := insert(nil, true)
		small := insert("20", true)
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		dbConn, err := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("db.New() failed: %v", err)
		}
		repo := NewProxyRepo(dbConn)
		t.Cleanup(func() { repo.Close() })

		for orderBy, want := range map[string][]uuid.UUID{
			"length":      {noHeader, small, big, inFlight},
			"length desc": {big, small, noHeader, inFlight},
		} {
			page, err := repo.ListTraffic(domain.TrafficQuery{OrderBy: orderBy, Limit: 10})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if got := idsOf(page.Items); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s\nwanted:\n%v\ngot:\n%v", orderBy, want, got)
			}
		}
	})
}
