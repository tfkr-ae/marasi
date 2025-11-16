package db

import (
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func setupTestDB(t *testing.T) (*Repository, func()) {
	t.Helper()

	tempFile, err := os.CreateTemp(t.TempDir(), "test_*.db")
	if err != nil {
		t.Fatalf("os.CreateTemp() failed: %v", err)
	}
	tempFile.Close()

	dbConn, err := New(tempFile.Name())
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
