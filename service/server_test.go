package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

func newTestServer(proxy *marasi.Proxy, stop func()) *Server {
	return NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil, stop, "test-version", "test-instance", "test-project")
}

type statusListener struct {
	status ListenerStatus
}

func (l *statusListener) Status() ListenerStatus { return cloneListenerStatus(l.status) }
func (l *statusListener) Start(context.Context, ListenerSettings) (ListenerStatus, error) {
	panic("unexpected listener start")
}
func (l *statusListener) Stop(context.Context) (ListenerStatus, error) {
	panic("unexpected listener stop")
}
func (l *statusListener) Update(context.Context, ListenerSettings) (ListenerStatus, error) {
	panic("unexpected listener update")
}
func (l *statusListener) Shutdown() error { panic("unexpected listener shutdown") }

type stubTrafficRepository struct {
	domain.TrafficRepository
	row        *domain.RequestResponseRow
	items      []*domain.RequestResponseSummary
	nextCursor *uuid.UUID
}

type stubLaunchpadRepository struct {
	domain.LaunchpadRepository
	items map[uuid.UUID]*domain.Launchpad
	order []uuid.UUID
}

func (s *stubLaunchpadRepository) GetLaunchpads() ([]*domain.Launchpad, error) {
	items := make([]*domain.Launchpad, 0, len(s.order))
	for _, id := range s.order {
		item := *s.items[id]
		items = append(items, &item)
	}
	return items, nil
}

func (s *stubLaunchpadRepository) GetLaunchpad(id uuid.UUID) (*domain.Launchpad, error) {
	item, ok := s.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	copy := *item
	return &copy, nil
}

func (s *stubLaunchpadRepository) CreateLaunchpad(name, description string) (uuid.UUID, error) {
	id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
	if s.items == nil {
		s.items = make(map[uuid.UUID]*domain.Launchpad)
	}
	s.items[id] = &domain.Launchpad{ID: id, Name: name, Description: description}
	s.order = append([]uuid.UUID{id}, s.order...)
	return id, nil
}

func (s *stubLaunchpadRepository) UpdateLaunchpad(id uuid.UUID, name, description *string) error {
	item, ok := s.items[id]
	if !ok {
		return errors.New("not found")
	}
	if name != nil {
		item.Name = *name
	}
	if description != nil {
		item.Description = *description
	}
	return nil
}

func (s *stubTrafficRepository) GetRequestResponseRow(id uuid.UUID) (*domain.RequestResponseRow, error) {
	if s.row != nil && s.row.Request.ID == id {
		return s.row, nil
	}
	return nil, errors.New("not found")
}

func (s *stubTrafficRepository) ListTraffic(cursor *uuid.UUID, limit int, filter domain.TrafficListFilter) ([]*domain.RequestResponseSummary, *uuid.UUID, error) {
	items := s.items
	if items == nil {
		items = []*domain.RequestResponseSummary{}
	}
	matched := make([]*domain.RequestResponseSummary, 0, len(items))
	for _, item := range items {
		if cursor != nil && item.ID.String() >= cursor.String() {
			continue
		}
		if !trafficMatchesFilter(item, filter) {
			continue
		}
		matched = append(matched, item)
	}
	items = matched
	if limit < len(items) {
		items = items[:limit]
		id := items[len(items)-1].ID
		return items, &id, nil
	}
	return items, s.nextCursor, nil
}

func trafficMatchesFilter(item *domain.RequestResponseSummary, filter domain.TrafficListFilter) bool {
	if filter.Host != "" && item.Host != filter.Host {
		return false
	}
	if filter.Method != "" && item.Method != filter.Method {
		return false
	}
	if filter.StatusCode != nil && item.StatusCode != *filter.StatusCode {
		return false
	}
	if filter.PathPrefix != "" && !strings.HasPrefix(item.Path, filter.PathPrefix) {
		return false
	}
	return true
}

type shutdownOrderRecorder struct {
	*httptest.ResponseRecorder
	shutdownRequested func() bool
	respondedFirst    bool
}

func (r *shutdownOrderRecorder) WriteHeader(status int) {
	if !r.shutdownRequested() {
		r.respondedFirst = true
	}
	r.ResponseRecorder.WriteHeader(status)
}

func TestServiceStop(t *testing.T) {
	t.Run("should request shutdown and return accepted", func(t *testing.T) {
		stopCalls := 0
		server := newTestServer(nil, func() { stopCalls++ })
		request := httptest.NewRequest(http.MethodPost, "/service/stop", nil)
		response := &shutdownOrderRecorder{
			ResponseRecorder:  httptest.NewRecorder(),
			shutdownRequested: func() bool { return stopCalls > 0 },
		}

		server.ServeHTTP(response, request)

		if stopCalls != 1 {
			t.Fatalf("\nwanted:\n1 stop call\ngot:\n%d", stopCalls)
		}
		if response.respondedFirst {
			t.Fatal("\nwanted:\nshutdown before response\ngot:\nresponse before shutdown")
		}
		if response.Code != http.StatusAccepted {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusAccepted, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		if got := response.Body.String(); got != "{\"status\":\"shutdown_in_progress\"}\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", "{\"status\":\"shutdown_in_progress\"}\\n", got)
		}
	})

	t.Run("should request shutdown once across repeated requests", func(t *testing.T) {
		stopCalls := 0
		server := newTestServer(nil, func() { stopCalls++ })

		for range 2 {
			request := httptest.NewRequest(http.MethodPost, "/service/stop", nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)

			if response.Code != http.StatusAccepted {
				t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusAccepted, response.Code)
			}
			if got := response.Body.String(); got != "{\"status\":\"shutdown_in_progress\"}\n" {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", "{\"status\":\"shutdown_in_progress\"}\\n", got)
			}
		}

		if stopCalls != 1 {
			t.Fatalf("\nwanted:\n1 stop call\ngot:\n%d", stopCalls)
		}
	})

	t.Run("should reject other methods", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		request := httptest.NewRequest(http.MethodGet, "/service/stop", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusMethodNotAllowed, response.Code)
		}
	})

	for _, path := range []string{"/service/unknown", "/stop"} {
		t.Run("should not find "+path, func(t *testing.T) {
			server := newTestServer(nil, func() {})
			request := httptest.NewRequest(http.MethodPost, path, nil)
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusNotFound, response.Code)
			}
		})
	}
}

func TestServiceStatus(t *testing.T) {
	t.Run("should report the supplied identity and active assigned listener", func(t *testing.T) {
		proxy, err := marasi.New()
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		proxy.Addr = "wrong-address"
		proxy.Port = "1"
		listenerProxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(listenerProxy, nil)
		port := uint16(0)
		address := "127.0.0.1"
		status, err := lifecycle.Start(context.Background(), ListenerSettings{Address: &address, Port: &port})
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		t.Cleanup(func() {
			lifecycle.Shutdown()
		})

		server := NewServer(proxy, lifecycle, nil, func() {}, "13.09.2026", "work", "/work/juice-shop.marasi")
		request := httptest.NewRequest(http.MethodGet, "/service/status", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := fmt.Sprintf("{\"status\":\"running\",\"version\":\"13.09.2026\",\"instance\":\"work\",\"project\":\"/work/juice-shop.marasi\",\"proxy_listener\":%q}\n", *status.ProxyListener)
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}

		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/service/status", nil))
		want = "{\"status\":\"running\",\"version\":\"13.09.2026\",\"instance\":\"work\",\"project\":\"/work/juice-shop.marasi\",\"proxy_listener\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should report an inactive listener", func(t *testing.T) {
		proxy := &marasi.Proxy{}
		server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil, func() {}, "dev", "default", "/work/scratchpad.marasi")
		request := httptest.NewRequest(http.MethodGet, "/service/status", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":\"/work/scratchpad.marasi\",\"proxy_listener\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should return an internal error without an open project", func(t *testing.T) {
		proxy := &marasi.Proxy{}
		server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil, func() {}, "dev", "default", "")
		request := httptest.NewRequest(http.MethodGet, "/service/status", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusInternalServerError {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusInternalServerError, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		if got := response.Body.String(); got != "{\"error\":\"internal_server_error\"}\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", "{\"error\":\"internal_server_error\"}\\n", got)
		}
	})

	t.Run("should reject other methods and leave unknown paths not found", func(t *testing.T) {
		proxy := &marasi.Proxy{}
		server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil, func() {}, "dev", "default", "scratchpad")
		for _, test := range []struct {
			method string
			path   string
			status int
		}{
			{method: http.MethodPost, path: "/service/status", status: http.StatusMethodNotAllowed},
			{method: http.MethodHead, path: "/service/status", status: http.StatusMethodNotAllowed},
			{method: http.MethodGet, path: "/service/unknown", status: http.StatusNotFound},
		} {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("\n%s %s wanted:\n%d\ngot:\n%d", test.method, test.path, test.status, response.Code)
			}
		}
	})
}

func TestListenerControl(t *testing.T) {
	t.Run("should report listener status and enforce route methods", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, nil, func() {}, "dev", "default", "/work/scratchpad.marasi")

		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/listener/status", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		if got := response.Body.String(); got != "{\"status\":\"inactive\",\"proxy_listener\":null}\n" {
			t.Fatalf("\nwanted exact inactive status\ngot:\n%s", got)
		}

		for _, test := range []struct {
			method string
			path   string
			allow  string
		}{
			{method: http.MethodPost, path: "/listener/status", allow: http.MethodGet},
			{method: http.MethodGet, path: "/listener/start", allow: http.MethodPost},
			{method: http.MethodGet, path: "/listener/stop", allow: http.MethodPost},
			{method: http.MethodGet, path: "/listener/update", allow: http.MethodPost},
		} {
			response = httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(test.method, test.path, bytes.NewReader(nil)))
			if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != test.allow {
				t.Fatalf("\n%s %s wanted:\n405 Allow %s\ngot:\n%d Allow %s", test.method, test.path, test.allow, response.Code, response.Header().Get("Allow"))
			}
		}
	})

	t.Run("should start stop and update with exact status responses", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, nil, func() {}, "dev", "default", "/work/scratchpad.marasi")

		response := requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("\nwanted:\n200 application/json\ngot:\n%d %s", response.Code, response.Header().Get("Content-Type"))
		}
		var started ListenerStatus
		if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
			t.Fatalf("decoding start response: %v", err)
		}
		startedAddress := statusAddress(t, started)
		wantStarted := fmt.Sprintf("{\"status\":\"active\",\"proxy_listener\":%q}\n", startedAddress)
		if got := response.Body.String(); got != wantStarted {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantStarted, got)
		}
		serviceStatus := httptest.NewRecorder()
		server.ServeHTTP(serviceStatus, httptest.NewRequest(http.MethodGet, "/service/status", nil))
		wantServiceStatus := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":\"/work/scratchpad.marasi\",\"proxy_listener\":%q}\n", startedAddress)
		if serviceStatus.Code != http.StatusOK || serviceStatus.Body.String() != wantServiceStatus {
			t.Fatalf("\nwanted synchronized service status:\n%s\ngot:\n%d %s", wantServiceStatus, serviceStatus.Code, serviceStatus.Body.String())
		}

		response = requestListener(t, server, http.MethodPost, "/listener/update", `{"port":0}`)
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		var updated ListenerStatus
		if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
			t.Fatalf("decoding update response: %v", err)
		}
		if updated.Status != ListenerActive || statusAddress(t, updated) == startedAddress {
			t.Fatalf("\nwanted:\nactive replacement\ngot:\n%+v", updated)
		}

		response = requestListener(t, server, http.MethodPost, "/listener/stop", "")
		if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"inactive\",\"proxy_listener\":null}\n" {
			t.Fatalf("\nwanted:\n200 inactive status\ngot:\n%d %s", response.Code, response.Body.String())
		}
		serviceStatus = httptest.NewRecorder()
		server.ServeHTTP(serviceStatus, httptest.NewRequest(http.MethodGet, "/service/status", nil))
		wantServiceStatus = "{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":\"/work/scratchpad.marasi\",\"proxy_listener\":null}\n"
		if serviceStatus.Code != http.StatusOK || serviceStatus.Body.String() != wantServiceStatus {
			t.Fatalf("\nwanted synchronized inactive service status:\n%s\ngot:\n%d %s", wantServiceStatus, serviceStatus.Code, serviceStatus.Body.String())
		}
		response = requestListener(t, server, http.MethodPost, "/listener/start", "")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200 for absent start body\ngot:\n%d %s", response.Code, response.Body.String())
		}
		requestListener(t, server, http.MethodPost, "/listener/stop", "")
		response = requestListener(t, server, http.MethodPost, "/listener/start", "{}")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200 for empty start object\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("should reject invalid mutation requests with the stable error", func(t *testing.T) {
		server := NewServer(nil, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil, func() {}, "dev", "default", "scratchpad")
		for _, test := range []struct {
			name string
			path string
			body string
		}{
			{name: "malformed JSON", path: "/listener/start", body: "{"},
			{name: "trailing JSON", path: "/listener/start", body: "{} {}"},
			{name: "null object", path: "/listener/start", body: "null"},
			{name: "unknown field", path: "/listener/start", body: `{"host":"127.0.0.1"}`},
			{name: "empty address", path: "/listener/start", body: `{"address":""}`},
			{name: "null address", path: "/listener/start", body: `{"address":null}`},
			{name: "negative port", path: "/listener/start", body: `{"port":-1}`},
			{name: "large port", path: "/listener/start", body: `{"port":65536}`},
			{name: "fractional port", path: "/listener/start", body: `{"port":1.5}`},
			{name: "null port", path: "/listener/start", body: `{"port":null}`},
			{name: "empty update body", path: "/listener/update", body: ""},
			{name: "empty update object", path: "/listener/update", body: "{}"},
			{name: "stop body", path: "/listener/stop", body: "{}"},
		} {
			t.Run(test.name, func(t *testing.T) {
				response := requestListener(t, server, http.MethodPost, test.path, test.body)
				if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != "{\"error\":\"invalid_listener_request\"}\n" {
					t.Fatalf("\nwanted:\n400 application/json invalid_listener_request\ngot:\n%d %s %s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
				}
			})
		}
	})

	t.Run("should map state bind and cleanup failures without exposing details", func(t *testing.T) {
		proxy := newListenerTestProxy()
		var log bytes.Buffer
		lifecycle := newListenerLifecycle(proxy, &log)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, nil, func() {}, "dev", "default", "scratchpad")

		response := requestListener(t, server, http.MethodPost, "/listener/update", `{"port":0}`)
		assertListenerError(t, response, http.StatusConflict, "listener_inactive")
		occupiedStart, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("occupying listener start endpoint: %v", err)
		}
		defer occupiedStart.Close()
		response = requestListener(t, server, http.MethodPost, "/listener/start", fmt.Sprintf(`{"address":"127.0.0.1","port":%d}`, occupiedStart.Addr().(*net.TCPAddr).Port))
		assertListenerError(t, response, http.StatusConflict, "listener_unavailable")
		response = requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		if response.Code != http.StatusOK {
			t.Fatalf("starting listener: %d %s", response.Code, response.Body.String())
		}
		response = requestListener(t, server, http.MethodPost, "/listener/start", "")
		assertListenerError(t, response, http.StatusConflict, "listener_already_active")

		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("occupying listener endpoint: %v", err)
		}
		defer occupied.Close()
		port := occupied.Addr().(*net.TCPAddr).Port
		response = requestListener(t, server, http.MethodPost, "/listener/update", fmt.Sprintf(`{"port":%d}`, port))
		assertListenerError(t, response, http.StatusConflict, "listener_unavailable")
		if log.Len() == 0 || bytes.Contains(response.Body.Bytes(), log.Bytes()) {
			t.Fatalf("\nwanted:\nbind details only in instance log\ngot response:\n%s\ngot log:\n%s", response.Body.String(), log.String())
		}

		proxy.cleanupErr = errors.New("secret cleanup detail")
		response = requestListener(t, server, http.MethodPost, "/listener/stop", "")
		assertListenerError(t, response, http.StatusInternalServerError, "listener_cleanup_failed")
		if !strings.Contains(log.String(), "secret cleanup detail") || strings.Contains(response.Body.String(), "secret cleanup detail") {
			t.Fatalf("\nwanted:\ncleanup details only in instance log\ngot response:\n%s\ngot log:\n%s", response.Body.String(), log.String())
		}
		response = requestListener(t, server, http.MethodPost, "/listener/stop", "")
		if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"inactive\",\"proxy_listener\":null}\n" {
			t.Fatalf("\nwanted:\nidempotent inactive stop\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})
}

func requestListener(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertListenerError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	want := fmt.Sprintf("{\"error\":%q}\n", code)
	if response.Code != status || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != want {
		t.Fatalf("\nwanted:\n%d application/json %s\ngot:\n%d %s %s", status, want, response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestLaunchpadControlAPI(t *testing.T) {
	id := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")

	t.Run("should create an empty launchpad and publish the mutation", func(t *testing.T) {
		repo := &stubLaunchpadRepository{}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		response := requestLaunchpad(server, http.MethodPost, "/launchpad", `{"name":"Login","description":"Try variants"}`)

		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","name":"Login","description":"Try variants"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "launchpad.created" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("\nwanted:\nlaunchpad.created %s\ngot:\n%s %s", strings.TrimSpace(want), event.name, event.data)
		}
	})

	t.Run("should reject invalid create bodies", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: &stubLaunchpadRepository{}}, func() {})
		for _, body := range []string{`{}`, `{"name":""}`, `{"name":"x","extra":true}`, `{"name":"x"} {}`} {
			response := requestLaunchpad(server, http.MethodPost, "/launchpad", body)
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_launchpad_request\"}\n" {
				t.Fatalf("\nbody %s wanted:\n400 invalid_launchpad_request\ngot:\n%d %s", body, response.Code, response.Body.String())
			}
		}
	})

	t.Run("should list launchpads and keep an empty list stable", func(t *testing.T) {
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{
			id:    {ID: id, Name: "New", Description: "newest"},
			older: {ID: older, Name: "Old", Description: "oldest"},
		}, order: []uuid.UUID{id, older}}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo}, func() {})
		response := requestLaunchpad(server, http.MethodGet, "/launchpad", "")
		want := `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","name":"New","description":"newest"},{"id":"0193802f-f0e7-73d9-a764-06d21e367809","name":"Old","description":"oldest"}]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}

		empty := newTestServer(&marasi.Proxy{LaunchpadRepo: &stubLaunchpadRepository{}}, func() {})
		response = requestLaunchpad(empty, http.MethodGet, "/launchpad", "")
		if response.Code != http.StatusOK || response.Body.String() != "{\"items\":[]}\n" {
			t.Fatalf("\nwanted:\n200 {\"items\":[]}\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("should get an empty launchpad and reject bad or missing ids", func(t *testing.T) {
		repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id, Name: "Login", Description: ""}}}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo}, func() {})
		response := requestLaunchpad(server, http.MethodGet, "/launchpad/"+id.String(), "")
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","name":"Login","description":"","items":[]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodGet, "/launchpad/not-a-uuid", ""), http.StatusBadRequest, "bad_request")
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodGet, "/launchpad/0193802f-f0e7-73d9-a764-06d21e367809", ""), http.StatusNotFound, "not_found")
	})

	t.Run("should update only supplied fields and publish the result", func(t *testing.T) {
		repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id, Name: "Login", Description: "old"}}}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		response := requestLaunchpad(server, http.MethodPost, "/launchpad/"+id.String(), `{"description":""}`)
		want := `{"id":"01938032-1b17-7243-b035-e6a9f4645904","name":"Login","description":""}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "launchpad.updated" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("\nwanted:\nlaunchpad.updated %s\ngot:\n%s %s", strings.TrimSpace(want), event.name, event.data)
		}

		assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launchpad/"+id.String(), `{"name":""}`), http.StatusBadRequest, "invalid_launchpad_request")
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launchpad/not-a-uuid", `{}`), http.StatusBadRequest, "bad_request")
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launchpad/0193802f-f0e7-73d9-a764-06d21e367809", `{}`), http.StatusNotFound, "not_found")
	})
}

func requestLaunchpad(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertLaunchpadError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	want := fmt.Sprintf("{\"error\":%q}\n", code)
	if response.Code != status || response.Body.String() != want {
		t.Fatalf("\nwanted:\n%d %s\ngot:\n%d %s", status, want, response.Code, response.Body.String())
	}
}

func TestTrafficGet(t *testing.T) {
	t.Run("should return a stored pair as nested json", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			row: &domain.RequestResponseRow{
				Request: domain.ProxyRequest{
					ID:          id,
					Scheme:      "https",
					Method:      "GET",
					Host:        "example.com",
					Path:        "/a",
					Raw:         []byte{0xff, 0x00, 0x01},
					RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				},
				Response: domain.ProxyResponse{
					ID:          id,
					Status:      "200 OK",
					StatusCode:  200,
					ContentType: "application/json",
					Length:      "12",
					Raw:         []byte{0x80, 0x81, 0x82},
					RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
				},
				Metadata: map[string]any{"foo": "bar"},
				Note:     "a note",
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic/"+id.String(), nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"note\":\"a note\",\"metadata\":{\"foo\":\"bar\"},\"request\":{\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"raw\":\"/wAB\",\"requested_at\":\"2026-01-02T03:04:05Z\"},\"response\":{\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"application/json\",\"length\":\"12\",\"raw\":\"gIGC\",\"responded_at\":\"2026-01-02T03:04:06Z\"}}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should omit prettified metadata keys", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			row: &domain.RequestResponseRow{
				Request: domain.ProxyRequest{
					ID:          id,
					Scheme:      "https",
					Method:      "GET",
					Host:        "example.com",
					Path:        "/a",
					Raw:         []byte{0xff, 0x00, 0x01},
					RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				},
				Response: domain.ProxyResponse{
					ID:          id,
					Status:      "200 OK",
					StatusCode:  200,
					ContentType: "application/json",
					Length:      "12",
					Raw:         []byte{0x80, 0x81, 0x82},
					RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
				},
				Metadata: map[string]any{
					"foo":                 "bar",
					"prettified-request":  "pretty-req",
					"prettified-response": "pretty-res",
				},
				Note: "a note",
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic/"+id.String(), nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"note\":\"a note\",\"metadata\":{\"foo\":\"bar\"},\"request\":{\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"raw\":\"/wAB\",\"requested_at\":\"2026-01-02T03:04:05Z\"},\"response\":{\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"application/json\",\"length\":\"12\",\"raw\":\"gIGC\",\"responded_at\":\"2026-01-02T03:04:06Z\"}}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should not find a well-formed unknown id", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic/01938032-1b17-7243-b035-e6a9f4645904", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusNotFound {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusNotFound, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		if got := response.Body.String(); got != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", "{\"error\":\"not_found\"}\\n", got)
		}
	})

	t.Run("should reject an unparseable id", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic/not-a-uuid", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusBadRequest, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		if got := response.Body.String(); got != "{\"error\":\"bad_request\"}\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", "{\"error\":\"bad_request\"}\\n", got)
		}
	})

	t.Run("should reject other methods", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		request := httptest.NewRequest(http.MethodPost, "/traffic/"+id.String(), nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusMethodNotAllowed, response.Code)
		}
	})
}

func TestTrafficList(t *testing.T) {
	t.Run("should return a page of summaries", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{{
				ID:          id,
				Scheme:      "https",
				Method:      "GET",
				Host:        "example.com",
				Path:        "/a",
				Status:      "200 OK",
				StatusCode:  200,
				ContentType: "application/json",
				Length:      "12",
				Metadata:    map[string]any{"foo": "bar"},
				RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
			}},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"application/json\",\"length\":\"12\",\"metadata\":{\"foo\":\"bar\"},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":\"2026-01-02T03:04:06Z\"}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should omit prettified metadata keys and raw bytes", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{{
				ID:          id,
				Scheme:      "https",
				Method:      "GET",
				Host:        "example.com",
				Path:        "/a",
				Status:      "200 OK",
				StatusCode:  200,
				ContentType: "application/json",
				Length:      "12",
				Metadata: map[string]any{
					"foo":                 "bar",
					"prettified-request":  "pretty-req",
					"prettified-response": "pretty-res",
				},
				RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
			}},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"application/json\",\"length\":\"12\",\"metadata\":{\"foo\":\"bar\"},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":\"2026-01-02T03:04:06Z\"}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should include in-flight rows with status code -1 and null responded_at", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{{
				ID:          id,
				Scheme:      "https",
				Method:      "GET",
				Host:        "example.com",
				Path:        "/a",
				Status:      "N/A",
				StatusCode:  -1,
				Length:      "0",
				Metadata:    map[string]any{},
				RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			}},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"status\":\"N/A\",\"status_code\":-1,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should return next_cursor when another page exists", func(t *testing.T) {
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: newer, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: older, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
			nextCursor: &older,
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?limit=2", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"01938032-1b17-7243-b035-e6a9f4645904\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:06Z\",\"responded_at\":null},{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":\"0193802f-f0e7-73d9-a764-06d21e367809\"}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should default limit to 200", func(t *testing.T) {
		items := make([]*domain.RequestResponseSummary, 201)
		for i := range items {
			id, err := uuid.NewV7()
			if err != nil {
				t.Fatalf("creating uuid: %v", err)
			}
			items[i] = &domain.RequestResponseSummary{ID: id, Length: "0"}
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{items: items}}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		var body struct {
			Items []struct {
				ID uuid.UUID `json:"id"`
			} `json:"items"`
			NextCursor *uuid.UUID `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if len(body.Items) != 200 {
			t.Fatalf("\nwanted:\n200\ngot:\n%d", len(body.Items))
		}
		if body.NextCursor == nil || *body.NextCursor != items[199].ID {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", items[199].ID, body.NextCursor)
		}
	})

	t.Run("should reject a limit below 1, above 500, or a non-integer", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		for _, raw := range []string{"0", "501", "abc", "1.5"} {
			request := httptest.NewRequest(http.MethodGet, "/traffic?limit="+raw, nil)
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("\nlimit %s wanted:\n%d\ngot:\n%d", raw, http.StatusBadRequest, response.Code)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("\nlimit %s wanted:\napplication/json\ngot:\n%s", raw, got)
			}
			if got := response.Body.String(); got != "{\"error\":\"bad_request\"}\n" {
				t.Fatalf("\nlimit %s wanted:\n%s\ngot:\n%s", raw, "{\"error\":\"bad_request\"}\\n", got)
			}
		}
	})

	t.Run("should return the next older page for a cursor", func(t *testing.T) {
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: newer, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: older, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?cursor="+newer.String(), nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should treat a zero uuid cursor as older than that id", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: id, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?cursor="+uuid.Nil.String(), nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should reject an unparseable cursor", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?cursor=not-a-uuid", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusBadRequest, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		if got := response.Body.String(); got != "{\"error\":\"bad_request\"}\n" {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", "{\"error\":\"bad_request\"}\\n", got)
		}
	})

	t.Run("should reject other methods", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		request := httptest.NewRequest(http.MethodPost, "/traffic", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusMethodNotAllowed, response.Code)
		}
	})

	t.Run("should filter by exact host", func(t *testing.T) {
		matching := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		other := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: other, Host: "other.com", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: matching, Host: "example.com", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?host=example.com", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"example.com\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should filter by exact method", func(t *testing.T) {
		matching := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		other := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: other, Method: "GET", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: matching, Method: "POST", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?method=POST", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"POST\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should filter by exact status code", func(t *testing.T) {
		matching := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		other := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: other, StatusCode: 404, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: matching, StatusCode: 200, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?status_code=200", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":200,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should filter by path prefix including the query string", func(t *testing.T) {
		matching := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		other := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: other, Path: "/api/v2/users", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: matching, Path: "/api/users?id=1", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?path=/api/users", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"/api/users?id=1\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should AND host, method, status code, and path prefix", func(t *testing.T) {
		matching := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		other := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: other, Host: "example.com", Method: "GET", Path: "/api/users", StatusCode: 200, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: matching, Host: "example.com", Method: "POST", Path: "/api/users", StatusCode: 200, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?host=example.com&method=POST&status_code=200&path=/api", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"POST\",\"host\":\"example.com\",\"path\":\"/api/users\",\"status\":\"\",\"status_code\":200,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should ignore unknown query parameters", func(t *testing.T) {
		matching := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		other := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: other, Host: "other.com", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: matching, Host: "example.com", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?host=example.com&q=secret", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("\nwanted:\napplication/json\ngot:\n%s", got)
		}
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"example.com\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should reject a non-integer status_code", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		for _, raw := range []string{"abc", "1.5"} {
			request := httptest.NewRequest(http.MethodGet, "/traffic?status_code="+raw, nil)
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("\nstatus_code %s wanted:\n%d\ngot:\n%d", raw, http.StatusBadRequest, response.Code)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("\nstatus_code %s wanted:\napplication/json\ngot:\n%s", raw, got)
			}
			if got := response.Body.String(); got != "{\"error\":\"bad_request\"}\n" {
				t.Fatalf("\nstatus_code %s wanted:\n%s\ngot:\n%s", raw, "{\"error\":\"bad_request\"}\\n", got)
			}
		}
	})

	t.Run("should page a filtered list with a cursor", func(t *testing.T) {
		newest := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		middle := uuid.MustParse("01938031-0a00-7000-8000-000000000000")
		oldest := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: newest, Host: "example.com", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC)},
				{ID: middle, Host: "other.com", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
				{ID: oldest, Host: "example.com", Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		request := httptest.NewRequest(http.MethodGet, "/traffic?host=example.com&limit=1", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
		}
		want := "{\"items\":[{\"id\":\"01938032-1b17-7243-b035-e6a9f4645904\",\"scheme\":\"\",\"method\":\"\",\"host\":\"example.com\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:07Z\",\"responded_at\":null}],\"next_cursor\":\"01938032-1b17-7243-b035-e6a9f4645904\"}\n"
		if got := response.Body.String(); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}

		olderRequest := httptest.NewRequest(http.MethodGet, "/traffic?host=example.com&limit=1&cursor="+newest.String(), nil)
		olderResponse := httptest.NewRecorder()
		server.ServeHTTP(olderResponse, olderRequest)

		if olderResponse.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, olderResponse.Code)
		}
		wantOlder := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"example.com\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null}],\"next_cursor\":null}\n"
		if got := olderResponse.Body.String(); got != wantOlder {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantOlder, got)
		}
	})
}
