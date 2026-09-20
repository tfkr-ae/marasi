package service

import (
	"net/http"
	"strings"
	"testing"

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
