package db

import (
	"bytes"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestTrafficRepo_InsertRequest(t *testing.T) {
	t.Run("should insert request", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}

		wantTime := time.Now().UTC().Truncate(time.Millisecond)
		wantMeta := map[string]any{"key": "value"}
		wantRaw := []byte("GET / HTTP/1.1\r\nHost: marasi.app\r\n\r\n")

		req := &domain.ProxyRequest{
			ID:          wantID,
			Scheme:      "https",
			Method:      "GET",
			Host:        "marasi.app",
			Path:        "/",
			Raw:         wantRaw,
			Metadata:    wantMeta,
			RequestedAt: wantTime,
		}

		err = repo.InsertRequest(req)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		var got dbRequestResponse
		err = repo.dbConn.Get(&got, "SELECT * FROM request WHERE id = ?", wantID)
		if err != nil {
			t.Fatalf("getting inserted request: %v", err)
		}

		if got.ID != wantID {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantID, got.ID)
		}
		if got.Host != "marasi.app" {
			t.Fatalf("\nwanted:\nmarasi.app\ngot:\n%s", got.Host)
		}
		if !reflect.DeepEqual(got.Metadata, Metadata(wantMeta)) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, got.Metadata)
		}
		if !got.RequestedAt.Equal(wantTime) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantTime, got.RequestedAt)
		}
	})

	t.Run("should violate unique constraint if an existing ID is used", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)
		req := &domain.ProxyRequest{
			ID:          reqID,
			Scheme:      "https",
			Method:      "GET",
			Host:        "marasi.app",
			Path:        "/",
			Raw:         []byte("GET / HTTP/1.1\r\nHost: marasi.app\r\n\r\n"),
			Metadata:    make(map[string]any),
			RequestedAt: time.Now().UTC().Truncate(time.Millisecond),
		}

		err := repo.InsertRequest(req)

		if err == nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Fatalf("\nwanted:\nerror containing 'UNIQUE constraint failed'\ngot:\n%v", err)
		}

	})
}

func TestTrafficRepo_InsertResponse(t *testing.T) {
	t.Run("should update an existing request row with response data", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)
		want := &domain.ProxyResponse{
			ID:          reqID,
			Status:      "200 OK",
			StatusCode:  200,
			ContentType: "text/plain",
			Length:      "12",
			Raw:         []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 12\r\n\r\nHello Marasi"),
			Metadata:    map[string]any{"key": "value"},
			RespondedAt: time.Now().UTC().Truncate(time.Millisecond),
		}

		err := repo.InsertResponse(want)
		if err != nil {
			t.Fatalf("inserting response : %v", err)
		}

		var got dbRequestResponse
		err = repo.dbConn.Get(&got, "SELECT * FROM request WHERE id = ?", reqID)
		if err != nil {
			t.Fatalf("getting updated request: %v", err)
		}

		if got.Status.String != want.Status {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want.Status, got.Status.String)
		}
		if got.StatusCode.Int64 != int64(want.StatusCode) {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", want.StatusCode, got.StatusCode.Int64)
		}
		if !bytes.Equal(got.ResponseRaw, want.Raw) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want.Raw, got.ResponseRaw)
		}
		if !got.RespondedAt.Time.Equal(want.RespondedAt) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want.RespondedAt, got.RespondedAt.Time)
		}
		if !reflect.DeepEqual(got.Metadata, Metadata(want.Metadata)) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want.Metadata, got.Metadata)
		}
	})

	t.Run("should return an error if request ID doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		resp := &domain.ProxyResponse{ID: nonExistentID, Status: "200 OK", StatusCode: 200}

		err := repo.InsertResponse(resp)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !strings.Contains(err.Error(), "no request found") {
			t.Fatalf("\nwanted:\nerror containing 'no request found'\ngot:\n%v", err)
		}
	})
}

func TestTrafficRepo_GetResponse(t *testing.T) {
	t.Run("should get an existing response", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)
		want := insertTestResponseAndGet(t, repo, reqID, nil)

		got, err := repo.GetResponse(reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should return empty fields for a request with no response", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)
		want := &domain.ProxyResponse{
			ID:         reqID,
			Status:     "N/A",
			StatusCode: -1,
			Length:     "0",
			Metadata:   make(map[string]any),
			Raw:        nil,
		}

		got, err := repo.GetResponse(reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should return an error for a non-existent ID", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")

		_, err := repo.GetResponse(nonExistentID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("\nwanted:\nsql.ErrNoRows\ngot:\n%v", err)
		}
	})
}

func TestTrafficRepo_GetRequestResponseRow(t *testing.T) {
	t.Run("should get a full row with request, response, and note", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)
		resp := insertTestResponseAndGet(t, repo, reqID, nil)
		wantNote := "This is a test note"

		err := repo.UpdateNote(reqID, wantNote)
		if err != nil {
			t.Fatalf("updating note: %v", err)
		}

		reqRow, err := repo.GetRequestResponseRow(reqID)
		if err != nil {
			t.Fatalf("fetching request row: %v", err)
		}

		req := &reqRow.Request

		want := &domain.RequestResponseRow{
			Request:  *req,
			Response: *resp,
			Metadata: resp.Metadata,
			Note:     wantNote,
		}
		want.Metadata["has_note"] = float64(1)

		got, err := repo.GetRequestResponseRow(reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should get a row with request but no response and no note", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqMeta := map[string]any{"key": "value"}
		reqID := testRequest(t, repo, reqMeta)

		reqRow, err := repo.GetRequestResponseRow(reqID)
		if err != nil {
			t.Fatalf("fetching request row: %v", err)
		}

		want := &domain.RequestResponseRow{
			Request: reqRow.Request,
			Response: domain.ProxyResponse{
				ID:         reqID,
				Status:     "N/A",
				StatusCode: -1,
				Length:     "0",
				Metadata:   reqMeta,
				Raw:        nil,
			},
			Metadata: reqMeta,
			Note:     "",
		}

		got, err := repo.GetRequestResponseRow(reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should return an error for a non-existent ID", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("01938035-374f-7f6d-8e18-639a061b5c40")

		_, err := repo.GetRequestResponseRow(nonExistentID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("\nwanted:\nsql.ErrNoRows\ngot:\n%v", err)
		}
	})
}

func TestTrafficRepo_GetRequestResponseSummary(t *testing.T) {
	t.Run("should return an empty slice if database is empty", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		summary, err := repo.GetRequestResponseSummary()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(summary) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(summary))
		}
	})

	t.Run("should return summary for all requests, ordered ASC by ID", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID1 := testRequest(t, repo, map[string]any{"prettified-request": "pretty1"})
		time.Sleep(2 * time.Millisecond)

		reqID2 := testRequest(t, repo, map[string]any{"key": "val"})
		time.Sleep(2 * time.Millisecond)

		reqID3 := testRequest(t, repo, map[string]any{"prettified-response": "pretty3"})

		insertTestResponseAndGet(t, repo, reqID1, nil)
		insertTestResponseAndGet(t, repo, reqID3, map[string]any{"prettified-response": "pretty3-resp", "foo": "bar"})

		summary, err := repo.GetRequestResponseSummary()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(summary) != 3 {
			t.Fatalf("\nwanted:\n3\ngot:\n%d", len(summary))
		}

		if summary[0].ID != reqID1 {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", reqID1, summary[0].ID)
		}

		if summary[1].ID != reqID2 {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", reqID2, summary[1].ID)
		}

		if summary[2].ID != reqID3 {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", reqID3, summary[2].ID)
		}

		wantMeta := map[string]any{"foo": "bar"}
		if !reflect.DeepEqual(summary[2].Metadata, wantMeta) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, summary[2].Metadata)
		}

		wantMeta = map[string]any{}
		if !reflect.DeepEqual(summary[0].Metadata, wantMeta) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, summary[0].Metadata)
		}
	})
}

func TestTrafficRepo_GetMetadata(t *testing.T) {
	t.Run("should get metadata for an existing request", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantMeta := map[string]any{"key1": "value1", "key2": float64(123)}
		reqID := testRequest(t, repo, wantMeta)

		got, err := repo.GetMetadata(reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(wantMeta, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, got)
		}
	})

	t.Run("should get empty metadata map for request with no metadata", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)
		wantMeta := make(map[string]any)

		got, err := repo.GetMetadata(reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if !reflect.DeepEqual(wantMeta, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, got)
		}
	})

	t.Run("should return an error for a non-existent ID", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		nonExistentID := uuid.MustParse("01938038-7090-785d-83b6-1216a6ca7052")

		_, err := repo.GetMetadata(nonExistentID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("\nwanted:\nsql.ErrNoRows\ngot:\n%v", err)
		}
	})
}

func TestTrafficRepo_UpdateMetadata(t *testing.T) {
	t.Run("should update metadata for a single request", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, map[string]any{"initial": "value"})
		wantMeta := map[string]any{"updated": "new_value", "number": float64(42)}

		err := repo.UpdateMetadata(wantMeta, reqID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetMetadata(reqID)
		if err != nil {
			t.Fatalf("getting metadata after update: %v", err)
		}

		if !reflect.DeepEqual(wantMeta, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, got)
		}
	})

	t.Run("should update metadata for multiple requests", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID1 := testRequest(t, repo, map[string]any{"id": "1"})
		reqID2 := testRequest(t, repo, map[string]any{"id": "2"})
		wantMeta := map[string]any{"batch": "updated"}

		err := repo.UpdateMetadata(wantMeta, reqID1, reqID2)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got1, err := repo.GetMetadata(reqID1)
		if err != nil {
			t.Fatalf("getting metadata for req1: %v", err)
		}

		got2, err := repo.GetMetadata(reqID2)
		if err != nil {
			t.Fatalf("getting metadata for req2: %v", err)
		}

		if !reflect.DeepEqual(wantMeta, got1) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, got1)
		}

		if !reflect.DeepEqual(wantMeta, got2) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantMeta, got2)
		}
	})
}

func TestTrafficRepo_GetNotes(t *testing.T) {
	t.Run("should return error if no note exists", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)

		_, err := repo.GetNote(reqID)
		if err == nil {
			t.Fatalf("\nwanted:\nerror\ngot:\nnil")
		}

		if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("\nwanted:\nsql.ErrNoRows\ngot:\n%v", err)
		}
	})
}
func TestTrafficRepo_Notes(t *testing.T) {
	t.Run("should add a new note", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)
		wantNote := "Marasi's note"

		err := repo.UpdateNote(reqID, wantNote)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetNote(reqID)
		if err != nil {
			t.Fatalf("getting note after update: %v", err)
		}

		if got != wantNote {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantNote, got)
		}
	})

	t.Run("should update an existing note", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)

		err := repo.UpdateNote(reqID, "Initial note")
		if err != nil {
			t.Fatalf("inserting initial note: %v", err)
		}

		wantNote := "Marasi's updated note"

		err = repo.UpdateNote(reqID, wantNote)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetNote(reqID)
		if err != nil {
			t.Fatalf("getting note after update: %v", err)
		}

		if got != wantNote {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantNote, got)
		}
	})
}

func TestTrafficRepo_NoteTriggers(t *testing.T) {
	t.Run("adding a note should set has_note in request metadata", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)

		err := repo.UpdateNote(reqID, "This note should trigger")
		if err != nil {
			t.Fatalf("updating note: %v", err)
		}

		meta, err := repo.GetMetadata(reqID)
		if err != nil {
			t.Fatalf("getting metadata: %v", err)
		}

		hasNote, ok := meta["has_note"]
		if !ok {
			t.Fatalf("\nwanted:\nmetadata to have 'has_note' key\ngot:\nkey not found")
		}
		if hasNote != float64(1) {
			t.Fatalf("\nwanted:\ntrue\ngot:\n%v", hasNote)
		}
	})

	t.Run("updating a note to empty string should remove has_note", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		reqID := testRequest(t, repo, nil)

		err := repo.UpdateNote(reqID, "This note will be removed")
		if err != nil {
			t.Fatalf("updating note: %v", err)
		}

		err = repo.UpdateNote(reqID, "")
		if err != nil {
			t.Fatalf("removing note: %v", err)
		}

		meta, err := repo.GetMetadata(reqID)
		if err != nil {
			t.Fatalf("getting metadata: %v", err)
		}

		if _, ok := meta["has_note"]; ok {
			t.Fatalf("\nwanted:\n'has_note' key to be removed\ngot:\nkey still exists")
		}
	})
}
