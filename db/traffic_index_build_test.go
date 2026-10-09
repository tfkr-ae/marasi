package db

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
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

// sizedPairs writes older-version pairs whose response body is size bytes,
// so each pair holds a little over size bytes of stored text.
func sizedPairs(t *testing.T, repo *Repository, size int, words ...string) map[string]uuid.UUID {
	t.Helper()
	ids := make(map[string]uuid.UUID, len(words))
	for _, word := range words {
		body := "body-" + word
		ids[word] = insertLegacyPair(t, repo,
			"GET /"+word+" HTTP/1.1\r\nHost: example.com\r\n\r\n",
			"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n"+body+strings.Repeat(".", size-len(body)))
	}
	return ids
}

// indexAll is a batch budget larger than any test project.
const indexAll = 1 << 30

func indexMissing(t *testing.T, repo *Repository, maxBytes int) (int, bool) {
	t.Helper()
	indexed, remaining, err := repo.IndexMissingTraffic(maxBytes)
	if err != nil {
		t.Fatalf("IndexMissingTraffic(%d): %v", maxBytes, err)
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
	t.Run("should index missing pairs newest first in batches that fit the byte budget", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		// Each pair holds about 1,100 bytes, so 2,500 bytes fit two.
		ids := sizedPairs(t, repo, 1000, "alpha", "bravo", "charlie", "delta", "echo")

		if got := listNames(t, repo, ids, `"body-"`); got != "" {
			t.Fatalf("\nwanted:\nno legacy pair searchable before the build\ngot:\n%q", got)
		}
		if indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nincomplete index\ngot:\ncomplete")
		}

		indexed, remaining := indexMissing(t, repo, 2500)
		if indexed != 2 || !remaining {
			t.Fatalf("\nwanted:\n2 indexed, some remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "echo,delta" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "echo,delta", got)
		}

		indexed, remaining = indexMissing(t, repo, 2500)
		if indexed != 2 || !remaining {
			t.Fatalf("\nwanted:\n2 indexed, some remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nincomplete index\ngot:\ncomplete")
		}

		indexed, remaining = indexMissing(t, repo, 2500)
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

		indexed, remaining = indexMissing(t, repo, 2500)
		if indexed != 0 || remaining {
			t.Fatalf("\nwanted:\nnothing left to index\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
	})

	t.Run("should index a pair larger than the byte budget in a batch of its own", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		ids := sizedPairs(t, repo, 1000, "small1")
		for name, id := range sizedPairs(t, repo, 10000, "large") {
			ids[name] = id
		}
		for name, id := range sizedPairs(t, repo, 1000, "small2") {
			ids[name] = id
		}

		for _, want := range []string{"small2", "small2,large", "small2,large,small1"} {
			indexMissing(t, repo, 2500)
			if got := listNames(t, repo, ids, `"body-"`); got != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
			}
		}
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\ncomplete index\ngot:\nincomplete")
		}
	})

	t.Run("should retry a pair that cannot be indexed, then skip it", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		ids := legacyPairs(t, repo, "good1", "broken", "good2")
		_, err := repo.dbConn.Exec(`UPDATE request SET metadata = 'not json' WHERE id = ?`, ids["broken"])
		if err != nil {
			t.Fatalf("breaking metadata: %v", err)
		}

		for attempt := 1; attempt < 3; attempt++ {
			_, _, err := repo.IndexMissingTraffic(indexAll)
			var skipped *SkippedPairError
			if err == nil || errors.As(err, &skipped) {
				t.Fatalf("\nwanted:\nattempt %d to fail without skipping\ngot:\n%v", attempt, err)
			}
		}
		_, _, err = repo.IndexMissingTraffic(indexAll)
		var skipped *SkippedPairError
		if !errors.As(err, &skipped) || skipped.ID != ids["broken"] {
			t.Fatalf("\nwanted:\nthe third attempt to skip %s\ngot:\n%v", ids["broken"], err)
		}

		indexed, remaining := indexMissing(t, repo, indexAll)
		if indexed != 2 || remaining {
			t.Fatalf("\nwanted:\n2 indexed, none remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		// Traffic lists cannot read malformed metadata either. Once it is
		// repaired, the skipped pair stays out of the index until the
		// project is opened again.
		if _, err := repo.dbConn.Exec(`UPDATE request SET metadata = '{}' WHERE id = ?`, ids["broken"]); err != nil {
			t.Fatalf("repairing metadata: %v", err)
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "good2,good1" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "good2,good1", got)
		}
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\ncomplete index once only the skipped pair is missing\ngot:\nincomplete")
		}
	})

	t.Run("should not skip pairs while the database itself is failing", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		defer repo.Close()
		ids := legacyPairs(t, repo, "first", "second")

		// Another connection holds the write lock, so every batch fails on
		// its first index write.
		holder, err := sqlx.Connect("sqlite", "file:"+path)
		if err != nil {
			t.Fatalf("opening a second connection: %v", err)
		}
		defer holder.Close()
		lock, err := holder.Beginx()
		if err != nil {
			t.Fatalf("starting the lock transaction: %v", err)
		}
		if _, err := lock.Exec(`UPDATE request SET method = method`); err != nil {
			t.Fatalf("taking the write lock: %v", err)
		}
		for attempt := 1; attempt <= 5; attempt++ {
			_, _, err := repo.IndexMissingTraffic(indexAll)
			var skipped *SkippedPairError
			if err == nil || errors.As(err, &skipped) {
				t.Fatalf("\nwanted:\nattempt %d to fail without skipping\ngot:\n%v", attempt, err)
			}
		}
		if err := lock.Rollback(); err != nil {
			t.Fatalf("releasing the write lock: %v", err)
		}

		indexed, remaining := indexMissing(t, repo, indexAll)
		if indexed != 2 || remaining {
			t.Fatalf("\nwanted:\n2 indexed, none remaining\ngot:\n%d indexed, remaining=%v", indexed, remaining)
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "second,first" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "second,first", got)
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
		indexed, remaining := indexMissing(t, repo, indexAll)
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

		indexed, remaining := indexMissing(t, repo, indexAll)
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
		indexMissing(t, repo, indexAll)
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
		indexed, remaining := indexMissing(t, repo, indexAll)
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
		indexMissing(t, repo, indexAll)

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
