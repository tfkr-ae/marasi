package db

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/tfkr-ae/marasi/domain"
)

// clientSettingsMigrationVersion is the migration that moves app.filters.
const clientSettingsMigrationVersion = 12

const legacyExclusionKey = "legacy.content_type_exclusion"

func TestFiltersMigration(t *testing.T) {
	t.Run("should leave no marasi key for a project with the seeded filters", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		assertNoMarasiSettings(t, repo)
		assertNoFiltersColumn(t, repo)
	})

	t.Run("should leave no marasi key when the seeded filters are reordered and reformatted", func(t *testing.T) {
		repo := migrateWithFilters(t, func(seeded []string) string {
			reordered := slices.Clone(seeded)
			slices.Reverse(reordered)
			reordered = append(reordered, seeded[0])
			encoded, err := json.Marshal(reordered)
			if err != nil {
				t.Fatalf("encoding filters: %v", err)
			}
			return string(encoded)
		})

		assertNoMarasiSettings(t, repo)
		assertNoFiltersColumn(t, repo)
	})

	t.Run("should move an edited list to the marasi legacy key", func(t *testing.T) {
		repo := migrateWithFilters(t, func([]string) string {
			return `["text/css", "image/png",  "application/x-custom"]`
		})

		got, err := repo.GetClientSetting(domain.ReservedClient, legacyExclusionKey)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		var members []string
		if err := json.Unmarshal(got.Value, &members); err != nil {
			t.Fatalf("decoding %s: %v", got.Value, err)
		}
		slices.Sort(members)
		want := []string{"application/x-custom", "image/png", "text/css"}
		if !slices.Equal(members, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, members)
		}
		assertNoFiltersColumn(t, repo)
	})

	t.Run("should move an emptied list to the marasi legacy key", func(t *testing.T) {
		repo := migrateWithFilters(t, func([]string) string { return `[]` })

		got, err := repo.GetClientSetting(domain.ReservedClient, legacyExclusionKey)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if string(got.Value) != `[]` {
			t.Fatalf("\nwanted:\n[]\ngot:\n%s", got.Value)
		}
	})

	t.Run("should put a moved list back into app.filters when rolled back", func(t *testing.T) {
		repo := migrateWithFilters(t, func([]string) string { return `["text/css"]` })

		migrationsFS, err := fs.Sub(embedMigrations, "migrations")
		if err != nil {
			t.Fatalf("migrations fs: %v", err)
		}
		migrateMu.Lock()
		provider, err := goose.NewProvider(goose.DialectSQLite3, repo.dbConn.DB, migrationsFS)
		if err == nil {
			_, err = provider.DownTo(context.Background(), clientSettingsMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("rolling back: %v", err)
		}

		var filters string
		if err := repo.dbConn.Get(&filters, `SELECT filters FROM app`); err != nil {
			t.Fatalf("reading app.filters: %v", err)
		}
		if filters != `["text/css"]` {
			t.Fatalf("\nwanted:\n[\"text/css\"]\ngot:\n%s", filters)
		}
	})
}

// migrateWithFilters migrates a new project to the version before the
// client settings migration, replaces app.filters with edit(seeded), and
// then opens it with New so the remaining migrations run.
func migrateWithFilters(t *testing.T, edit func(seeded []string) string) *Repository {
	t.Helper()
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
		_, err = provider.UpTo(context.Background(), clientSettingsMigrationVersion-1)
	}
	migrateMu.Unlock()
	if err != nil {
		t.Fatalf("migrating to %d: %v", clientSettingsMigrationVersion-1, err)
	}

	var seededText string
	if err := conn.Get(&seededText, `SELECT filters FROM app`); err != nil {
		t.Fatalf("reading seeded filters: %v", err)
	}
	var seeded []string
	if err := json.Unmarshal([]byte(seededText), &seeded); err != nil {
		t.Fatalf("decoding seeded filters: %v", err)
	}
	if _, err := conn.Exec(`UPDATE app SET filters = ?`, edit(seeded)); err != nil {
		t.Fatalf("editing filters: %v", err)
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
	return repo
}

func assertNoMarasiSettings(t *testing.T, repo *Repository) {
	t.Helper()
	settings, err := repo.ListClientSettings(domain.ReservedClient, "")
	if err != nil {
		t.Fatalf("wanted: nil\ngot: %v", err)
	}
	if len(settings) != 0 {
		t.Fatalf("wanted: no marasi settings\ngot: %v", settings)
	}
}

func assertNoFiltersColumn(t *testing.T, repo *Repository) {
	t.Helper()
	var count int
	if err := repo.dbConn.Get(&count, `SELECT count(*) FROM pragma_table_info('app') WHERE name = 'filters'`); err != nil {
		t.Fatalf("reading app columns: %v", err)
	}
	if count != 0 {
		t.Fatalf("wanted: app.filters dropped\ngot: column present")
	}
}
