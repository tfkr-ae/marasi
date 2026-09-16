package service

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type stubFindingRepository struct {
	domain.ReportingRepository
	findings  map[uuid.UUID]*domain.Finding
	testCases map[uuid.UUID]*domain.TestCase
	order     []uuid.UUID
	traffic   map[uuid.UUID]*domain.RequestResponseSummary
}

func (repo *stubFindingRepository) SaveFinding(finding *domain.Finding) error {
	if repo.findings == nil {
		repo.findings = make(map[uuid.UUID]*domain.Finding)
	}
	if existing, ok := repo.findings[finding.ID]; ok {
		finding.CreatedAt = existing.CreatedAt
	} else {
		finding.CreatedAt = time.Date(2026, 9, 16, 10, 11, 12, 0, time.UTC)
		repo.order = append([]uuid.UUID{finding.ID}, repo.order...)
	}
	copy := *finding
	repo.findings[finding.ID] = &copy
	return nil
}

func (repo *stubFindingRepository) GetFinding(id uuid.UUID) (*domain.Finding, error) {
	finding, ok := repo.findings[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	copy := *finding
	return &copy, nil
}

func (repo *stubFindingRepository) GetFindingRequests(id uuid.UUID) ([]*domain.RequestResponseSummary, error) {
	finding, ok := repo.findings[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	items := make([]*domain.RequestResponseSummary, 0, len(finding.Requests))
	for _, requestID := range finding.Requests {
		items = append(items, repo.traffic[requestID])
	}
	return items, nil
}

func (repo *stubFindingRepository) LinkRequestToFinding(findingID, requestID uuid.UUID) error {
	finding, ok := repo.findings[findingID]
	if !ok {
		return sql.ErrNoRows
	}
	for _, linkedID := range finding.Requests {
		if linkedID == requestID {
			return domain.ErrReportingAlreadyLinked
		}
	}
	finding.Requests = append(finding.Requests, requestID)
	return nil
}

func (repo *stubFindingRepository) UnlinkRequestFromFinding(findingID, requestID uuid.UUID) error {
	finding, ok := repo.findings[findingID]
	if !ok {
		return sql.ErrNoRows
	}
	for i, linkedID := range finding.Requests {
		if linkedID == requestID {
			finding.Requests = append(finding.Requests[:i], finding.Requests[i+1:]...)
			return nil
		}
	}
	return domain.ErrReportingNotLinked
}

func (repo *stubFindingRepository) ListFindings() ([]*domain.Finding, error) {
	items := make([]*domain.Finding, 0, len(repo.order))
	for _, id := range repo.order {
		copy := *repo.findings[id]
		items = append(items, &copy)
	}
	return items, nil
}

func (repo *stubFindingRepository) DeleteFinding(id uuid.UUID) error {
	if _, ok := repo.findings[id]; !ok {
		return sql.ErrNoRows
	}
	delete(repo.findings, id)
	return nil
}

func (repo *stubFindingRepository) GetTestCase(id uuid.UUID) (*domain.TestCase, error) {
	testCase, ok := repo.testCases[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return testCase, nil
}

func TestFindingControlAPI(t *testing.T) {
	t.Run("links and unlinks traffic with events", func(t *testing.T) {
		findingID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		requestID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubFindingRepository{
			findings: map[uuid.UUID]*domain.Finding{findingID: {ID: findingID, Title: "Access control"}},
			traffic:  map[uuid.UUID]*domain.RequestResponseSummary{requestID: {ID: requestID, Method: "POST", Host: "example.com", Path: "/login", Status: "200 OK", StatusCode: 200, Metadata: map[string]any{}, RequestedAt: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC), RespondedAt: time.Date(2026, 9, 15, 10, 0, 1, 0, time.UTC)}},
		}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo, TrafficRepo: &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: requestID}}}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/finding/"+findingID.String()+"/traffic", strings.NewReader(`{"id":"`+requestID.String()+`"}`)))
		want := `{"finding_id":"` + findingID.String() + `","id":"` + requestID.String() + `"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted linked response %s, got %d %s", want, response.Code, response.Body.String())
		}
		if event := <-subscriber.events; event.name != "finding.linked" {
			t.Fatalf("wanted finding.linked, got %s", event.name)
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/finding/"+findingID.String(), nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[{"id":"`+requestID.String()+`"`) || !strings.Contains(response.Body.String(), `"status_code":200`) {
			t.Fatalf("wanted response-bearing traffic summary, got %d %s", response.Code, response.Body.String())
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/finding/"+findingID.String()+"/traffic/"+requestID.String(), nil))
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted unlinked response %s, got %d %s", want, response.Code, response.Body.String())
		}
		if event := <-subscriber.events; event.name != "finding.unlinked" {
			t.Fatalf("wanted finding.unlinked, got %s", event.name)
		}
	})

	t.Run("creates a title-only finding without a default severity", func(t *testing.T) {
		repo := &stubFindingRepository{}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/finding", strings.NewReader(`{"title":"Broken access control"}`)))

		var saved *domain.Finding
		for _, saved = range repo.findings {
		}
		if saved == nil || saved.ID.Version() != 7 || saved.Severity != "" {
			t.Fatalf("wanted one UUID v7 finding with empty severity, got %+v", saved)
		}
		want := fmt.Sprintf("{\"id\":%q,\"test_case_id\":null,\"title\":\"Broken access control\",\"severity\":\"\",\"cvss_vector\":\"\",\"cvss_score\":0,\"writeup\":\"\",\"treatment_plan\":\"\",\"created_at\":\"2026-09-16T10:11:12Z\"}\n", saved.ID)
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted 200 %s, got %d %s", want, response.Code, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "finding.created" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("wanted finding.created %s, got %s %s", strings.TrimSpace(want), event.name, event.data)
		}
	})

	t.Run("lists newest-first and gets empty memberships", func(t *testing.T) {
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubFindingRepository{
			findings: map[uuid.UUID]*domain.Finding{
				newer: {ID: newer, Title: "New", Severity: "High", CVSSScore: 8.1, CreatedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)},
				older: {ID: older, Title: "Old", CreatedAt: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)},
			},
			order: []uuid.UUID{newer, older},
		}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/finding", nil))
		wantList := `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","title":"New","severity":"High","test_case_id":null,"cvss_score":8.1,"created_at":"2026-09-16T10:00:00Z"},{"id":"0193802f-f0e7-73d9-a764-06d21e367809","title":"Old","severity":"","test_case_id":null,"cvss_score":0,"created_at":"2026-09-15T10:00:00Z"}]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != wantList {
			t.Fatalf("wanted 200 %s, got %d %s", wantList, response.Code, response.Body.String())
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/finding/"+newer.String(), nil))
		wantGet := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","test_case_id":null,"title":"New","severity":"High","cvss_vector":"","cvss_score":8.1,"writeup":"","treatment_plan":"","created_at":"2026-09-16T10:00:00Z","items":[],"artifacts":[]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != wantGet {
			t.Fatalf("wanted 200 %s, got %d %s", wantGet, response.Code, response.Body.String())
		}

		empty := newTestServer(&marasi.Proxy{ReportingRepo: &stubFindingRepository{}}, func() {})
		response = httptest.NewRecorder()
		empty.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/finding", nil))
		if response.Body.String() != "{\"items\":[]}\n" {
			t.Fatalf("wanted stable empty list, got %s", response.Body.String())
		}
	})

	t.Run("sets, preserves, and clears a test case", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		testCaseID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubFindingRepository{
			findings:  map[uuid.UUID]*domain.Finding{id: {ID: id, Title: "Old", TestCaseID: &testCaseID, Severity: "Low", CreatedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)}},
			testCases: map[uuid.UUID]*domain.TestCase{testCaseID: {ID: testCaseID}},
		}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/finding/"+id.String(), strings.NewReader(`{"title":"New"}`)))
		if response.Code != http.StatusOK || repo.findings[id].TestCaseID == nil || *repo.findings[id].TestCaseID != testCaseID {
			t.Fatalf("wanted omitted test_case_id preserved, got %d %+v", response.Code, repo.findings[id])
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/finding/"+id.String(), strings.NewReader(`{"test_case_id":null}`)))
		if response.Code != http.StatusOK || repo.findings[id].TestCaseID != nil {
			t.Fatalf("wanted test_case_id cleared, got %d %+v", response.Code, repo.findings[id])
		}
	})

	t.Run("rejects invalid writes and missing related test cases", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{ReportingRepo: &stubFindingRepository{}}, func() {})
		for _, body := range []string{
			`{}`, `{"title":""}`, `{"title":null}`, `{"title":"x","severity":"high"}`,
			`{"title":"x","severity":null}`, `{"title":"x","test_case_id":""}`,
			`{"title":"x","requests":[]}`, `{"title":"x"} {}`, `{`,
		} {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/finding", strings.NewReader(body)))
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_finding_request\"}\n" {
				t.Fatalf("body %s wanted invalid_finding_request, got %d %s", body, response.Code, response.Body.String())
			}
		}

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/finding", strings.NewReader(`{"title":"x","test_case_id":"0193802f-f0e7-73d9-a764-06d21e367809"}`)))
		if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("wanted missing test case rejected, got %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("deletes and reports malformed or missing ids", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubFindingRepository{findings: map[uuid.UUID]*domain.Finding{id: {ID: id, Title: "Delete me"}}}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/finding/"+id.String(), nil))
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted 200 %s, got %d %s", want, response.Code, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "finding.deleted" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("wanted finding.deleted, got %s %s", event.name, event.data)
		}

		for _, path := range []string{"/finding/not-a-uuid", "/finding/0193802f-f0e7-73d9-a764-06d21e367809"} {
			response = httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if path == "/finding/not-a-uuid" && response.Code != http.StatusBadRequest || path != "/finding/not-a-uuid" && response.Code != http.StatusNotFound {
				t.Fatalf("GET %s returned %d %s", path, response.Code, response.Body.String())
			}
		}
	})
}
