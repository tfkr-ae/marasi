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

type stubReportingRepository struct {
	domain.ReportingRepository
	testCases map[uuid.UUID]*domain.TestCase
	order     []uuid.UUID
	traffic   map[uuid.UUID]*domain.RequestResponseSummary
}

func (repo *stubReportingRepository) SaveTestCase(testCase *domain.TestCase) error {
	if repo.testCases == nil {
		repo.testCases = make(map[uuid.UUID]*domain.TestCase)
	}
	if existing, ok := repo.testCases[testCase.ID]; ok {
		testCase.CreatedAt = existing.CreatedAt
	} else {
		testCase.CreatedAt = time.Date(2026, 9, 16, 10, 11, 12, 0, time.UTC)
		repo.order = append([]uuid.UUID{testCase.ID}, repo.order...)
	}
	copy := *testCase
	copy.Tags = append([]string(nil), testCase.Tags...)
	repo.testCases[testCase.ID] = &copy
	return nil
}

func (repo *stubReportingRepository) ListTestCases() ([]*domain.TestCase, error) {
	items := make([]*domain.TestCase, 0, len(repo.order))
	for _, id := range repo.order {
		copy := *repo.testCases[id]
		items = append(items, &copy)
	}
	return items, nil
}

func (repo *stubReportingRepository) GetTestCase(id uuid.UUID) (*domain.TestCase, error) {
	testCase, ok := repo.testCases[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	copy := *testCase
	return &copy, nil
}

func (repo *stubReportingRepository) GetTestCaseRequests(id uuid.UUID) ([]*domain.RequestResponseSummary, error) {
	testCase, ok := repo.testCases[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	items := make([]*domain.RequestResponseSummary, 0, len(testCase.Requests))
	for _, requestID := range testCase.Requests {
		items = append(items, repo.traffic[requestID])
	}
	return items, nil
}

func (repo *stubReportingRepository) LinkRequestToTestCase(testCaseID, requestID uuid.UUID) error {
	testCase, ok := repo.testCases[testCaseID]
	if !ok {
		return sql.ErrNoRows
	}
	for _, linkedID := range testCase.Requests {
		if linkedID == requestID {
			return domain.ErrReportingAlreadyLinked
		}
	}
	testCase.Requests = append(testCase.Requests, requestID)
	return nil
}

func (repo *stubReportingRepository) UnlinkRequestFromTestCase(testCaseID, requestID uuid.UUID) error {
	testCase, ok := repo.testCases[testCaseID]
	if !ok {
		return sql.ErrNoRows
	}
	for i, linkedID := range testCase.Requests {
		if linkedID == requestID {
			testCase.Requests = append(testCase.Requests[:i], testCase.Requests[i+1:]...)
			return nil
		}
	}
	return domain.ErrReportingNotLinked
}

func (repo *stubReportingRepository) DeleteTestCase(id uuid.UUID) error {
	if _, ok := repo.testCases[id]; !ok {
		return sql.ErrNoRows
	}
	delete(repo.testCases, id)
	return nil
}

func TestTestCaseControlAPI(t *testing.T) {
	t.Run("links, returns, and unlinks traffic", func(t *testing.T) {
		testCaseID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		requestID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		requestedAt := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
		repo := &stubReportingRepository{
			testCases: map[uuid.UUID]*domain.TestCase{testCaseID: {ID: testCaseID, Title: "Access control", Tags: []string{}}},
			traffic:   map[uuid.UUID]*domain.RequestResponseSummary{requestID: {ID: requestID, Scheme: "https", Method: "GET", Host: "example.com", Path: "/account", Status: "N/A", StatusCode: -1, Length: "0", Metadata: map[string]any{}, RequestedAt: requestedAt}},
		}
		trafficRepo := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: requestID}}}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo, TrafficRepo: trafficRepo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test-case/"+testCaseID.String()+"/traffic", strings.NewReader(`{"id":"`+requestID.String()+`"}`)))
		wantLink := `{"test_case_id":"` + testCaseID.String() + `","id":"` + requestID.String() + `"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != wantLink {
			t.Fatalf("wanted linked response %s, got %d %s", wantLink, response.Code, response.Body.String())
		}
		if event := <-subscriber.events; event.name != "test-case.linked" || string(event.data) != strings.TrimSpace(wantLink) {
			t.Fatalf("wanted linked event, got %s %s", event.name, event.data)
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case/"+testCaseID.String(), nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[{"id":"`+requestID.String()+`","scheme":"https","method":"GET"`) || !strings.Contains(response.Body.String(), `"status_code":-1`) || !strings.Contains(response.Body.String(), `"responded_at":null`) {
			t.Fatalf("wanted linked in-flight traffic summary, got %d %s", response.Code, response.Body.String())
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test-case/"+testCaseID.String()+"/traffic", strings.NewReader(`{"id":"`+requestID.String()+`"}`)))
		if response.Code != http.StatusConflict || response.Body.String() != "{\"error\":\"already_linked\"}\n" {
			t.Fatalf("wanted duplicate conflict, got %d %s", response.Code, response.Body.String())
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/test-case/"+testCaseID.String()+"/traffic/"+requestID.String(), nil))
		if response.Code != http.StatusOK || response.Body.String() != wantLink {
			t.Fatalf("wanted unlinked response %s, got %d %s", wantLink, response.Code, response.Body.String())
		}
		if event := <-subscriber.events; event.name != "test-case.unlinked" || string(event.data) != strings.TrimSpace(wantLink) {
			t.Fatalf("wanted unlinked event, got %s %s", event.name, event.data)
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/test-case/"+testCaseID.String()+"/traffic/"+requestID.String(), nil))
		if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("wanted missing link not found, got %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("creates a title-only test case and publishes it", func(t *testing.T) {
		repo := &stubReportingRepository{}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		request := httptest.NewRequest(http.MethodPost, "/test-case", strings.NewReader(`{"title":"Check access control"}`))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)

		if len(repo.testCases) != 1 {
			t.Fatalf("wanted one saved test case, got %d", len(repo.testCases))
		}
		var saved *domain.TestCase
		for _, saved = range repo.testCases {
		}
		if saved.ID.Version() != 7 {
			t.Fatalf("wanted UUID v7, got %s", saved.ID)
		}
		want := fmt.Sprintf("{\"id\":%q,\"title\":\"Check access control\",\"description\":\"\",\"category\":\"\",\"tags\":[],\"note\":\"\",\"created_at\":\"2026-09-16T10:11:12Z\"}\n", saved.ID)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != want {
			t.Fatalf("wanted 200 application/json %s, got %d %s %s", want, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "test-case.created" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("wanted test-case.created %s, got %s %s", strings.TrimSpace(want), event.name, event.data)
		}
	})

	t.Run("lists test cases newest-first and keeps an empty list stable", func(t *testing.T) {
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubReportingRepository{
			testCases: map[uuid.UUID]*domain.TestCase{
				newer: {ID: newer, Title: "New", Description: "not in summary", Category: "Web", Tags: []string{"auth"}, Note: "not in summary", CreatedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)},
				older: {ID: older, Title: "Old", Category: "API", Tags: []string{}, CreatedAt: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)},
			},
			order: []uuid.UUID{newer, older},
		}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case", nil))

		want := `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","title":"New","category":"Web","tags":["auth"],"created_at":"2026-09-16T10:00:00Z"},{"id":"0193802f-f0e7-73d9-a764-06d21e367809","title":"Old","category":"API","tags":[],"created_at":"2026-09-15T10:00:00Z"}]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted 200 %s, got %d %s", want, response.Code, response.Body.String())
		}

		empty := newTestServer(&marasi.Proxy{ReportingRepo: &stubReportingRepository{}}, func() {})
		response = httptest.NewRecorder()
		empty.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case", nil))
		if response.Code != http.StatusOK || response.Body.String() != "{\"items\":[]}\n" {
			t.Fatalf("wanted stable empty list, got %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("gets a test case with empty memberships and handles invalid ids", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubReportingRepository{testCases: map[uuid.UUID]*domain.TestCase{
			id: {ID: id, Title: "Access control", Description: "Try another user", Category: "Web", Tags: []string{"auth"}, Note: "started", CreatedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)},
		}}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case/"+id.String(), nil))
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","title":"Access control","description":"Try another user","category":"Web","tags":["auth"],"note":"started","created_at":"2026-09-16T10:00:00Z","items":[],"artifacts":[]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted 200 %s, got %d %s", want, response.Code, response.Body.String())
		}

		for _, test := range []struct {
			id     string
			status int
			body   string
		}{
			{id: "not-a-uuid", status: http.StatusBadRequest, body: "{\"error\":\"bad_request\"}\n"},
			{id: "0193802f-f0e7-73d9-a764-06d21e367809", status: http.StatusNotFound, body: "{\"error\":\"not_found\"}\n"},
		} {
			response = httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case/"+test.id, nil))
			if response.Code != test.status || response.Body.String() != test.body {
				t.Fatalf("GET %s wanted %d %s, got %d %s", test.id, test.status, test.body, response.Code, response.Body.String())
			}
		}
	})

	t.Run("updates only supplied fields, clears tags, and publishes the result", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubReportingRepository{testCases: map[uuid.UUID]*domain.TestCase{
			id: {ID: id, Title: "Old title", Description: "keep", Category: "Web", Tags: []string{"remove"}, Note: "keep", CreatedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)},
		}}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test-case/"+id.String(), strings.NewReader(`{"title":"New title","tags":[]}`)))
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","title":"New title","description":"keep","category":"Web","tags":[],"note":"keep","created_at":"2026-09-16T10:00:00Z"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted 200 %s, got %d %s", want, response.Code, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "test-case.updated" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("wanted test-case.updated %s, got %s %s", strings.TrimSpace(want), event.name, event.data)
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test-case/"+id.String(), strings.NewReader(`{"title":""}`)))
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_test_case_request\"}\n" {
			t.Fatalf("wanted empty title rejected, got %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("deletes an existing test case and publishes its id", func(t *testing.T) {
		id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubReportingRepository{testCases: map[uuid.UUID]*domain.TestCase{id: {ID: id, Title: "Delete me"}}}
		server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/test-case/"+id.String(), nil))
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted 200 %s, got %d %s", want, response.Code, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "test-case.deleted" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("wanted test-case.deleted %s, got %s %s", strings.TrimSpace(want), event.name, event.data)
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/test-case/"+id.String(), nil))
		if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("wanted missing delete rejected, got %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("rejects invalid create bodies", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{ReportingRepo: &stubReportingRepository{}}, func() {})
		for _, body := range []string{
			`{}`,
			`{"title":""}`,
			`{"title":null}`,
			`{"title":"x","tags":null}`,
			`{"title":"x","requests":[]}`,
			`{"title":"x"} {}`,
			`{`,
		} {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test-case", strings.NewReader(body)))
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_test_case_request\"}\n" {
				t.Fatalf("body %s wanted invalid_test_case_request, got %d %s", body, response.Code, response.Body.String())
			}
		}
	})
}
