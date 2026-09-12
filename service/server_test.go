package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type stubTrafficRepository struct {
	domain.TrafficRepository
	row        *domain.RequestResponseRow
	items      []*domain.RequestResponseSummary
	nextCursor *uuid.UUID
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{items: items}}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
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
		server := NewServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
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
