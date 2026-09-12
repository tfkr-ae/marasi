package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type stubTrafficRepository struct {
	domain.TrafficRepository
	row *domain.RequestResponseRow
}

func (s *stubTrafficRepository) GetRequestResponseRow(id uuid.UUID) (*domain.RequestResponseRow, error) {
	if s.row != nil && s.row.Request.ID == id {
		return s.row, nil
	}
	return nil, errors.New("not found")
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
		server := NewServer(nil, func() { stopCalls++ })
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
		server := NewServer(nil, func() { stopCalls++ })

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
		server := NewServer(nil, func() {})
		request := httptest.NewRequest(http.MethodGet, "/service/stop", nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusMethodNotAllowed, response.Code)
		}
	})

	for _, path := range []string{"/service/unknown", "/stop"} {
		t.Run("should not find "+path, func(t *testing.T) {
			server := NewServer(nil, func() {})
			request := httptest.NewRequest(http.MethodPost, path, nil)
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusNotFound, response.Code)
			}
		})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		request := httptest.NewRequest(http.MethodPost, "/traffic/"+id.String(), nil)
		response := httptest.NewRecorder()

		server.ServeHTTP(response, request)

		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusMethodNotAllowed, response.Code)
		}
	})
}
