package db

import (
	"database/sql"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
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
	t.Parallel()
	// The body index row is dropped and must not come back; the size of
	// the body does not matter.
	body := strings.Repeat("large-body-text ", 1<<10)
	for name, annotate := range map[string]func(repo *Repository, id uuid.UUID) error{
		"note": func(repo *Repository, id uuid.UUID) error { return repo.UpdateNote(id, "annotated note") },
		"metadata": func(repo *Repository, id uuid.UUID) error {
			return repo.UpdateMetadata(map[string]any{"tag": "annotated"}, id)
		},
		"bulk note append": func(repo *Repository, id uuid.UUID) error {
			_, err := repo.AppendNote(domain.Selection{IDs: []uuid.UUID{id}}, "annotated note")
			return err
		},
		"bulk note clear": func(repo *Repository, id uuid.UUID) error {
			if err := repo.UpdateNote(id, "annotated note"); err != nil {
				return err
			}
			_, err := repo.ClearNotes(domain.Selection{IDs: []uuid.UUID{id}})
			return err
		},
		"bulk metadata set": func(repo *Repository, id uuid.UUID) error {
			_, err := repo.SetMetadataKey(domain.Selection{IDs: []uuid.UUID{id}}, "tag", json.RawMessage(`"annotated"`))
			return err
		},
		"bulk metadata clear": func(repo *Repository, id uuid.UUID) error {
			if err := repo.UpdateMetadata(map[string]any{"tag": "annotated"}, id); err != nil {
				return err
			}
			_, err := repo.ClearMetadataKey(domain.Selection{IDs: []uuid.UUID{id}}, "tag")
			return err
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
			indexCapturedTraffic(t, repo)
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
	t.Parallel()
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
	indexCapturedTraffic(t, repo)
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

// annotationIndexJob returns the saved state of the annotation index backfill.
func annotationIndexJob(t *testing.T, repo *Repository) JobState {
	t.Helper()
	jobs, err := repo.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, job := range jobs {
		if job.Name == domain.JobAnnotationIndex {
			return job
		}
	}
	t.Fatalf("\nwanted:\na %s job\ngot:\n%+v", domain.JobAnnotationIndex, jobs)
	return JobState{}
}

// forgetAnnotationIndex returns a project to its state just after the
// annotation index migration: every pair is in the body index, with its note
// there, and the annotation index is empty and its job unknown.
func forgetAnnotationIndex(t *testing.T, repo *Repository) *Repository {
	t.Helper()
	var pairs []struct {
		ID   uuid.UUID      `db:"id"`
		Note sql.NullString `db:"note"`
	}
	if err := repo.dbConn.Select(&pairs, `SELECT r.id, n.note FROM request r LEFT JOIN notes n ON n.request_id = r.id`); err != nil {
		t.Fatalf("reading pairs: %v", err)
	}
	for _, pair := range pairs {
		writeOldBodyIndexRow(t, repo, pair.ID, pair.Note.String, "")
	}
	for _, statement := range []string{
		`INSERT INTO traffic_annotation_index (traffic_annotation_index) VALUES ('delete-all')`,
		`DELETE FROM background_job WHERE name = 'annotation_index'`,
	} {
		if _, err := repo.dbConn.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	return reopenProject(t, repo)
}

func runJob(t *testing.T, repo *Repository, name domain.JobName, maxBytes int) (int, bool) {
	t.Helper()
	processed, remaining, err := repo.RunJobBatch(name, maxBytes)
	if err != nil {
		t.Fatalf("RunJobBatch(%s, %d): %v", name, maxBytes, err)
	}
	return processed, remaining
}

// finishJob runs the named job until nothing remains, the way the service
// runs each job after the one before it.
func finishJob(t *testing.T, repo *Repository, name domain.JobName) {
	t.Helper()
	for {
		if _, remaining := runJob(t, repo, name, indexAll); !remaining {
			return
		}
	}
}

func TestAnnotationIndexJob(t *testing.T) {
	t.Parallel()
	t.Run("should list the annotation index as complete on a new project", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		if got, want := annotationIndexJob(t, repo), (JobState{Name: domain.JobAnnotationIndex, Complete: true}); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should backfill the notes and metadata of pairs indexed before the split, in batches", func(t *testing.T) {
		repo := openTestProjectFile(t, t.TempDir()+"/project.marasi")
		ids := captureTextPairs(t, repo, []textPair{
			{name: "first", request: "GET /first HTTP/1.1\r\n\r\n"},
			{name: "second", request: "GET /second HTTP/1.1\r\n\r\n"},
			{name: "third", request: "GET /third HTTP/1.1\r\n\r\n"},
		})
		if err := repo.UpdateNote(ids["first"], "backfilled-note"); err != nil {
			t.Fatalf("inserting note: %v", err)
		}
		if err := repo.UpdateMetadata(map[string]any{"tag": "backfilled-meta"}, ids["third"]); err != nil {
			t.Fatalf("updating metadata: %v", err)
		}
		indexCapturedTraffic(t, repo)
		repo = forgetAnnotationIndex(t, repo)

		if got := annotationIndexJob(t, repo); got.Complete {
			t.Fatalf("\nwanted:\nan incomplete job after the migration\ngot:\n%+v", got)
		}
		page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 1})
		if err != nil {
			t.Fatalf("ListTraffic: %v", err)
		}
		if want := map[domain.JobName]bool{domain.JobTrafficIndex: true, domain.JobAnnotationIndex: false}; !maps.Equal(page.Complete, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, page.Complete)
		}
		assertListNames(t, repo, ids, "before backfill", `note:"backfilled-note"`, "")

		if processed, remaining := runJob(t, repo, domain.JobAnnotationIndex, 1); processed != 1 || !remaining {
			t.Fatalf("\nwanted:\none pair and more remaining\ngot:\n%d, %v", processed, remaining)
		}
		for {
			if _, remaining := runJob(t, repo, domain.JobAnnotationIndex, 1); !remaining {
				break
			}
		}

		if got, want := annotationIndexJob(t, repo), (JobState{Name: domain.JobAnnotationIndex, Complete: true, Done: 3, Total: 3}); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\ncomplete index\ngot:\nincomplete")
		}
		assertListNames(t, repo, ids, "after backfill", `note:"backfilled-note"`, "first")
		assertListNames(t, repo, ids, "after backfill", `"backfilled"`, "third,first")
		assertListNames(t, repo, ids, "after backfill", `metadata:"backfilled-meta"`, "third")
	})
}

func TestTrafficRepo_CountTrafficAfterSearchesBothIndexes(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{name: "old-note", request: "GET /a HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nplain")},
		{name: "old-body", request: "GET /b HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nneedle-in-body")},
		{name: "bound", request: "GET /c HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nplain")},
		{name: "new-note", request: "GET /d HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nplain")},
		{name: "new-body", request: "GET /e HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nneedle-in-body")},
	})
	for _, name := range []string{"old-note", "new-note"} {
		if err := repo.UpdateNote(ids[name], "needle-in-note"); err != nil {
			t.Fatalf("inserting note: %v", err)
		}
	}
	after := ids["bound"]

	for query, want := range map[string]int{
		`"needle-in"`:                    2,
		`note:"needle-in-note"`:          1,
		`response_body:"needle-in-body"`: 1,
		`note:"needle-in-note" OR response_body:"needle-in-body"`: 2,
	} {
		t.Run(query, func(t *testing.T) {
			got, err := repo.CountTraffic(domain.TrafficCountQuery{Text: query, After: &after})
			if err != nil {
				t.Fatalf("CountTraffic: %v", err)
			}
			if got.Count != want {
				t.Fatalf("\nwanted:\n%d new matches\ngot:\n%d", want, got.Count)
			}
		})
	}
}
