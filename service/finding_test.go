package service

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// Pause after real SQLite reads, so concurrent HTTP mutations see the same
// snapshot without replacing persistence with a mock.
type reportingReadBarrier struct {
	*db.Repository
	remaining atomic.Int32
	read      chan struct{}
	release   chan struct{}
}

func (repo *reportingReadBarrier) pause() {
	if repo.remaining.Add(-1) >= 0 {
		repo.read <- struct{}{}
		<-repo.release
	}
}

func (repo *reportingReadBarrier) GetFinding(id uuid.UUID) (*domain.Finding, error) {
	finding, err := repo.Repository.GetFinding(id)
	repo.pause()
	return finding, err
}

func (repo *reportingReadBarrier) GetTestCase(id uuid.UUID) (*domain.TestCase, error) {
	testCase, err := repo.Repository.GetTestCase(id)
	repo.pause()
	return testCase, err
}

func newReportingSQLiteControl(t *testing.T, resource string) (*Server, *reportingReadBarrier, string) {
	t.Helper()
	conn, err := db.New(t.TempDir()+"/reporting.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := &reportingReadBarrier{Repository: db.NewProxyRepo(conn), read: make(chan struct{}, 2), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-repo.release:
		default:
			close(repo.release)
		}
	})
	server := newTestServer(&marasi.Proxy{ReportingRepo: repo, TrafficRepo: repo.Repository}, func() {})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("POST", "/"+resource, strings.NewReader(`{"title":"original"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return server, repo, "/" + resource + "/" + created.ID.String()
}

func waitReportingRead(t *testing.T, repo *reportingReadBarrier) {
	t.Helper()
	select {
	case <-repo.read:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP mutation did not reach SQLite read barrier")
	}
}

func TestFindingConcurrentControlEdits(t *testing.T) {
	server, repo, path := newReportingSQLiteControl(t, "finding")
	repo.remaining.Store(2)
	done := make(chan *httptest.ResponseRecorder, 2)
	for _, body := range []string{`{"title":"changed-title"}`, `{"writeup":"changed-writeup","severity":"high","cvss_vector":"vector","cvss_score":7.5,"treatment_plan":"remediate"}`} {
		go func() {
			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
			done <- w
		}()
	}
	waitReportingRead(t, repo)
	waitReportingRead(t, repo)
	close(repo.release)
	for range 2 {
		w := <-done
		if w.Code != http.StatusOK {
			t.Fatalf("edit: %d %s", w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	var got findingDetail
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || got.Title != "changed-title" || got.WriteUp != "changed-writeup" || got.Severity != "High" || got.CVSSVector != "vector" || got.CVSSScore != 7.5 || got.TreatmentPlan != "remediate" {
		t.Fatalf("concurrent edits lost: %d %s", w.Code, w.Body)
	}
}

func TestFindingSQLiteControlNullableAssociation(t *testing.T) {
	server, _, path := newReportingSQLiteControl(t, "finding")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("POST", "/test-case", strings.NewReader(`{"title":"parent"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("create test case: %d %s", w.Code, w.Body)
	}
	var parent testCaseResponse
	if err := json.Unmarshal(w.Body.Bytes(), &parent); err != nil {
		t.Fatal(err)
	}
	var createdAt time.Time
	for _, step := range []struct {
		body   string
		linked bool
	}{
		{fmt.Sprintf(`{"test_case_id":%q,"severity":"High","cvss_vector":"vector","cvss_score":7.5,"writeup":"writeup","treatment_plan":"plan"}`, parent.ID), true},
		{`{}`, true},
		{`{"title":"renamed"}`, true},
		{`{"test_case_id":null,"severity":"","cvss_vector":"","cvss_score":0,"writeup":"","treatment_plan":""}`, false},
		{`{}`, false},
	} {
		w = httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(step.body)))
		if w.Code != http.StatusOK {
			t.Fatalf("patch %s: %d %s", step.body, w.Code, w.Body)
		}
		w = httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var got findingDetail
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || step.linked && (got.TestCaseID == nil || *got.TestCaseID != parent.ID) || !step.linked && got.TestCaseID != nil {
			t.Fatalf("association after %s: %d %s", step.body, w.Code, w.Body)
		}
		if !step.linked && (got.Severity != "" || got.CVSSVector != "" || got.CVSSScore != 0 || got.WriteUp != "" || got.TreatmentPlan != "") {
			t.Fatalf("zero-value fields were not cleared: %s", w.Body)
		}
		if createdAt.IsZero() {
			createdAt = got.CreatedAt
		}
		if !got.CreatedAt.Equal(createdAt) {
			t.Fatalf("patch changed creation time: %s", w.Body)
		}
	}
}

func TestReportingConcurrentControlMembershipAndDelete(t *testing.T) {
	for _, resource := range []string{"finding", "test-case"} {
		for _, operation := range []string{"link", "unlink", "delete", "delete-noop", "link-noop"} {
			t.Run(resource+"/"+operation, func(t *testing.T) {
				server, repo, path := newReportingSQLiteControl(t, resource)
				requestID := uuid.New()
				if err := repo.InsertRequest(&domain.ProxyRequest{ID: requestID, Method: "GET", Scheme: "http", Host: "example.test", Path: "/proof", RequestedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				linkBody := fmt.Sprintf(`{"id":%q}`, requestID)
				if operation == "unlink" {
					w := httptest.NewRecorder()
					server.ServeHTTP(w, httptest.NewRequest("POST", path+"/traffic", strings.NewReader(linkBody)))
					if w.Code != http.StatusOK {
						t.Fatalf("initial link: %d %s", w.Code, w.Body)
					}
				}
				body := `{"title":"changed-title"}`
				if strings.HasSuffix(operation, "-noop") {
					body = `{}`
				}
				repo.remaining.Store(1)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					w := httptest.NewRecorder()
					server.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
					done <- w
				}()
				waitReportingRead(t, repo)
				w := httptest.NewRecorder()
				switch operation {
				case "link", "link-noop":
					server.ServeHTTP(w, httptest.NewRequest("POST", path+"/traffic", strings.NewReader(linkBody)))
				case "unlink":
					server.ServeHTTP(w, httptest.NewRequest("DELETE", path+"/traffic/"+requestID.String(), nil))
				case "delete", "delete-noop":
					server.ServeHTTP(w, httptest.NewRequest("DELETE", path, nil))
				}
				if w.Code != http.StatusOK {
					t.Fatalf("interleaved %s: %d %s", operation, w.Code, w.Body)
				}
				close(repo.release)
				edit := <-done
				w = httptest.NewRecorder()
				server.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if strings.HasPrefix(operation, "delete") {
					if edit.Code != http.StatusNotFound || w.Code != http.StatusNotFound {
						t.Fatalf("edit resurrected deleted record: edit=%d %s get=%d %s", edit.Code, edit.Body, w.Code, w.Body)
					}
					return
				}
				var got struct {
					Title string           `json:"title"`
					Items []trafficSummary `json:"items"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				wantTitle := "changed-title"
				if operation == "link-noop" {
					wantTitle = "original"
				}
				if edit.Code != http.StatusOK || w.Code != http.StatusOK || got.Title != wantTitle {
					t.Fatalf("edit failed: %d %s get=%d %s", edit.Code, edit.Body, w.Code, w.Body)
				}
				if operation == "unlink" {
					if len(got.Items) != 0 {
						t.Fatalf("edit restored unlinked traffic: %s", w.Body)
					}
				} else if len(got.Items) != 1 || got.Items[0].ID != requestID {
					t.Fatalf("edit removed linked traffic: %s", w.Body)
				}
			})
		}
	}
}

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

func (repo *stubFindingRepository) UpdateFinding(id uuid.UUID, mutation domain.FindingMutation) error {
	finding, err := repo.GetFinding(id)
	if err != nil {
		return err
	}
	applyFindingMutation(finding, mutation)
	return repo.SaveFinding(finding)
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

	t.Run("stores the canonical severity regardless of case", func(t *testing.T) {
		for _, test := range []struct {
			in   string
			want string
		}{
			{in: "high", want: "High"},
			{in: "CRITICAL", want: "Critical"},
			{in: "medium", want: "Medium"},
			{in: "LoW", want: "Low"},
			{in: "informational", want: "Informational"},
		} {
			repo := &stubFindingRepository{}
			server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/finding", strings.NewReader(`{"title":"Broken access control","severity":"`+test.in+`"}`)))
			var saved *domain.Finding
			for _, saved = range repo.findings {
			}
			if response.Code != http.StatusOK || saved == nil || saved.Severity != test.want || !strings.Contains(response.Body.String(), `"severity":"`+test.want+`"`) {
				t.Fatalf("severity %q wanted %q, got %d %s saved %+v", test.in, test.want, response.Code, response.Body.String(), saved)
			}
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
			`{}`, `{"title":""}`, `{"title":null}`, `{"title":"x","severity":"urgent"}`,
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
