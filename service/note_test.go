package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

func TestNoteControlAPI(t *testing.T) {
	id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")

	t.Run("should set a note and publish the mutation", func(t *testing.T) {
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: id}}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPut, "/notes/"+id.String(), `{"note":"needs review"}`)
		want := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"needs review"}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		if repo.notes[id] != "needs review" {
			t.Fatalf("\nwanted:\nstored note %q\ngot:\n%q", "needs review", repo.notes[id])
		}
		event := <-subscriber.events
		if event.name != "note.updated" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("\nwanted:\nnote.updated %s\ngot:\n%s %s", strings.TrimSpace(want), event.name, event.data)
		}
	})

	t.Run("should overwrite an existing note", func(t *testing.T) {
		repo := &stubTrafficRepository{
			row:   &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: id}},
			notes: map[uuid.UUID]string{id: "old"},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		response := requestControlAPI(server, http.MethodPut, "/notes/"+id.String(), `{"note":"new"}`)
		want := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809","note":"new"}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		if repo.notes[id] != "new" {
			t.Fatalf("\nwanted:\nstored note %q\ngot:\n%q", "new", repo.notes[id])
		}
	})

	t.Run("should reject invalid note bodies", func(t *testing.T) {
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: id}}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		for _, body := range []string{
			`{"note":""}`,
			`{}`,
			`{"note":"x","extra":true}`,
			`{"note":"x"} {}`,
			`{"note":null}`,
			`[]`,
			`"note"`,
			``,
		} {
			response := requestControlAPI(server, http.MethodPut, "/notes/"+id.String(), body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, "{\"error\":\"invalid_note_request\"}\n")
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should reject a missing pair a junk id and a nil repository", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPut, "/notes/"+id.String(), `{"note":"x"}`), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPut, "/notes/not-a-uuid", `{"note":"x"}`), http.StatusBadRequest, "{\"error\":\"bad_request\"}\n")
		nilRepo := newTestServer(&marasi.Proxy{}, func() {})
		assertControlAPIResponse(t, requestControlAPI(nilRepo, http.MethodPut, "/notes/"+id.String(), `{"note":"x"}`), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should delete a note and publish the mutation", func(t *testing.T) {
		repo := &stubTrafficRepository{
			row:   &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: id}},
			notes: map[uuid.UUID]string{id: "needs review"},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodDelete, "/notes/"+id.String(), "")
		want := `{"id":"0193802f-f0e7-73d9-a764-06d21e367809"}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		if _, ok := repo.notes[id]; ok {
			t.Fatalf("\nwanted:\nnote row removed\ngot:\n%q", repo.notes[id])
		}
		event := <-subscriber.events
		if event.name != "note.deleted" || string(event.data) != strings.TrimSpace(want) {
			t.Fatalf("\nwanted:\nnote.deleted %s\ngot:\n%s %s", strings.TrimSpace(want), event.name, event.data)
		}
	})

	t.Run("should return not found when deleting a missing note or pair", func(t *testing.T) {
		noNote := &stubTrafficRepository{row: &domain.RequestResponseRow{Request: domain.ProxyRequest{ID: id}}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: noNote}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/notes/"+id.String(), ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")

		missingPair := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		assertControlAPIResponse(t, requestControlAPI(missingPair, http.MethodDelete, "/notes/"+id.String(), ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodDelete, "/notes/"+id.String(), ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/notes/not-a-uuid", ""), http.StatusBadRequest, "{\"error\":\"bad_request\"}\n")
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})
}

func TestNotesList(t *testing.T) {
	t.Run("should return an empty page", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/notes", ""), http.StatusOK, "{\"items\":[],\"next_cursor\":null}\n")
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should return summaries plus note newest first", func(t *testing.T) {
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{
					ID:          newer,
					Scheme:      "https",
					Method:      "POST",
					Host:        "example.com",
					Path:        "/login",
					Status:      "401 Unauthorized",
					StatusCode:  401,
					ContentType: "text/plain",
					Length:      "45",
					Metadata:    map[string]any{"foo": "bar"},
					RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
					RespondedAt: time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC),
					Note:        "newer note",
				},
				{
					ID:          older,
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
					Note:        "older note",
				},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		want := "{\"items\":[{\"id\":\"01938032-1b17-7243-b035-e6a9f4645904\",\"scheme\":\"https\",\"method\":\"POST\",\"host\":\"example.com\",\"path\":\"/login\",\"status\":\"401 Unauthorized\",\"status_code\":401,\"content_type\":\"text/plain\",\"length\":\"45\",\"metadata\":{\"foo\":\"bar\"},\"requested_at\":\"2026-01-02T03:04:06Z\",\"responded_at\":\"2026-01-02T03:04:07Z\",\"note\":\"newer note\"},{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"application/json\",\"length\":\"12\",\"metadata\":{\"foo\":\"bar\"},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":\"2026-01-02T03:04:06Z\",\"note\":\"older note\"}],\"next_cursor\":null}\n"
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/notes", ""), http.StatusOK, want)
	})

	t.Run("should include in-flight rows and strip prettified metadata", func(t *testing.T) {
		withNote := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		empty := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{
					ID:          withNote,
					Scheme:      "https",
					Method:      "GET",
					Host:        "example.com",
					Path:        "/a",
					Status:      "N/A",
					StatusCode:  -1,
					Length:      "0",
					Metadata:    map[string]any{"foo": "bar", "prettified-request": "pretty-req", "prettified-response": "pretty-res"},
					RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
					Note:        "needs review",
				},
				{ID: empty, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"status\":\"N/A\",\"status_code\":-1,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{\"foo\":\"bar\"},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null,\"note\":\"needs review\"}],\"next_cursor\":null}\n"
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/notes", ""), http.StatusOK, want)
	})

	t.Run("should return next_cursor when another older page exists", func(t *testing.T) {
		newer := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		older := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: newer, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), Note: "newer"},
				{ID: older, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Note: "older"},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		want := "{\"items\":[{\"id\":\"01938032-1b17-7243-b035-e6a9f4645904\",\"scheme\":\"\",\"method\":\"\",\"host\":\"\",\"path\":\"\",\"status\":\"\",\"status_code\":0,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:06Z\",\"responded_at\":null,\"note\":\"newer\"}],\"next_cursor\":\"01938032-1b17-7243-b035-e6a9f4645904\"}\n"
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/notes?limit=1", ""), http.StatusOK, want)
	})

	t.Run("should default limit to 200", func(t *testing.T) {
		items := make([]*domain.RequestResponseSummary, 201)
		for i := range items {
			id, err := uuid.NewV7()
			if err != nil {
				t.Fatalf("creating uuid: %v", err)
			}
			items[i] = &domain.RequestResponseSummary{ID: id, Length: "0", Note: "n"}
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{items: items}}, func() {})
		response := requestControlAPI(server, http.MethodGet, "/notes", "")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusOK, response.Code)
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

	t.Run("should reject a junk limit or cursor", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		for _, path := range []string{"/notes?limit=0", "/notes?limit=501", "/notes?limit=abc", "/notes?cursor=not-a-uuid"} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, path, ""), http.StatusBadRequest, "{\"error\":\"bad_request\"}\n")
		}
	})

	t.Run("should ignore unknown query parameters including traffic filters", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{
			items: []*domain.RequestResponseSummary{
				{ID: id, Host: "other.com", Method: "GET", Path: "/a", StatusCode: 200, Length: "0", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Note: "keep"},
			},
		}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		want := "{\"items\":[{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"scheme\":\"\",\"method\":\"GET\",\"host\":\"other.com\",\"path\":\"/a\",\"status\":\"\",\"status_code\":200,\"content_type\":\"\",\"length\":\"0\",\"metadata\":{},\"requested_at\":\"2026-01-02T03:04:05Z\",\"responded_at\":null,\"note\":\"keep\"}],\"next_cursor\":null}\n"
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/notes?host=example.com&method=POST&status_code=abc&path=/nope", ""), http.StatusOK, want)
	})

	t.Run("should return not found for a nil repository", func(t *testing.T) {
		assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodGet, "/notes", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	})
}
