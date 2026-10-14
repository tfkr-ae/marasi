package db

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// bodyIndexRowExists reports whether the pair with id has a row in the body
// index, from its FTS5 docsize table.
func bodyIndexRowExists(t *testing.T, repo *Repository, id uuid.UUID) bool {
	t.Helper()
	var count int
	err := repo.dbConn.Get(&count, `SELECT count(*) FROM traffic_index_docsize d
		JOIN traffic_index_key k ON k.id = d.id WHERE k.request_id = ?`, id)
	if err != nil {
		t.Fatalf("reading the body index docsize: %v", err)
	}
	return count > 0
}

// dropBodyIndexRow deletes the pair's body index row, so that a later write to
// the body index shows up as a new docsize row.
func dropBodyIndexRow(t *testing.T, repo *Repository, id uuid.UUID) {
	t.Helper()
	_, err := repo.dbConn.Exec(`DELETE FROM traffic_index
		WHERE rowid = (SELECT id FROM traffic_index_key WHERE request_id = ?)`, id)
	if err != nil {
		t.Fatalf("deleting the body index row: %v", err)
	}
}

func TestTrafficRepo_AnnotationWritesSkipBodyIndex(t *testing.T) {
	body := strings.Repeat("large-body-text ", 5<<20/16)
	for name, annotate := range map[string]func(repo *Repository, id uuid.UUID) error{
		"note": func(repo *Repository, id uuid.UUID) error { return repo.UpdateNote(id, "annotated note") },
		"metadata": func(repo *Repository, id uuid.UUID) error {
			return repo.UpdateMetadata(map[string]any{"tag": "annotated"}, id)
		},
		"delete note": func(repo *Repository, id uuid.UUID) error {
			if err := repo.UpdateNote(id, "annotated note"); err != nil {
				return err
			}
			return repo.DeleteNote(id)
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, teardown := setupTestDB(t)
			defer teardown()
			ids := captureTextPairs(t, repo, []textPair{{
				name:     "large",
				request:  "GET /large HTTP/1.1\r\n\r\n",
				response: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n" + body),
			}})
			if !bodyIndexRowExists(t, repo, ids["large"]) {
				t.Fatalf("\nwanted:\na body index row after capture\ngot:\nnone")
			}
			dropBodyIndexRow(t, repo, ids["large"])

			if err := annotate(repo, ids["large"]); err != nil {
				t.Fatalf("annotating: %v", err)
			}

			if bodyIndexRowExists(t, repo, ids["large"]) {
				t.Fatalf("\nwanted:\nno write to the body index\ngot:\na new body index row")
			}
		})
	}
}

// writeOldBodyIndexRow writes the pair's body index row the way a binary from
// before the annotation index did: with its note and metadata text in the
// body index.
func writeOldBodyIndexRow(t *testing.T, repo *Repository, id uuid.UUID, note, metadata string) {
	t.Helper()
	_, err := repo.dbConn.Exec(`INSERT OR REPLACE INTO traffic_index
		(rowid, request_head, request_body, response_head, response_body, note, metadata)
		SELECT k.id, 'GET /old HTTP/1.1', NULL, 'HTTP/1.1 200 OK', 'old body', ?, ?
		FROM traffic_index_key k WHERE k.request_id = ?`, note, metadata, id)
	if err != nil {
		t.Fatalf("writing an old body index row: %v", err)
	}
}

func TestTrafficRepo_ListTrafficSearchesBothIndexes(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{name: "noted", request: "GET /noted HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nplain")},
		{name: "body", request: "GET /body HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nneedle-in-body")},
		{name: "stale", request: "GET /stale HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nplain")},
	})
	if err := repo.UpdateNote(ids["noted"], "needle-in-note"); err != nil {
		t.Fatalf("inserting note: %v", err)
	}
	writeOldBodyIndexRow(t, repo, ids["stale"], "needle-in-note stale", `{"tag":"needle-in-meta"}`)

	for query, want := range map[string]string{
		`note:"needle-in-note" OR response_body:"needle-in-body"`: "body,noted",
		`"needle-in"`:              "body,noted",
		`"needle-in-note"`:         "noted",
		`note:"needle"`:            "noted",
		`metadata:"needle"`:        "",
		`"stale"`:                  "",
		`response_body:"old body"`: "stale",
		`NOT "needle-in"`:          "stale",
	} {
		t.Run(query, func(t *testing.T) {
			if got := listNames(t, repo, ids, query); got != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})
	}
}
