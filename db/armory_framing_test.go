package db

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/tfkr-ae/marasi/domain"
)

// armoryFramingMigrationVersion adds the request framing to Armory templates
// and their runs.
const armoryFramingMigrationVersion = 33

func TestArmoryRepo_Framing(t *testing.T) {
	t.Run("should store a template's framing and its run's snapshot of it", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		template := &domain.ArmoryTemplate{
			ID:          armoryTestUUID(t),
			Name:        "gRPC",
			RawTemplate: "POST /test.Echo/Say HTTP/2\r\n:authority: marasi.app\r\ncontent-type: application/grpc\r\n\r\n--- message ---\n1:LEN {\"a\"}\n",
			Framing:     "grpc",
		}
		if err := repo.CreateArmoryTemplate(template); err != nil {
			t.Fatalf("creating armory template: %v", err)
		}
		got, err := repo.GetArmoryTemplate(template.ID)
		if err != nil || !reflect.DeepEqual(template, got) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v (%v)", template, got, err)
		}
		listed, err := repo.GetArmoryTemplates()
		if err != nil || len(listed) != 1 || listed[0].Framing != "grpc" {
			t.Fatalf("the listed template\nwanted:\nframing grpc\ngot:\n%+v (%v)", listed, err)
		}

		template.Framing = ""
		if err := repo.UpdateArmoryTemplate(template); err != nil {
			t.Fatalf("updating armory template: %v", err)
		}
		if got, err := repo.GetArmoryTemplate(template.ID); err != nil || got.Framing != "" {
			t.Fatalf("the updated template\nwanted:\nno framing\ngot:\n%+v (%v)", got, err)
		}

		run := armoryTestRun(t, repo, template)
		run.TemplateFraming = "grpc"
		run.ID = armoryTestUUID(t)
		if err := repo.CreateArmoryRun(run); err != nil {
			t.Fatalf("creating armory run: %v", err)
		}
		if got, err := repo.GetArmoryRun(run.ID); err != nil || got.TemplateFraming != "grpc" {
			t.Fatalf("the run\nwanted:\ntemplate framing grpc\ngot:\n%+v (%v)", got, err)
		}
	})

	t.Run("should read a template stored before the migration back with no framing", func(t *testing.T) {
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
			_, err = provider.UpTo(context.Background(), armoryFramingMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", armoryFramingMigrationVersion-1, err)
		}
		id := armoryTestUUID(t)
		if _, err := conn.Exec(`INSERT INTO armory_template (id, name, description, raw_template) VALUES (?, 'Old', '', 'GET / HTTP/1.1')`, id); err != nil {
			t.Fatalf("inserting an old template: %v", err)
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

		got, err := repo.GetArmoryTemplate(id)
		if err != nil || got.Framing != "" || got.RawTemplate != "GET / HTTP/1.1" {
			t.Fatalf("the old template\nwanted:\nno framing\ngot:\n%+v (%v)", got, err)
		}
	})
}
