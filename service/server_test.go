package service

import (
	"bytes"
	"context"
	"encoding/base64"
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
	items     map[uuid.UUID]*domain.Launchpad
	order     []uuid.UUID
	listErr   error
	getErr    error
	updateErr error
	members   map[uuid.UUID][]*domain.RequestResponseSummary
	linkErr   error
	linked    []domain.LaunchpadRequest
}

func (s *stubLaunchpadRepository) GetLaunchpads() ([]*domain.Launchpad, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	items := make([]*domain.Launchpad, 0, len(s.order))
	for _, id := range s.order {
		item := *s.items[id]
		items = append(items, &item)
	}
	return items, nil
}

func (s *stubLaunchpadRepository) GetLaunchpad(id uuid.UUID) (*domain.Launchpad, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	item, ok := s.items[id]
	if !ok {
		return nil, domain.ErrLaunchpadNotFound
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
	if s.updateErr != nil {
		return s.updateErr
	}
	item, ok := s.items[id]
	if !ok {
		return domain.ErrLaunchpadNotFound
	}
	if name != nil {
		item.Name = *name
	}
	if description != nil {
		item.Description = *description
	}
	return nil
}

func (s *stubLaunchpadRepository) GetLaunchpadRequests(id uuid.UUID) ([]*domain.RequestResponseSummary, error) {
	items := s.members[id]
	if items == nil {
		return []*domain.RequestResponseSummary{}, nil
	}
	return items, nil
}

func (s *stubLaunchpadRepository) LinkRequestToLaunchpad(requestID, launchpadID uuid.UUID) error {
	if s.linkErr != nil {
		return s.linkErr
	}
	s.linked = append(s.linked, domain.LaunchpadRequest{LaunchpadID: launchpadID, RequestID: requestID})
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

		response = requestListener(t, server, http.MethodPost, "/listener/update", `{"address":"127.0.0.1","port":0}`)
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
		assertListenerError(t, response, http.StatusBadRequest, "invalid_listener_request")
		response = requestListener(t, server, http.MethodPost, "/listener/start", "{}")
		assertListenerError(t, response, http.StatusBadRequest, "invalid_listener_request")
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
			{name: "empty start body", path: "/listener/start", body: ""},
			{name: "empty start object", path: "/listener/start", body: "{}"},
			{name: "start address only", path: "/listener/start", body: `{"address":"127.0.0.1"}`},
			{name: "start port only", path: "/listener/start", body: `{"port":0}`},
			{name: "unknown field", path: "/listener/start", body: `{"host":"127.0.0.1"}`},
			{name: "empty address", path: "/listener/start", body: `{"address":""}`},
			{name: "null address", path: "/listener/start", body: `{"address":null}`},
			{name: "negative port", path: "/listener/start", body: `{"port":-1}`},
			{name: "large port", path: "/listener/start", body: `{"port":65536}`},
			{name: "fractional port", path: "/listener/start", body: `{"port":1.5}`},
			{name: "null port", path: "/listener/start", body: `{"port":null}`},
			{name: "empty update body", path: "/listener/update", body: ""},
			{name: "empty update object", path: "/listener/update", body: "{}"},
			{name: "update address only", path: "/listener/update", body: `{"address":"127.0.0.1"}`},
			{name: "update port only", path: "/listener/update", body: `{"port":0}`},
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

	t.Run("should report serving failure before readiness as unavailable and inactive", func(t *testing.T) {
		proxy := newListenerTestProxy()
		proxy.serve = func(net.Listener) error { return errors.New("serve failed before readiness") }
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, nil, func() {}, "dev", "default", "/work/scratchpad.marasi")

		response := requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		assertListenerError(t, response, http.StatusConflict, "listener_unavailable")
		response = requestListener(t, server, http.MethodGet, "/listener/status", "")
		if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"inactive\",\"proxy_listener\":null}\n" {
			t.Fatalf("\nwanted:\ninactive listener status\ngot:\n%d %s", response.Code, response.Body.String())
		}
		serviceStatus := httptest.NewRecorder()
		server.ServeHTTP(serviceStatus, httptest.NewRequest(http.MethodGet, "/service/status", nil))
		if serviceStatus.Code != http.StatusOK || !strings.Contains(serviceStatus.Body.String(), `"proxy_listener":null`) {
			t.Fatalf("\nwanted:\nservice status with inactive listener\ngot:\n%d %s", serviceStatus.Code, serviceStatus.Body.String())
		}
	})

	t.Run("should map state bind and cleanup failures without exposing details", func(t *testing.T) {
		proxy := newListenerTestProxy()
		var log bytes.Buffer
		lifecycle := newListenerLifecycle(proxy, &log)
		t.Cleanup(func() { lifecycle.Shutdown() })
		server := NewServer(nil, lifecycle, nil, func() {}, "dev", "default", "scratchpad")

		response := requestListener(t, server, http.MethodPost, "/listener/update", `{"address":"127.0.0.1","port":0}`)
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
		response = requestListener(t, server, http.MethodPost, "/listener/start", `{"address":"127.0.0.1","port":0}`)
		assertListenerError(t, response, http.StatusConflict, "listener_already_active")

		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("occupying listener endpoint: %v", err)
		}
		defer occupied.Close()
		port := occupied.Addr().(*net.TCPAddr).Port
		response = requestListener(t, server, http.MethodPost, "/listener/update", fmt.Sprintf(`{"address":"127.0.0.1","port":%d}`, port))
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

	t.Run("should link a traffic pair and publish the mutation", func(t *testing.T) {
		requestID := uuid.MustParse("01938033-298e-73dc-b640-eb321b621154")
		repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id, Name: "Login"}}}
		traffic := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: requestID}}}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo, TrafficRepo: traffic}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestLaunchpad(server, http.MethodPost, "/launchpad/"+id.String()+"/link", `{"request_id":"`+requestID.String()+`"}`)
		want := `{"launchpad_id":"` + id.String() + `","request_id":"` + requestID.String() + `"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
		if len(repo.linked) != 1 || repo.linked[0].LaunchpadID != id || repo.linked[0].RequestID != requestID {
			t.Fatalf("\nwanted:\none link for %s and %s\ngot:\n%v", id, requestID, repo.linked)
		}
		event := <-subscriber.events
		if event.name != "launchpad.linked" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("\nwanted:\nlaunchpad.linked %s\ngot:\n%s %s", strings.TrimSpace(want), event.name, event.data)
		}
	})

	t.Run("should return traffic summaries oldest-first with missing-response values", func(t *testing.T) {
		older := uuid.MustParse("01938030-1b17-7243-b035-e6a9f4645904")
		newer := uuid.MustParse("01938031-1b17-7243-b035-e6a9f4645904")
		requestedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		respondedAt := requestedAt.Add(time.Second)
		repo := &stubLaunchpadRepository{
			items: map[uuid.UUID]*domain.Launchpad{id: {ID: id, Name: "Login", Description: "Try variants"}},
			members: map[uuid.UUID][]*domain.RequestResponseSummary{id: {
				{ID: older, Scheme: "https", Method: "GET", Host: "example.com", Path: "/seed", Status: "N/A", StatusCode: -1, Length: "0", Metadata: map[string]any{"source": "seed", "prettified-request": "omit"}, RequestedAt: requestedAt},
				{ID: newer, Scheme: "https", Method: "POST", Host: "example.com", Path: "/login", Status: "200 OK", StatusCode: 200, ContentType: "text/plain", Length: "2", Metadata: map[string]any{}, RequestedAt: requestedAt, RespondedAt: respondedAt},
			}},
		}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo}, func() {})
		response := requestLaunchpad(server, http.MethodGet, "/launchpad/"+id.String(), "")
		want := `{"id":"` + id.String() + `","name":"Login","description":"Try variants","items":[{"id":"` + older.String() + `","scheme":"https","method":"GET","host":"example.com","path":"/seed","status":"N/A","status_code":-1,"content_type":"","length":"0","metadata":{"source":"seed"},"requested_at":"2026-01-02T03:04:05Z","responded_at":null},{"id":"` + newer.String() + `","scheme":"https","method":"POST","host":"example.com","path":"/login","status":"200 OK","status_code":200,"content_type":"text/plain","length":"2","metadata":{},"requested_at":"2026-01-02T03:04:05Z","responded_at":"2026-01-02T03:04:06Z"}]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
	})

	t.Run("should reject invalid duplicate and missing links", func(t *testing.T) {
		requestID := uuid.MustParse("01938033-298e-73dc-b640-eb321b621154")
		pair := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: requestID}}}
		for _, body := range []string{`{}`, `{"request_id":"bad"}`, `{"request_id":"` + requestID.String() + `","extra":true}`, `{"request_id":"` + requestID.String() + `"} {}`} {
			repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id}}}
			server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo, TrafficRepo: pair}, func() {})
			assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launchpad/"+id.String()+"/link", body), http.StatusBadRequest, "invalid_launchpad_request")
		}

		duplicate := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id}}, linkErr: domain.ErrLaunchpadAlreadyLinked}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: duplicate, TrafficRepo: pair}, func() {})
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launchpad/"+id.String()+"/link", `{"request_id":"`+requestID.String()+`"}`), http.StatusConflict, "already_linked")

		missingPad := newTestServer(&marasi.Proxy{LaunchpadRepo: &stubLaunchpadRepository{}, TrafficRepo: pair}, func() {})
		assertLaunchpadError(t, requestLaunchpad(missingPad, http.MethodPost, "/launchpad/0193802f-f0e7-73d9-a764-06d21e367809/link", `{"request_id":"`+requestID.String()+`"}`), http.StatusNotFound, "not_found")

		missingPair := newTestServer(&marasi.Proxy{LaunchpadRepo: &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id}}}, TrafficRepo: &stubTrafficRepository{}}, func() {})
		assertLaunchpadError(t, requestLaunchpad(missingPair, http.MethodPost, "/launchpad/"+id.String()+"/link", `{"request_id":"`+requestID.String()+`"}`), http.StatusNotFound, "not_found")
	})

	t.Run("should launch a working copy without publishing a launch event", func(t *testing.T) {
		received := make(chan *http.Request, 1)
		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- r
			w.WriteHeader(http.StatusNoContent)
		}))
		defer origin.Close()
		host := strings.TrimPrefix(origin.URL, "https://")
		raw := "POST /login HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 4\r\n\r\nbody"
		repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id, Name: "Login"}}}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo, Client: origin.Client()}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		body := `{"raw":"` + base64.StdEncoding.EncodeToString([]byte(raw)) + `","scheme":"https"}`
		response := requestLaunchpad(server, http.MethodPost, "/launchpad/"+id.String()+"/launch", body)
		if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"launched\"}\n" {
			t.Fatalf("\nwanted:\n200 launched\ngot:\n%d %s", response.Code, response.Body.String())
		}
		request := <-received
		if request.Method != http.MethodPost || request.URL.Path != "/login" || request.Header.Get("x-launchpad-id") != id.String() {
			t.Fatalf("\nwanted:\nPOST /login with launchpad %s\ngot:\n%s %s with launchpad %s", id, request.Method, request.URL.Path, request.Header.Get("x-launchpad-id"))
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno launch event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should reject invalid launches before sending", func(t *testing.T) {
		repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{id: {ID: id}}}
		server := newTestServer(&marasi.Proxy{LaunchpadRepo: repo, Client: http.DefaultClient}, func() {})
		invalidRaw := base64.StdEncoding.EncodeToString([]byte("not an HTTP request"))
		missingHost := base64.StdEncoding.EncodeToString([]byte("GET / HTTP/1.1\r\n\r\n"))
		for _, body := range []string{
			`{"raw":"%%%","scheme":"http"}`,
			`{"raw":"` + invalidRaw + `","scheme":"http"}`,
			`{"raw":"` + missingHost + `","scheme":"http"}`,
			`{"raw":"` + invalidRaw + `","scheme":"ftp"}`,
			`{"raw":"` + invalidRaw + `","scheme":"http","extra":true}`,
		} {
			assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launchpad/"+id.String()+"/launch", body), http.StatusBadRequest, "invalid_launchpad_request")
		}

		missing := newTestServer(&marasi.Proxy{LaunchpadRepo: &stubLaunchpadRepository{}}, func() {})
		validRaw := base64.StdEncoding.EncodeToString([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
		assertLaunchpadError(t, requestLaunchpad(missing, http.MethodPost, "/launchpad/"+id.String()+"/launch", `{"raw":"`+validRaw+`","scheme":"http"}`), http.StatusNotFound, "not_found")
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

	t.Run("should not report repository failures as missing launchpads", func(t *testing.T) {
		failure := errors.New("database unavailable")
		for _, test := range []struct {
			method string
			path   string
			body   string
			repo   *stubLaunchpadRepository
		}{
			{method: http.MethodGet, path: "/launchpad", repo: &stubLaunchpadRepository{listErr: failure}},
			{method: http.MethodGet, path: "/launchpad/" + id.String(), repo: &stubLaunchpadRepository{getErr: failure}},
			{method: http.MethodPost, path: "/launchpad/" + id.String(), body: `{}`, repo: &stubLaunchpadRepository{updateErr: failure}},
		} {
			server := newTestServer(&marasi.Proxy{LaunchpadRepo: test.repo}, func() {})
			assertLaunchpadError(t, requestLaunchpad(server, test.method, test.path, test.body), http.StatusInternalServerError, "internal_server_error")
		}
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

	t.Run("should return the page oldest first with next_cursor when another page exists", func(t *testing.T) {
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
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null},{\"id\":\"01938032-1b17-7243-b035-e6a9f4645904\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:06Z\",\"responded_at\":null}],\"next_cursor\":\"0193802f-f0e7-73d9-a764-06d21e367809\"}\n"
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
