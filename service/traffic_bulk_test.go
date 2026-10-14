package service

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// bulkProject is a real project served by a test server.
type bulkProject struct {
	repo   *db.Repository
	server *Server
}

func setupBulkProject(t *testing.T) *bulkProject {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewProxyRepo(database)
	return &bulkProject{
		repo:   repo,
		server: newTestServer(&marasi.Proxy{LaunchpadRepo: repo, TrafficRepo: repo, ReportingRepo: repo}, func() {}),
	}
}

// insertPair inserts a request with the given method and returns its id.
func (p *bulkProject) insertPair(t *testing.T, method string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}
	err = p.repo.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: "https", Method: method, Host: "bulk.example.com", Path: "/",
		Raw: []byte(method + " / HTTP/1.1\r\nHost: bulk.example.com\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	return id
}

// drainEvents returns every event published so far.
func drainEvents(subscriber *eventSubscriber) []serviceEvent {
	var events []serviceEvent
	for {
		select {
		case event := <-subscriber.events:
			events = append(events, event)
		default:
			return events
		}
	}
}

func TestTrafficBulkLinkAPI(t *testing.T) {
	t.Run("should link a query selection up to as_of and publish one traffic.changed event", func(t *testing.T) {
		p := setupBulkProject(t)
		launchpadID, err := p.repo.CreateLaunchpad("bulk", "")
		if err != nil {
			t.Fatalf("CreateLaunchpad() error = %v", err)
		}
		p.insertPair(t, "POST")
		asOf := p.insertPair(t, "POST")
		p.insertPair(t, "POST") // after as_of
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		selection := fmt.Sprintf(`{"query":"method = \"POST\"","as_of":%q}`, asOf)
		body := fmt.Sprintf(`{"selection":%s,"launchpad_id":%q}`, selection, launchpadID)
		response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/link", body)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":2}`+"\n")

		events := drainEvents(subscriber)
		want := `{"change":"link","count":2,"selection":` + selection + `}`
		if len(events) != 1 || events[0].name != "traffic.changed" || string(events[0].data) != want {
			t.Fatalf("\nwanted:\none traffic.changed %s\ngot:\n%v", want, eventSummary(events))
		}

		response = requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/link", body)
		assertControlAPIResponse(t, response, http.StatusOK, `{"count":0}`+"\n")
		if events := drainEvents(subscriber); len(events) != 1 || events[0].name != "traffic.changed" {
			t.Fatalf("\nwanted:\none traffic.changed\ngot:\n%v", eventSummary(events))
		}
	})

	t.Run("should link and unlink ids with a finding and a test case", func(t *testing.T) {
		p := setupBulkProject(t)
		finding := &domain.Finding{ID: uuid.New(), Title: "bulk", Severity: "Low"}
		if err := p.repo.SaveFinding(finding); err != nil {
			t.Fatalf("SaveFinding() error = %v", err)
		}
		testCase := &domain.TestCase{ID: uuid.New(), Title: "bulk"}
		if err := p.repo.SaveTestCase(testCase); err != nil {
			t.Fatalf("SaveTestCase() error = %v", err)
		}
		id := p.insertPair(t, "GET")
		selection := fmt.Sprintf(`{"ids":[%q,%q]}`, id, uuid.New())
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)

		for _, field := range []string{fmt.Sprintf(`"finding_id":%q`, finding.ID), fmt.Sprintf(`"test_case_id":%q`, testCase.ID)} {
			body := `{"selection":` + selection + `,` + field + `}`
			response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/link", body)
			assertControlAPIResponse(t, response, http.StatusOK, `{"count":1}`+"\n")
			response = requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/unlink", body)
			assertControlAPIResponse(t, response, http.StatusOK, `{"count":1}`+"\n")
			response = requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/unlink", body)
			assertControlAPIResponse(t, response, http.StatusOK, `{"count":0}`+"\n")
		}
		events := drainEvents(subscriber)
		wantChanges := []string{"link", "unlink", "unlink", "link", "unlink", "unlink"}
		if len(events) != len(wantChanges) {
			t.Fatalf("\nwanted:\n%d events\ngot:\n%v", len(wantChanges), eventSummary(events))
		}
		for i, change := range wantChanges {
			want := fmt.Sprintf(`"change":%q`, change)
			if events[i].name != "traffic.changed" || !strings.Contains(string(events[i].data), want) {
				t.Errorf("event %d = %s %s, want traffic.changed with %s", i, events[i].name, events[i].data, want)
			}
		}
	})

	t.Run("should return 404 for a missing target and publish nothing", func(t *testing.T) {
		p := setupBulkProject(t)
		id := p.insertPair(t, "GET")
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		for _, route := range []string{"/traffic/bulk/link", "/traffic/bulk/unlink"} {
			for _, field := range []string{"launchpad_id", "finding_id", "test_case_id"} {
				body := fmt.Sprintf(`{"selection":{"ids":[%q]},%q:%q}`, id, field, uuid.New())
				response := requestControlAPI(p.server, http.MethodPost, route, body)
				assertControlAPIResponse(t, response, http.StatusNotFound, `{"error":"not_found"}`+"\n")
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
	})

	t.Run("should reject invalid bodies and publish nothing", func(t *testing.T) {
		p := setupBulkProject(t)
		launchpadID, err := p.repo.CreateLaunchpad("bulk", "")
		if err != nil {
			t.Fatalf("CreateLaunchpad() error = %v", err)
		}
		id := p.insertPair(t, "GET")
		lp := fmt.Sprintf(`"launchpad_id":%q`, launchpadID)
		ids := fmt.Sprintf(`{"ids":[%q]}`, id)
		subscriber := p.server.events.subscribe()
		defer p.server.events.unsubscribe(subscriber)
		cases := []struct {
			body string
			want string
		}{
			{`{"selection":` + ids + `}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":` + ids + `,` + lp + fmt.Sprintf(`,"finding_id":%q}`, uuid.New()), `{"error":"invalid_bulk_request"}`},
			{`{` + lp + `}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":` + ids + `,` + lp + `,"extra":1}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":` + ids + `,"launchpad_id":"nope"}`, `{"error":"invalid_bulk_request"}`},
			{`{"selection":{"ids":[]},` + lp + `}`, `{"error":"invalid_selection","message":"invalid selection: ids is empty"}`},
			{fmt.Sprintf(`{"selection":{"query":"method =","as_of":%q},`, id) + lp + `}`, ``},
			{``, `{"error":"invalid_bulk_request"}`},
		}
		for _, c := range cases {
			response := requestControlAPI(p.server, http.MethodPost, "/traffic/bulk/link", c.body)
			if response.Code != http.StatusBadRequest {
				t.Errorf("body %s: status = %d %s, want 400", c.body, response.Code, response.Body)
				continue
			}
			if c.want != "" && response.Body.String() != c.want+"\n" {
				t.Errorf("body %s: response = %s, want %s", c.body, response.Body, c.want)
			}
		}
		if events := drainEvents(subscriber); len(events) != 0 {
			t.Fatalf("\nwanted:\nno events\ngot:\n%v", eventSummary(events))
		}
	})
}

func TestLaunchpadUnlinkAPI(t *testing.T) {
	p := setupBulkProject(t)
	launchpadID, err := p.repo.CreateLaunchpad("unlink", "")
	if err != nil {
		t.Fatalf("CreateLaunchpad() error = %v", err)
	}
	id := p.insertPair(t, "GET")
	if err := p.repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
		t.Fatalf("LinkRequestToLaunchpad() error = %v", err)
	}
	subscriber := p.server.events.subscribe()
	defer p.server.events.unsubscribe(subscriber)
	path := fmt.Sprintf("/launchpad/%s/link/%s", launchpadID, id)

	response := requestControlAPI(p.server, http.MethodDelete, path, "")
	want := fmt.Sprintf(`{"launchpad_id":%q,"request_id":%q}`, launchpadID, id)
	assertControlAPIResponse(t, response, http.StatusOK, want+"\n")
	events := drainEvents(subscriber)
	if len(events) != 1 || events[0].name != "launchpad.unlinked" || string(events[0].data) != want {
		t.Fatalf("\nwanted:\nlaunchpad.unlinked %s\ngot:\n%v", want, eventSummary(events))
	}

	response = requestControlAPI(p.server, http.MethodDelete, path, "")
	assertControlAPIResponse(t, response, http.StatusNotFound, `{"error":"not_found"}`+"\n")
	response = requestControlAPI(p.server, http.MethodDelete, fmt.Sprintf("/launchpad/%s/link/%s", uuid.New(), id), "")
	assertControlAPIResponse(t, response, http.StatusNotFound, `{"error":"not_found"}`+"\n")
	response = requestControlAPI(p.server, http.MethodDelete, fmt.Sprintf("/launchpad/%s/link/junk", launchpadID), "")
	assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"bad_request"}`+"\n")
	if events := drainEvents(subscriber); len(events) != 0 {
		t.Fatalf("\nwanted:\nno further events\ngot:\n%v", eventSummary(events))
	}
}

func eventSummary(events []serviceEvent) []string {
	summary := make([]string, len(events))
	for i, event := range events {
		summary[i] = event.name + " " + string(event.data)
	}
	return summary
}
