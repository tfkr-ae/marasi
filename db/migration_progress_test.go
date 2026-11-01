package db

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
)

func migrationNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, pattern := range []string{"migrations/*.sql", "migrations/*.go"} {
		matches, err := fs.Glob(embedMigrations, pattern)
		if err != nil {
			t.Fatalf("listing migrations: %v", err)
		}
		for _, match := range matches {
			names = append(names, filepath.Base(match))
		}
	}
	slices.Sort(names)
	return names
}

func TestNewMigrationProgress(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("should report each migration of a new project with the total", func(t *testing.T) {
		t.Parallel()
		var steps []MigrationStep
		connection, err := New(filepath.Join(t.TempDir(), "new.marasi"), logger, WithMigrationProgress(func(step MigrationStep) {
			steps = append(steps, step)
		}))
		if err != nil {
			t.Fatalf("opening: %v", err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		names := migrationNames(t)
		want := make([]MigrationStep, len(names))
		for i, name := range names {
			want[i] = MigrationStep{Migration: name, Applied: i + 1, Total: len(names)}
		}
		if !slices.Equal(steps, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, steps)
		}
	})

	t.Run("should count only the pending migrations of an older project", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "old.marasi")
		conn, err := sqlx.Connect("sqlite", path)
		if err != nil {
			t.Fatalf("connecting: %v", err)
		}
		migrationsFS, err := fs.Sub(embedMigrations, "migrations")
		if err != nil {
			t.Fatalf("migrations fs: %v", err)
		}
		names := migrationNames(t)
		applied := len(names) - 2
		migrateMu.Lock()
		provider, err := goose.NewProvider(goose.DialectSQLite3, conn.DB, migrationsFS)
		if err == nil {
			_, err = provider.UpTo(context.Background(), int64(applied))
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", applied, err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		var steps []MigrationStep
		connection, err := New(path, logger, WithMigrationProgress(func(step MigrationStep) {
			steps = append(steps, step)
		}))
		if err != nil {
			t.Fatalf("opening: %v", err)
		}
		want := []MigrationStep{
			{Migration: names[applied], Applied: 1, Total: 2},
			{Migration: names[applied+1], Applied: 2, Total: 2},
		}
		if !slices.Equal(steps, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, steps)
		}
		if err := connection.Close(); err != nil {
			t.Fatalf("closing: %v", err)
		}

		steps = nil
		connection, err = New(path, logger, WithMigrationProgress(func(step MigrationStep) {
			steps = append(steps, step)
		}))
		if err != nil {
			t.Fatalf("reopening: %v", err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		if len(steps) != 0 {
			t.Fatalf("\nwanted:\nno steps for an up-to-date project\ngot:\n%v", steps)
		}
	})

	t.Run("should apply every migration while progress is blocked", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "blocked.marasi")
		unblock := make(chan struct{})
		opened := make(chan error, 1)
		var steps []MigrationStep
		go func() {
			connection, err := New(path, logger, WithMigrationProgress(func(step MigrationStep) {
				<-unblock
				steps = append(steps, step)
			}))
			if err == nil {
				t.Cleanup(func() { _ = connection.Close() })
			}
			opened <- err
		}()

		// Read the version table beside the opening connection: every
		// migration lands while the first step is still undelivered.
		names := migrationNames(t)
		latest := names[len(names)-1]
		var wantVersion int64
		if _, err := fmt.Sscanf(latest, "%d_", &wantVersion); err != nil {
			t.Fatalf("parsing %s: %v", latest, err)
		}
		reader, err := sqlx.Connect("sqlite", path+"?_pragma=busy_timeout(5000)")
		if err != nil {
			close(unblock)
			t.Fatalf("connecting reader: %v", err)
		}
		defer reader.Close()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var version int64
			err := reader.Get(&version, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version`)
			if err == nil && version == wantVersion {
				break
			}
			if time.Now().After(deadline) {
				close(unblock)
				t.Fatalf("\nwanted:\nversion %d while progress is blocked\ngot:\n%d %v", wantVersion, version, err)
			}
			time.Sleep(5 * time.Millisecond)
		}
		select {
		case err := <-opened:
			t.Fatalf("\nwanted:\nNew to wait for undelivered steps\ngot:\nreturned %v", err)
		default:
		}
		close(unblock)
		if err := <-opened; err != nil {
			t.Fatalf("opening: %v", err)
		}
		if len(steps) != len(names) {
			t.Fatalf("\nwanted:\n%d steps\ngot:\n%d", len(names), len(steps))
		}
	})
}
