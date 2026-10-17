package db

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// capturePrettifiedPair captures a pair through the capture path with the
// given prettified bodies.
func capturePrettifiedPair(t *testing.T, repo *Repository, request, response string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: "https", Method: "POST", Host: "marasi.app", Path: "/",
		Raw:         []byte("POST / HTTP/1.1\r\nHost: marasi.app\r\n\r\n{\"a\":1}"),
		Metadata:    map[string]any{"source": "proxy"},
		Prettified:  request,
		RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	err = repo.InsertResponse(&domain.ProxyResponse{
		ID: id, Status: "200 OK", StatusCode: 200, ContentType: "application/json", Length: "7",
		Raw:         []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"b\":2}"),
		Metadata:    map[string]any{"source": "proxy"},
		Prettified:  response,
		RespondedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	return id
}

// insertUnmovedPair writes a pair the way a binary from before ADR-0036 does:
// its prettified copies in metadata, nothing in pair_prettified.
func insertUnmovedPair(t *testing.T, repo *Repository, metadata string) uuid.UUID {
	t.Helper()
	id := insertLegacyPair(t, repo, "GET / HTTP/1.1\r\n\r\n", "HTTP/1.1 200 OK\r\n\r\nok")
	if _, err := repo.dbConn.Exec(`UPDATE request SET metadata = ? WHERE id = ?`, metadata, id); err != nil {
		t.Fatalf("writing legacy metadata: %v", err)
	}
	return id
}

// fullRead returns the prettified copies and metadata of a full read of id.
func fullRead(t *testing.T, repo *Repository, id uuid.UUID) (domain.Prettified, map[string]any) {
	t.Helper()
	row, err := repo.GetRequestResponseRow(id)
	if err != nil {
		t.Fatalf("GetRequestResponseRow: %v", err)
	}
	return row.Prettified, row.Metadata
}

func TestPrettifiedBodies(t *testing.T) {
	t.Parallel()
	t.Run("should return the copies of a pair that has not moved yet, without them in metadata", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := insertUnmovedPair(t, repo, `{"source":"old","prettified-request":"old request","prettified-response":"old response"}`)

		prettified, metadata := fullRead(t, repo, id)
		if want := (domain.Prettified{Request: "old request", Response: "old response"}); prettified != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, prettified)
		}
		if want := map[string]any{"source": "old"}; !reflect.DeepEqual(metadata, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, metadata)
		}
	})

	t.Run("should keep the copies of a pair that has not moved yet when its metadata is replaced", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := insertUnmovedPair(t, repo, `{"prettified-request":"old request","prettified-response":"old response"}`)

		if err := repo.UpdateMetadata(map[string]any{"highlight": "red"}, id); err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}

		prettified, metadata := fullRead(t, repo, id)
		if want := (domain.Prettified{Request: "old request", Response: "old response"}); prettified != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, prettified)
		}
		if want := map[string]any{"highlight": "red"}; !reflect.DeepEqual(metadata, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, metadata)
		}
	})

	t.Run("should keep the request copy of a pair captured by an older binary when its response arrives", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := insertUnmovedPair(t, repo, `{"prettified-request":"old request"}`)

		insertTestResponseAndGet(t, repo, id, map[string]any{"source": "proxy"})

		prettified, _ := fullRead(t, repo, id)
		if want := (domain.Prettified{Request: "old request"}); prettified != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, prettified)
		}
	})

	t.Run("should return captured prettified bodies on a full read, outside metadata", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := capturePrettifiedPair(t, repo, "pretty request", "pretty response")

		row, err := repo.GetRequestResponseRow(id)
		if err != nil {
			t.Fatalf("GetRequestResponseRow: %v", err)
		}
		want := domain.Prettified{Request: "pretty request", Response: "pretty response"}
		if row.Prettified != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, row.Prettified)
		}
		metadata, err := repo.GetMetadata(id)
		if err != nil {
			t.Fatalf("GetMetadata: %v", err)
		}
		for _, key := range []string{"prettified-request", "prettified-response"} {
			if _, ok := metadata[key]; ok {
				t.Fatalf("\nwanted:\nno %s in metadata\ngot:\n%v", key, metadata)
			}
		}
	})
}

// prettifiedMoveJob returns the saved state of the prettified move.
func prettifiedMoveJob(t *testing.T, repo *Repository) JobState {
	t.Helper()
	jobs, err := repo.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, job := range jobs {
		if job.Name == domain.JobPrettifiedMove {
			return job
		}
	}
	t.Fatalf("\nwanted:\na %s job\ngot:\n%+v", domain.JobPrettifiedMove, jobs)
	return JobState{}
}

// metadataWithPrettified counts the stored metadata documents that still
// hold a prettified copy. Reads never show these keys, so this checks the
// stored rows themselves.
func metadataWithPrettified(t *testing.T, repo *Repository) int {
	t.Helper()
	var count int
	err := repo.dbConn.Get(&count, `SELECT count(*) FROM request
		WHERE json_type(metadata, '$."prettified-request"') IS NOT NULL
		OR json_type(metadata, '$."prettified-response"') IS NOT NULL`)
	if err != nil {
		t.Fatalf("counting metadata with prettified copies: %v", err)
	}
	return count
}

func TestPrettifiedMoveJob(t *testing.T) {
	t.Parallel()
	t.Run("should be complete on a new project", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		if got, want := prettifiedMoveJob(t, repo), (JobState{Name: domain.JobPrettifiedMove, Complete: true}); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should move every copy out of metadata in size-capped batches, newest first", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		captured := capturePrettifiedPair(t, repo, "new request", "new response")
		pad := strings.Repeat("x", 1000)
		// An older binary writes these pairs, so they are above the job's
		// high-water mark.
		oldest := insertUnmovedPair(t, repo, `{"source":"a","prettified-request":"a`+pad+`"}`)
		insertUnmovedPair(t, repo, `{"source":"b"}`)
		middle := insertUnmovedPair(t, repo, `{"prettified-response":"c`+pad+`"}`)
		newest := insertUnmovedPair(t, repo, `{"prettified-request":"d`+pad+`","prettified-response":"d"}`)
		repo = reopenProject(t, repo)

		if got := prettifiedMoveJob(t, repo); got.Complete {
			t.Fatalf("\nwanted:\nan incomplete job after an older binary wrote pairs\ngot:\n%+v", got)
		}
		moved, remaining, err := repo.RunJobBatch(domain.JobPrettifiedMove, 1500)
		if err != nil {
			t.Fatalf("RunJobBatch: %v", err)
		}
		if moved != 1 || !remaining {
			t.Fatalf("\nwanted:\n1 moved, more remaining\ngot:\n%d moved, remaining %v", moved, remaining)
		}
		if got := metadataWithPrettified(t, repo); got != 2 {
			t.Fatalf("\nwanted:\nthe two older pairs still unmoved\ngot:\n%d", got)
		}
		if prettified, _ := fullRead(t, repo, oldest); prettified.Request != "a"+pad {
			t.Fatalf("\nwanted:\nthe unmoved pair's request copy\ngot:\n%+v", prettified)
		}

		for remaining {
			if _, remaining, err = repo.RunJobBatch(domain.JobPrettifiedMove, 1500); err != nil {
				t.Fatalf("RunJobBatch: %v", err)
			}
		}

		if !prettifiedMoveJob(t, repo).Complete {
			t.Fatalf("\nwanted:\ncomplete\ngot:\nincomplete")
		}
		if got := metadataWithPrettified(t, repo); got != 0 {
			t.Fatalf("\nwanted:\nno metadata with prettified copies\ngot:\n%d", got)
		}
		for id, want := range map[uuid.UUID]domain.Prettified{
			oldest:   {Request: "a" + pad},
			middle:   {Response: "c" + pad},
			newest:   {Request: "d" + pad, Response: "d"},
			captured: {Request: "new request", Response: "new response"},
		} {
			if got, _ := fullRead(t, repo, id); got != want {
				t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
			}
		}
		if _, metadata := fullRead(t, repo, oldest); !reflect.DeepEqual(metadata, map[string]any{"source": "a"}) {
			t.Fatalf("\nwanted:\nthe other metadata kept\ngot:\n%v", metadata)
		}
	})
}
