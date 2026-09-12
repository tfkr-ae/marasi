package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
