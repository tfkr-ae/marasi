package db

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestNewLiteralPaths(t *testing.T) {
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

func setupTestDB(t *testing.T) (*Repository, func()) {
	t.Helper()

	tempFile, err := os.CreateTemp(t.TempDir(), "test_*.db")
	if err != nil {
		t.Fatalf("os.CreateTemp() failed: %v", err)
	}
	tempFile.Close()

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
