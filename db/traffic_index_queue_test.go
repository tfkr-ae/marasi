package db

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// indexCapturedTraffic indexes the pairs capture queued, as the repository
// does within a second, for tests that read or rewrite stored index rows.
func indexCapturedTraffic(t *testing.T, repo *Repository) {
	t.Helper()
	if err := repo.flushIndexQueue(); err != nil {
		t.Fatalf("indexing captured traffic: %v", err)
	}
}

// projectFile returns the path of repo's project file.
func projectFile(t *testing.T, repo *Repository) string {
	t.Helper()
	var databases []struct {
		Seq  int    `db:"seq"`
		Name string `db:"name"`
		File string `db:"file"`
	}
	if err := repo.dbConn.Select(&databases, `PRAGMA database_list`); err != nil || len(databases) == 0 {
		t.Fatalf("finding the project file: %v", err)
	}
	return databases[0].File
}

// storedIndexMatches returns the ids of the pairs whose stored body index row
// matches the FTS5 expression match, read on a separate connection to path
// so that nothing in the capturing repository runs first.
func storedIndexMatches(t *testing.T, path, match string) []uuid.UUID {
	t.Helper()
	// A plain read-only connection, not a second Repository: opening a
	// Repository runs open-time writes that would race the live one's index
	// flush, and Marasi never opens one project twice.
	reader, err := sqlx.Connect("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening the stored index: %v", err)
	}
	defer reader.Close()
	var ids []uuid.UUID
	err = reader.Select(&ids, `SELECT k.request_id FROM traffic_index
		JOIN traffic_index_key k ON k.id = traffic_index.rowid WHERE traffic_index MATCH ?`, match)
	if err != nil {
		t.Fatalf("reading the stored index: %v", err)
	}
	return ids
}

// A captured pair reaches the stored index within about a second, at its
// request and again at its response, even when nothing reads the traffic.
func TestTrafficRepo_CapturedPairIsIndexedWithinASecond(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	path := projectFile(t, repo)

	inFlight := captureRequest(t, repo, "example.com", "GET /never-answered HTTP/1.1\r\n\r\n")
	answered := captureRequest(t, repo, "example.com", "GET /answered HTTP/1.1\r\n\r\n")
	captureResponse(t, repo, answered, []byte("HTTP/1.1 200 OK\r\n\r\nanswer-text"))

	for match, want := range map[string]uuid.UUID{
		`request_head : "never-answered"`: inFlight,
		`response_body : "answer-text"`:   answered,
	} {
		deadline := time.Now().Add(1500 * time.Millisecond)
		for {
			got := storedIndexMatches(t, path, match)
			if len(got) == 1 && got[0] == want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s\nwanted:\n%v\ngot:\n%v", match, []uuid.UUID{want}, got)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// Pairs captured just before the process stops, so that the repository
// neither closes nor indexes them, are searchable by their request and
// response text once the project is opened again.
func TestTrafficRepo_PairsCapturedBeforeACrashAreSearchableAfterReopening(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	path := projectFile(t, repo)
	ids := captureTextPairs(t, repo, textSearchPairs())

	// A second repository on the same file stands in for the next run: the
	// first is never closed.
	reopened := openTestProjectFile(t, path)
	defer reopened.Close()

	for query, want := range map[string]string{
		`request_head:"slow"`:      "pending",
		`response_body:"password"`: "reset",
		`"hbGci"`:                  "jwt",
	} {
		if got := listNames(t, reopened, ids, query); got != want {
			t.Fatalf("%s\nwanted:\n%q\ngot:\n%q", query, want, got)
		}
	}
}
