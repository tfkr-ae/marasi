package db

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/tfkr-ae/marasi/domain"
)

// entryIDs lists launchpadID's entries without an orderBy.
func entryIDs(t *testing.T, repo *Repository, launchpadID uuid.UUID, query string) []uuid.UUID {
	t.Helper()
	entries, _, err := repo.GetLaunchpadRequests(launchpadID, query, nil, "")
	if err != nil {
		t.Fatalf("listing launchpad %s: %v", launchpadID, err)
	}
	ids := make([]uuid.UUID, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	return ids
}

func assertEntries(t *testing.T, got, want []uuid.UUID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
	}
}

func TestLaunchpadEntryOrder(t *testing.T) {
	t.Parallel()
	t.Run("should list a single link after the current last entry", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Order", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		older := testRequest(t, repo, nil)
		newer := testRequest(t, repo, nil)
		for _, id := range []uuid.UUID{newer, older} {
			if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
				t.Fatalf("linking %s: %v", id, err)
			}
		}

		assertEntries(t, entryIDs(t, repo, launchpadID, ""), []uuid.UUID{newer, older})
	})

	t.Run("should list a bulk link after the current last entry in capture order", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Order", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		first := testRequest(t, repo, nil)
		second := testRequest(t, repo, nil)
		third := testRequest(t, repo, nil)
		linked := testRequest(t, repo, nil)
		if err := repo.LinkRequestToLaunchpad(linked, launchpadID); err != nil {
			t.Fatalf("linking %s: %v", linked, err)
		}

		count, err := repo.LinkTraffic(
			domain.Selection{IDs: []uuid.UUID{third, linked, first, second}},
			domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: launchpadID})
		if err != nil {
			t.Fatalf("LinkTraffic() error = %v", err)
		}
		if count != 3 {
			t.Fatalf("\nwanted:\n3 linked\ngot:\n%d", count)
		}

		assertEntries(t, entryIDs(t, repo, launchpadID, ""), []uuid.UUID{linked, first, second, third})
	})

	t.Run("should sort by an explicit orderBy and narrow by a query", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Order", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		first := insertTraffic(t, repo, "GET", "a.example.com", "/1")
		second := insertTraffic(t, repo, "POST", "a.example.com", "/2")
		third := insertTraffic(t, repo, "POST", "a.example.com", "/3")
		for _, id := range []uuid.UUID{second, third, first} {
			if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
				t.Fatalf("linking %s: %v", id, err)
			}
		}

		entries, _, err := repo.GetLaunchpadRequests(launchpadID, "", nil, "path")
		if err != nil {
			t.Fatalf("listing by path: %v", err)
		}
		got := make([]uuid.UUID, len(entries))
		for i, entry := range entries {
			got[i] = entry.ID
		}
		assertEntries(t, got, []uuid.UUID{first, second, third})
		assertEntries(t, entryIDs(t, repo, launchpadID, `method = "POST"`), []uuid.UUID{second, third})
		assertEntries(t, entryIDs(t, repo, launchpadID, `method = "GET"`), []uuid.UUID{first})
	})

	t.Run("should take the last status code from the newest entry by capture", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Order", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		older := insertTraffic(t, repo, "GET", "a.example.com", "/older")
		newer := insertTraffic(t, repo, "GET", "a.example.com", "/newer")
		insertTrafficResponse(t, repo, older, 404)
		insertTrafficResponse(t, repo, newer, 201)
		for _, id := range []uuid.UUID{newer, older} {
			if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
				t.Fatalf("linking %s: %v", id, err)
			}
		}

		launchpad, err := repo.GetLaunchpad(launchpadID)
		if err != nil {
			t.Fatalf("GetLaunchpad() error = %v", err)
		}
		if launchpad.LastStatusCode == nil || *launchpad.LastStatusCode != 201 {
			t.Fatalf("\nwanted:\n201\ngot:\n%v", launchpad.LastStatusCode)
		}
	})

	t.Run("should keep the place of a pair that is already an entry", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("Order", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		first := testRequest(t, repo, nil)
		second := testRequest(t, repo, nil)
		for _, id := range []uuid.UUID{first, second} {
			if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
				t.Fatalf("linking %s: %v", id, err)
			}
		}

		if err := repo.LinkRequestToLaunchpad(first, launchpadID); !errors.Is(err, domain.ErrLaunchpadAlreadyLinked) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrLaunchpadAlreadyLinked, err)
		}
		_, err = repo.LinkTraffic(
			domain.Selection{Query: &domain.SelectionQuery{AsOf: second}},
			domain.LinkTarget{Kind: domain.LinkLaunchpad, ID: launchpadID})
		if err != nil {
			t.Fatalf("LinkTraffic() error = %v", err)
		}

		assertEntries(t, entryIDs(t, repo, launchpadID, ""), []uuid.UUID{first, second})
	})
}

// entryOrderMigrationVersion is the migration that gives launchpad links
// their entry order.
const entryOrderMigrationVersion = 25

func TestEntryOrderMigration(t *testing.T) {
	t.Parallel()
	t.Run("should keep each launchpad's existing links in capture order", func(t *testing.T) {
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
			_, err = provider.UpTo(context.Background(), entryOrderMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", entryOrderMigrationVersion-1, err)
		}

		exec := func(query string, args ...any) {
			t.Helper()
			if _, err := conn.Exec(query, args...); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
		}
		pairs := make([]uuid.UUID, 4)
		for i := range pairs {
			pairs[i] = uuid.Must(uuid.NewV7())
			exec(`INSERT INTO request(id, scheme, method, host, path, request_raw, requested_at) VALUES(?, 'https', 'GET', 'example.com', '/', '', ?)`, pairs[i], time.Now())
		}
		first, second, empty := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		for _, id := range []uuid.UUID{first, second, empty} {
			exec(`INSERT INTO launchpad(id, name, description) VALUES(?, 'lp', '')`, id)
		}
		// Linked out of capture order, as the old schema allowed.
		for _, link := range []struct{ pair, launchpad uuid.UUID }{
			{pairs[3], first}, {pairs[0], first}, {pairs[2], first},
			{pairs[2], second}, {pairs[1], second},
		} {
			exec(`INSERT INTO launchpad_request(request_id, launchpad_id) VALUES(?, ?)`, link.pair, link.launchpad)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		dbConn, err := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("db.New() failed: %v", err)
		}
		repo := NewProxyRepo(dbConn)
		t.Cleanup(func() { repo.Close() })

		assertEntries(t, entryIDs(t, repo, first, ""), []uuid.UUID{pairs[0], pairs[2], pairs[3]})
		assertEntries(t, entryIDs(t, repo, second, ""), []uuid.UUID{pairs[1], pairs[2]})
		assertEntries(t, entryIDs(t, repo, empty, ""), []uuid.UUID{})

		latest := uuid.Must(uuid.NewV7())
		if err := repo.InsertRequest(&domain.ProxyRequest{ID: latest, Scheme: "https", Method: "GET", Host: "example.com", Path: "/", Raw: []byte("GET / HTTP/1.1\r\n\r\n"), RequestedAt: time.Now()}); err != nil {
			t.Fatalf("inserting request: %v", err)
		}
		if err := repo.LinkRequestToLaunchpad(pairs[1], first); err != nil {
			t.Fatalf("linking after the migration: %v", err)
		}
		if err := repo.LinkRequestToLaunchpad(latest, second); err != nil {
			t.Fatalf("linking after the migration: %v", err)
		}
		assertEntries(t, entryIDs(t, repo, first, ""), []uuid.UUID{pairs[0], pairs[2], pairs[3], pairs[1]})
		assertEntries(t, entryIDs(t, repo, second, ""), []uuid.UUID{pairs[1], pairs[2], latest})
	})

	t.Run("should keep keys unique per launchpad through an index led by the launchpad id", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		var columns []string
		err := repo.dbConn.Select(&columns, `
			SELECT info.name FROM pragma_index_list('launchpad_request') AS list
			JOIN pragma_index_info(list.name) AS info
			WHERE list."unique" AND list.origin = 'c'
			ORDER BY info.seqno`)
		if err != nil {
			t.Fatalf("reading launchpad_request indexes: %v", err)
		}
		if !slices.Equal(columns, []string{"launchpad_id", "position"}) {
			t.Fatalf("\nwanted:\nunique index on (launchpad_id, position)\ngot:\n%v", columns)
		}
	})
}
