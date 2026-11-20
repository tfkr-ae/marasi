package db

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
)

// checkpointStreamHookMigrationVersion is the migration that gives the
// built-in Checkpoint extension interceptStreamMessage.
const checkpointStreamHookMigrationVersion = 30

// checkpointLua is the Lua of the project's checkpoint extension.
func checkpointLua(t *testing.T, repo *Repository) string {
	t.Helper()
	extensions, err := repo.GetExtensions()
	if err != nil {
		t.Fatal(err)
	}
	for _, extension := range extensions {
		if extension.Name == "checkpoint" {
			return extension.LuaContent
		}
	}
	t.Fatal("the project has no checkpoint extension")
	return ""
}

func TestCheckpointStreamHookMigration(t *testing.T) {
	// migrateBefore opens a project at the migration before the hook, runs
	// edit on it, then opens it, which runs the rest.
	migrateBefore := func(t *testing.T, edit string) *Repository {
		t.Helper()
		path := filepath.Join(t.TempDir(), "project.db")
		conn, err := sqlx.Connect("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		migrationsFS, err := fs.Sub(embedMigrations, "migrations")
		if err != nil {
			t.Fatal(err)
		}
		migrateMu.Lock()
		provider, err := goose.NewProvider(goose.DialectSQLite3, conn.DB, migrationsFS)
		if err == nil {
			_, err = provider.UpTo(context.Background(), checkpointStreamHookMigrationVersion-1)
		}
		migrateMu.Unlock()
		if err != nil {
			t.Fatalf("migrating to %d: %v", checkpointStreamHookMigrationVersion-1, err)
		}
		if edit != "" {
			if _, err := conn.Exec(edit); err != nil {
				t.Fatal(err)
			}
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		return openProject(t, path)
	}

	t.Run("should give a new project's checkpoint an interceptStreamMessage that holds nothing", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		lua := checkpointLua(t, repo)
		if !strings.Contains(lua, "function interceptStreamMessage(message)\n\treturn false\nend") {
			t.Fatalf("the checkpoint Lua\nwanted:\ninterceptStreamMessage returning false\ngot:\n%s", lua)
		}
	})

	t.Run("should add the hook to a project's checkpoint, keeping the user's edits", func(t *testing.T) {
		repo := migrateBefore(t, `UPDATE extensions SET lua_content = '-- mine' || char(10) || lua_content WHERE name = 'checkpoint'`)
		lua := checkpointLua(t, repo)
		if !strings.HasPrefix(lua, "-- mine\n") || strings.Count(lua, "function interceptStreamMessage") != 1 || !strings.Contains(lua, "function interceptWebSocketMessage") {
			t.Fatalf("the checkpoint Lua\nwanted:\nthe user's edit, the hooks it had and one interceptStreamMessage\ngot:\n%s", lua)
		}
	})

	t.Run("should leave a checkpoint that already defines the hook as it is", func(t *testing.T) {
		const own = "function interceptStreamMessage(message)\n\treturn true\nend\n"
		repo := migrateBefore(t, `UPDATE extensions SET lua_content = '`+own+`' WHERE name = 'checkpoint'`)
		if lua := checkpointLua(t, repo); lua != own {
			t.Fatalf("the checkpoint Lua\nwanted:\n%q\ngot:\n%q", own, lua)
		}
	})
}
