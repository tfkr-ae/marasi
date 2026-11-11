package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// countingReportingRepo counts per-record reads, so a test can prove the full
// list does not fan out into one query per record.
type countingReportingRepo struct {
	*db.Repository
	perRecord atomic.Int32
}

func (repo *countingReportingRepo) GetFinding(id uuid.UUID) (*domain.Finding, error) {
	repo.perRecord.Add(1)
	return repo.Repository.GetFinding(id)
}

func (repo *countingReportingRepo) GetFindingRequests(id uuid.UUID) ([]*domain.RequestResponseSummary, error) {
	repo.perRecord.Add(1)
	return repo.Repository.GetFindingRequests(id)
}

func (repo *countingReportingRepo) GetTestCase(id uuid.UUID) (*domain.TestCase, error) {
	repo.perRecord.Add(1)
	return repo.Repository.GetTestCase(id)
}

func (repo *countingReportingRepo) GetTestCaseRequests(id uuid.UUID) ([]*domain.RequestResponseSummary, error) {
	repo.perRecord.Add(1)
	return repo.Repository.GetTestCaseRequests(id)
}

func serveLogbook(t *testing.T, server *Server, method, target, body string) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, target, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

func createLogbookRecord(t *testing.T, server *Server, resource, body string) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(serveLogbook(t, server, http.MethodPost, "/"+resource, body), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

// TestLogbookFullLists proves that GET /finding?full=1 and GET
// /test-case?full=1 return each record exactly as its per-id route does, and
// that the list without full keeps the summary form.
func TestLogbookFullLists(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/logbook.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := &countingReportingRepo{Repository: db.NewProxyRepo(conn)}
	server := newTestServer(&marasi.Proxy{ReportingRepo: repo, TrafficRepo: repo.Repository}, func() {})

	// Three requests, inserted out of time order, so a list that does not
	// keep each record's traffic oldest-first differs from the per-id read.
	base := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	requestIDs := make([]uuid.UUID, 3)
	for i, offset := range []int{2, 0, 1} {
		requestIDs[i] = uuid.New()
		if err := repo.InsertRequest(&domain.ProxyRequest{ID: requestIDs[i], Method: "GET", Scheme: "https", Host: "example.test", Path: fmt.Sprintf("/r%d", i), RequestedAt: base.Add(time.Duration(offset) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}

	testCaseLinked := createLogbookRecord(t, server, "test-case", `{"title":"auth","description":"d","category":"Authentication","tags":["a","b"],"note":"n"}`)
	testCaseBare := createLogbookRecord(t, server, "test-case", `{"title":"bare"}`)
	findingLinked := createLogbookRecord(t, server, "finding", fmt.Sprintf(`{"title":"sqli","severity":"high","cvss_vector":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H","writeup":"w","treatment_plan":"p","test_case_id":%q}`, testCaseLinked))
	findingBare := createLogbookRecord(t, server, "finding", `{"title":"bare"}`)

	for _, link := range []struct{ resource, id string }{{"test-case", testCaseLinked}, {"finding", findingLinked}} {
		for _, requestID := range requestIDs {
			serveLogbook(t, server, http.MethodPost, "/"+link.resource+"/"+link.id+"/traffic", fmt.Sprintf(`{"id":%q}`, requestID))
		}
		serveLogbook(t, server, http.MethodPost, "/"+link.resource+"/"+link.id+"/artifact?filename=one.txt", "first")
		serveLogbook(t, server, http.MethodPost, "/"+link.resource+"/"+link.id+"/artifact?filename=two.txt", "second")
	}
	// Traffic linked to the other kind of record must not leak across.
	serveLogbook(t, server, http.MethodPost, "/test-case/"+testCaseBare+"/traffic", fmt.Sprintf(`{"id":%q}`, requestIDs[0]))

	summaryKeys := map[string][]string{
		"finding":   {"created_at", "cvss_score", "id", "severity", "test_case_id", "title"},
		"test-case": {"category", "created_at", "id", "tags", "title"},
	}
	for resource, ids := range map[string][]string{
		"finding":   {findingLinked, findingBare},
		"test-case": {testCaseLinked, testCaseBare},
	} {
		t.Run(resource, func(t *testing.T) {
			perID := map[string]json.RawMessage{}
			for _, id := range ids {
				perID[id] = json.RawMessage(serveLogbook(t, server, http.MethodGet, "/"+resource+"/"+id, ""))
			}

			repo.perRecord.Store(0)
			var full struct {
				Items []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(serveLogbook(t, server, http.MethodGet, "/"+resource+"?full=1", ""), &full); err != nil {
				t.Fatal(err)
			}
			if n := repo.perRecord.Load(); n != 0 {
				t.Fatalf("full list made %d per-record reads", n)
			}
			if len(full.Items) != len(ids) {
				t.Fatalf("full list has %d items, want %d", len(full.Items), len(ids))
			}
			seen := map[string]bool{}
			for _, item := range full.Items {
				var head struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(item, &head); err != nil {
					t.Fatal(err)
				}
				want, ok := perID[head.ID]
				if !ok {
					t.Fatalf("full list has unknown id %s", head.ID)
				}
				// The per-id body ends in a newline from the JSON encoder.
				if got := string(item); got != strings.TrimSpace(string(want)) {
					t.Fatalf("full item for %s differs from per-id read\n got: %s\nwant: %s", head.ID, got, want)
				}
				seen[head.ID] = true
			}
			if len(seen) != len(ids) {
				t.Fatalf("full list repeats ids: %v", seen)
			}

			linked := perID[ids[0]]
			var detail struct {
				Items     []struct{ ID uuid.UUID }    `json:"items"`
				Artifacts []struct{ Filename string } `json:"artifacts"`
			}
			if err := json.Unmarshal(linked, &detail); err != nil {
				t.Fatal(err)
			}
			if len(detail.Items) != 3 || len(detail.Artifacts) != 2 {
				t.Fatalf("fixture did not link traffic and artifacts: %s", linked)
			}

			defaultBody := serveLogbook(t, server, http.MethodGet, "/"+resource, "")
			for _, query := range []string{"?full=0", "?full=false"} {
				if got := serveLogbook(t, server, http.MethodGet, "/"+resource+query, ""); string(got) != string(defaultBody) {
					t.Fatalf("%s differs from the default list\n got: %s\nwant: %s", query, got, defaultBody)
				}
			}
			var summaries struct {
				Items []map[string]json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(defaultBody, &summaries); err != nil {
				t.Fatal(err)
			}
			if len(summaries.Items) != len(ids) {
				t.Fatalf("default list has %d items", len(summaries.Items))
			}
			for _, item := range summaries.Items {
				keys := make([]string, 0, len(item))
				for key := range item {
					keys = append(keys, key)
				}
				slices.Sort(keys)
				if !slices.Equal(keys, summaryKeys[resource]) {
					t.Fatalf("default list item keys %v, want %v", keys, summaryKeys[resource])
				}
				var record map[string]json.RawMessage
				if err := json.Unmarshal(perID[strings.Trim(string(item["id"]), `"`)], &record); err != nil {
					t.Fatal(err)
				}
				for key, value := range item {
					if string(value) != string(record[key]) {
						t.Fatalf("default list %s = %s, per-id %s", key, value, record[key])
					}
				}
			}

			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+resource+"?full=maybe", nil))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("full=maybe: %d %s", w.Code, w.Body)
			}
		})
	}
}
