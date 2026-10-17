package db

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestNewLiteralPaths(t *testing.T) {
	t.Parallel()
	names := []string{"shared#", "shared%23", "shared%", "shared space 雪"}
	if runtime.GOOS != "windows" {
		// Windows forbids '?' in filenames; the other URI characters are valid.
		names = append(names, "shared?", "shared?_pragma=foreign_keys(OFF)&")
	}
	for _, prefix := range names {
		t.Run(prefix, func(t *testing.T) {
			for _, relative := range []bool{false, true} {
				t.Run(fmt.Sprintf("relative=%t", relative), func(t *testing.T) {
					dir := t.TempDir()
					logger := slog.New(slog.NewTextHandler(io.Discard, nil))
					connections := make([]*Repository, 0, 2)
					for _, value := range []string{"one", "two"} {
						path := filepath.Join(dir, prefix+value+".marasi")
						name := path
						if relative {
							cwd, err := os.Getwd()
							if err != nil {
								t.Fatal(err)
							}
							name, err = filepath.Rel(cwd, path)
							if err != nil {
								t.Fatal(err)
							}
						}
						connection, err := New(name, logger)
						if err != nil {
							t.Fatalf("opening %q: %v", name, err)
						}
						repository := NewProxyRepo(connection)
						t.Cleanup(func() { _ = repository.Close() })
						connections = append(connections, repository)
						if _, err := os.Stat(path); err != nil {
							t.Errorf("requested literal file %q: %v", path, err)
						}
						if err := repository.CreateWaypoint(value, "127.0.0.1"); err != nil {
							t.Fatalf("inserting waypoint: %v", err)
						}
						var foreignKeys int
						if err := connection.Get(&foreignKeys, "PRAGMA foreign_keys"); err != nil || foreignKeys != 1 {
							t.Fatalf("foreign keys: got %d, %v; want 1", foreignKeys, err)
						}
					}
					for i, value := range []string{"one", "two"} {
						waypoints, err := connections[i].GetWaypoints()
						if err != nil || len(waypoints) != 1 || waypoints[0].Hostname != value {
							t.Fatalf("independent data: want %s; got %v, %v", value, waypoints, err)
						}
					}
				})
			}
		})
	}
}

func TestNewConcurrent(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const n = 2
	errc := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			dbConn, err := New(filepath.Join(t.TempDir(), fmt.Sprintf("%d.marasi", i)), logger)
			if err != nil {
				errc <- err
				return
			}
			errc <- dbConn.Close()
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errc; err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	}
}

func TestNewMemory(t *testing.T) {
	t.Parallel()
	connection, err := New(":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening in-memory database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	var path string
	if err := connection.Get(&path, "SELECT file FROM pragma_database_list WHERE name = 'main'"); err != nil || path != "" {
		t.Fatalf("in-memory database filename: want empty; got %q, %v", path, err)
	}
	if _, err := NewProxyRepo(connection).GetWaypoints(); err != nil {
		t.Fatalf("in-memory migrations: %v", err)
	}
}

func TestNewPragmas(t *testing.T) {
	t.Parallel()
	t.Run("should open a project file in WAL mode with normal sync, a busy timeout, and foreign keys", func(t *testing.T) {
		connection, err := New(filepath.Join(t.TempDir(), "pragmas.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("opening database: %v", err)
		}
		t.Cleanup(func() { _ = connection.Close() })

		want := map[string]string{
			"journal_mode": "wal",
			"synchronous":  "1", // NORMAL
			"busy_timeout": "5000",
			"foreign_keys": "1",
		}
		for pragma, wantValue := range want {
			var got string
			if err := connection.Get(&got, "PRAGMA "+pragma); err != nil {
				t.Fatalf("reading PRAGMA %s: %v", pragma, err)
			}
			if got != wantValue {
				t.Fatalf("\nwanted PRAGMA %s:\n%s\ngot:\n%s", pragma, wantValue, got)
			}
		}
	})
}

// newProjectBytes is a new project file with every migration applied, made
// once per test binary. Migrating takes about half a second under -race;
// opening a copy takes a tenth of that.
var newProjectBytes = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "marasi-db-test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "new.marasi")
	conn, err := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return nil, err
	}
	// Closing checkpoints the write-ahead log into the file.
	if err := NewProxyRepo(conn).Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
})

// writeNewProject writes a new, fully migrated project file at path.
func writeNewProject(t *testing.T, path string) {
	t.Helper()
	raw, err := newProjectBytes()
	if err != nil {
		t.Fatalf("making a new project: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing a new project: %v", err)
	}
}

// TestNewProjectCopy holds the copied project setupTestDB opens to what
// New makes of an empty file.
func TestNewProjectCopy(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dump := func(path string) []string {
		conn, err := New(path, logger)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var rows []string
		queries := []string{
			`SELECT type || ' ' || name || ' ' || coalesce(sql, '') FROM sqlite_schema ORDER BY name`,
			`SELECT version_id || ' ' || is_applied FROM goose_db_version ORDER BY id`,
			`SELECT name || ' ' || complete || ' ' || version || ' ' || done || ' ' || coalesce(total, '-') || ' ' || coalesce(high_water, '-') FROM background_job ORDER BY name`,
		}
		for _, query := range queries {
			var got []string
			if err := conn.Select(&got, query); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			rows = append(rows, got...)
		}
		return rows
	}

	fresh := filepath.Join(t.TempDir(), "fresh.marasi")
	copied := filepath.Join(t.TempDir(), "copied.marasi")
	writeNewProject(t, copied)

	if want, got := dump(fresh), dump(copied); !slices.Equal(want, got) {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}

func setupTestDB(t *testing.T) (*Repository, func()) {
	t.Helper()

	tempFile, err := os.CreateTemp(t.TempDir(), "test_*.db")
	if err != nil {
		t.Fatalf("os.CreateTemp() failed: %v", err)
	}
	tempFile.Close()
	writeNewProject(t, tempFile.Name())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConn, err := New(tempFile.Name(), logger)
	if err != nil {
		t.Fatalf("db.New() failed: %v", err)
	}

	repo := NewProxyRepo(dbConn)

	teardown := func() {
		repo.Close()
		os.Remove(tempFile.Name())
	}

	return repo, teardown
}

func testRequest(t *testing.T, repo *Repository, metadata map[string]any) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}

	if metadata == nil {
		metadata = make(map[string]any)
	}

	req := &domain.ProxyRequest{
		ID:          id,
		Scheme:      "https",
		Method:      "GET",
		Host:        "marasi.app",
		Path:        "/",
		Raw:         []byte("GET / HTTP/1.1\r\nHost: marasi.app\r\n\r\n"),
		Metadata:    metadata,
		RequestedAt: time.Now(),
	}

	err = repo.InsertRequest(req)
	if err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	return id
}

func insertTestResponseAndGet(t *testing.T, repo *Repository, reqID uuid.UUID, metadata map[string]any) *domain.ProxyResponse {
	t.Helper()

	if metadata == nil {
		metadata = make(map[string]any)
	}

	rawResp := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 12\r\n\r\nHello Marasi")

	resp := &domain.ProxyResponse{
		ID:          reqID,
		Status:      "200 OK",
		StatusCode:  200,
		ContentType: "text/plain",
		Length:      "12",
		Raw:         rawResp,
		Metadata:    metadata,
		RespondedAt: time.Now().UTC().Truncate(time.Millisecond),
	}

	err := repo.InsertResponse(resp)
	if err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	return resp
}
