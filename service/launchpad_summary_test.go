package service

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestLaunchpadListEntrySummary(t *testing.T) {
	conn, err := db.New(filepath.Join(t.TempDir(), "launchpads.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	repo := db.NewProxyRepo(conn)
	t.Cleanup(func() { repo.Close() })

	empty := createSummaryLaunchpad(t, repo, "Empty")
	inFlight := createSummaryLaunchpad(t, repo, "In flight")
	big := createSummaryLaunchpad(t, repo, "Big")

	answered := insertSummaryPair(t, repo, 200)
	pending := insertSummaryPair(t, repo, 0)
	linkSummaryPairs(t, repo, inFlight, answered, pending)

	for range 499 {
		linkSummaryPairs(t, repo, big, insertSummaryPair(t, repo, 200))
	}
	linkSummaryPairs(t, repo, big, insertSummaryPair(t, repo, 404))

	server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo}, func() {})
	response := requestLaunchpad(server, http.MethodGet, "/launchpad", "")
	want := `{"items":[` +
		`{"id":"` + empty.String() + `","name":"Empty","description":"","entry_count":0,"last_status_code":null},` +
		`{"id":"` + inFlight.String() + `","name":"In flight","description":"","entry_count":2,"last_status_code":null},` +
		`{"id":"` + big.String() + `","name":"Big","description":"","entry_count":500,"last_status_code":404}` +
		`]}` + "\n"
	if response.Code != http.StatusOK || response.Body.String() != want {
		t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
	}
}

func createSummaryLaunchpad(t *testing.T, repo *db.Repository, name string) uuid.UUID {
	t.Helper()
	id, err := repo.CreateLaunchpad(name, "")
	if err != nil {
		t.Fatalf("creating launchpad: %v", err)
	}
	return id
}

// insertSummaryPair stores a request/response pair. A statusCode of 0 leaves
// the pair in flight, with no response.
func insertSummaryPair(t *testing.T, repo *db.Repository, statusCode int) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	request := &domain.ProxyRequest{
		ID: id, Scheme: "https", Method: "GET", Host: "example.com", Path: "/",
		Raw: []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
	}
	if err := repo.InsertRequest(request); err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	if statusCode == 0 {
		return id
	}
	response := &domain.ProxyResponse{
		ID: id, Status: fmt.Sprintf("%d X", statusCode), StatusCode: statusCode,
		Raw: fmt.Appendf(nil, "HTTP/1.1 %d X\r\n\r\n", statusCode), Metadata: map[string]any{}, RespondedAt: time.Now(),
	}
	if err := repo.InsertResponse(response); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	return id
}

func linkSummaryPairs(t *testing.T, repo *db.Repository, launchpadID uuid.UUID, requestIDs ...uuid.UUID) {
	t.Helper()
	for _, requestID := range requestIDs {
		if err := repo.LinkRequestToLaunchpad(requestID, launchpadID); err != nil {
			t.Fatalf("linking request: %v", err)
		}
	}
}
