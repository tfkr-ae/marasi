package db

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

// insertLegacyPair writes a pair the way an older Marasi binary does: into the
// request table only, without an index row.
func insertLegacyPair(t *testing.T, repo *Repository, request, response string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}
	_, err = repo.dbConn.Exec(`INSERT INTO request
		(id, scheme, method, host, path, request_raw, response_raw, status, status_code, metadata, requested_at, responded_at)
		VALUES (?, 'https', 'GET', 'example.com', '/', ?, ?, '200 OK', 200, '{}', ?, ?)`,
		id, request, response, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("inserting legacy pair: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	return id
}

// openTestProjectFile opens the project file at path as a fresh repository.
func openTestProjectFile(t *testing.T, path string) *Repository {
	t.Helper()
	connection, err := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	return NewProxyRepo(connection)
}

func legacyPairs(t *testing.T, repo *Repository, words ...string) map[string]uuid.UUID {
	t.Helper()
	ids := make(map[string]uuid.UUID, len(words))
	for _, word := range words {
		ids[word] = insertLegacyPair(t, repo,
			"GET /"+word+" HTTP/1.1\r\nHost: example.com\r\n\r\n",
			"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nbody-"+word)
	}
	return ids
}

func indexMissing(t *testing.T, repo *Repository, limit int) (int, bool) {
	t.Helper()
	indexed, remaining, err := repo.IndexMissingTraffic(limit)
	if err != nil {
		t.Fatalf("IndexMissingTraffic(%d): %v", limit, err)
	}
	return indexed, remaining
}

func indexComplete(t *testing.T, repo *Repository) bool {
	t.Helper()
	_, _, complete, err := repo.ListTraffic(nil, 1, "")
	if err != nil {
		t.Fatalf("ListTraffic: %v", err)
	}
	return complete
}

func TestTrafficRepo_IndexMissingTraffic(t *testing.T) {
	t.Run("should index missing pairs newest first in batches", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		ids := legacyPairs(t, repo, "alpha", "bravo", "charlie", "delta", "echo")

		if got := listNames(t, repo, ids, `"body-"`); got != "" {
			t.Fatalf("\nwanted:\nno legacy pair searchable before the build\ngot:\n%q", got)
		}
		if indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nincomplete index\ngot:\ncomplete")
		}

		indexed, remaining := indexMissing(t, repo, 2)
		if indexed != 2 || !remaining {
			t.Fatalf("\nwanted:\n2 indexed, some remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "echo,delta" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "echo,delta", got)
		}

		indexed, remaining = indexMissing(t, repo, 2)
		if indexed != 2 || !remaining {
			t.Fatalf("\nwanted:\n2 indexed, some remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nincomplete index\ngot:\ncomplete")
		}

		indexed, remaining = indexMissing(t, repo, 2)
		if indexed != 1 || remaining {
			t.Fatalf("\nwanted:\n1 indexed, none remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\ncomplete index\ngot:\nincomplete")
		}
		if got := listNames(t, repo, ids, `response_body:"body-"`); got != "echo,delta,charlie,bravo,alpha" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "echo,delta,charlie,bravo,alpha", got)
		}
		if got := listNames(t, repo, ids, `request_head:"/charlie"`); got != "charlie" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "charlie", got)
		}

		indexed, remaining = indexMissing(t, repo, 2)
		if indexed != 0 || remaining {
			t.Fatalf("\nwanted:\nnothing left to index\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
	})

	t.Run("should report a complete index when every pair was captured by this version", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nempty project complete\ngot:\nincomplete")
		}
		captureTextPairs(t, repo, textSearchPairs())
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\ncomplete index\ngot:\nincomplete")
		}
		indexed, remaining := indexMissing(t, repo, 100)
		if indexed != 0 || remaining {
			t.Fatalf("\nwanted:\nnothing to index\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
	})

	t.Run("should index older-version pairs mixed with current captures", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		ids := legacyPairs(t, repo, "old1")
		current := captureTextPairs(t, repo, []textPair{{name: "current", request: "GET /x HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nbody-current")}})
		ids["current"] = current["current"]
		for _, word := range []string{"old2"} {
			for name, id := range legacyPairs(t, repo, word) {
				ids[name] = id
			}
		}

		indexed, remaining := indexMissing(t, repo, 100)
		if indexed != 2 || remaining {
			t.Fatalf("\nwanted:\n2 indexed, none remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "old2,current,old1" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "old2,current,old1", got)
		}
	})

	t.Run("should find pairs an older version wrote after the index was complete", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		ids := legacyPairs(t, repo, "first")
		indexMissing(t, repo, 100)
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\ncomplete index\ngot:\nincomplete")
		}
		repo.Close()

		// An older binary opens the project and captures more traffic.
		older := openTestProjectFile(t, path)
		for name, id := range legacyPairs(t, older, "second") {
			ids[name] = id
		}
		older.Close()

		repo = openTestProjectFile(t, path)
		defer repo.Close()
		if indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nincomplete index after an older version wrote\ngot:\ncomplete")
		}
		indexed, remaining := indexMissing(t, repo, 100)
		if indexed != 1 || remaining {
			t.Fatalf("\nwanted:\n1 indexed, none remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "second,first" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "second,first", got)
		}
	})

	t.Run("should index the notes and metadata older versions wrote", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		ids := legacyPairs(t, repo, "tagged")
		_, err := repo.dbConn.Exec(`UPDATE request SET metadata = '{"extension":"workshop","prettified-response":"hidden-pretty"}' WHERE id = ?`, ids["tagged"])
		if err != nil {
			t.Fatalf("writing legacy metadata: %v", err)
		}
		if _, err := repo.dbConn.Exec(`INSERT INTO notes (request_id, note) VALUES (?, 'idor candidate')`, ids["tagged"]); err != nil {
			t.Fatalf("writing legacy note: %v", err)
		}
		indexMissing(t, repo, 100)

		for query, want := range map[string]string{
			`note:"idor"`:           "tagged",
			`metadata:"workshop"`:   "tagged",
			`metadata:"hidden-pre"`: "",
		} {
			if got := listNames(t, repo, ids, query); got != want {
				t.Fatalf("\nwanted:\n%s -> %q\ngot:\n%q", query, want, got)
			}
		}
	})
}
