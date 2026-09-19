package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
)

func TestExtensionControlRoutes(t *testing.T) {
	t.Run("should list no loaded runtimes as an empty items array", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodGet, "/extension", "")
		assertControlAPIResponse(t, response, http.StatusOK, `{"items":[]}`+"\n")
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should list loaded runtimes by id without lua settings logs or core", func(t *testing.T) {
		updatedAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{
				Data: &domain.Extension{
					ID: uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43"), Name: "workshop", Enabled: true,
					Author: "TenSoon", Description: "Workshop", SourceURL: "marasi-internal",
					LuaContent: "-- workshop", Settings: map[string]any{"theme": "dark"}, UpdatedAt: updatedAt,
				},
				Logs: []extensions.ExtensionLog{{Time: updatedAt, Text: "print"}},
			},
			{
				Data: &domain.Extension{
					ID: uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6"), Name: "compass", Enabled: true,
					Author: "Steris", Description: "Scope Management Extension", SourceURL: "marasi-internal",
					LuaContent: "function processRequest(request) end", Settings: map[string]any{}, UpdatedAt: updatedAt,
				},
			},
			{
				Data: &domain.Extension{
					ID: uuid.MustParse("01937d13-9632-75b1-9e73-c5129b06fa8c"), Name: "checkpoint", Enabled: false,
					Author: "Colms", Description: "Intercept Requests / Responses", SourceURL: "marasi-internal",
					LuaContent: "function interceptRequest(request) end", UpdatedAt: updatedAt,
				},
			},
		}}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/extension", "")
		want := `{"items":[` +
			`{"id":"01937d13-9632-72aa-83b9-c10ea1abbdd6","name":"compass","enabled":true,"author":"Steris","description":"Scope Management Extension","source_url":"marasi-internal","updated_at":"2026-09-19T10:00:00Z"},` +
			`{"id":"01937d13-9632-75b1-9e73-c5129b06fa8c","name":"checkpoint","enabled":false,"author":"Colms","description":"Intercept Requests / Responses","source_url":"marasi-internal","updated_at":"2026-09-19T10:00:00Z"},` +
			`{"id":"01937d13-9632-7f84-add5-14ec2c2c7f43","name":"workshop","enabled":true,"author":"TenSoon","description":"Workshop","source_url":"marasi-internal","updated_at":"2026-09-19T10:00:00Z"}` +
			`]}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
	})

	t.Run("should get one extension with lua and settings", func(t *testing.T) {
		updatedAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
		id := uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43")
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{
				Data: &domain.Extension{
					ID: id, Name: "workshop", Enabled: true, Author: "TenSoon",
					Description: "Workshop", SourceURL: "marasi-internal",
					LuaContent: `print("hi")`, Settings: map[string]any{"theme": "dark"}, UpdatedAt: updatedAt,
				},
				Logs: []extensions.ExtensionLog{{Time: updatedAt, Text: "print"}},
			},
		}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String(), "")
		want := `{"id":"01937d13-9632-7f84-add5-14ec2c2c7f43","name":"workshop","enabled":true,"author":"TenSoon","description":"Workshop","source_url":"marasi-internal","updated_at":"2026-09-19T10:00:00Z","lua_content":"print(\"hi\")","settings":{"theme":"dark"}}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should reject a missing id and a malformed uuid", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{Data: &domain.Extension{ID: id, Name: "compass", Settings: map[string]any{}}},
		}}, func() {})

		missing := requestControlAPI(server, http.MethodGet, "/extension/01937d13-9632-75b1-9e73-c5129b06fa8c", "")
		assertControlAPIResponse(t, missing, http.StatusNotFound, `{"error":"not_found"}`+"\n")

		malformed := requestControlAPI(server, http.MethodGet, "/extension/not-a-uuid", "")
		assertControlAPIResponse(t, malformed, http.StatusBadRequest, `{"error":"bad_request"}`+"\n")
	})

	t.Run("should encode missing settings as an empty object", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
		updatedAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{Data: &domain.Extension{ID: id, Name: "compass", Enabled: true, UpdatedAt: updatedAt}},
		}}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String(), "")
		want := `{"id":"01937d13-9632-72aa-83b9-c10ea1abbdd6","name":"compass","enabled":true,"author":"","description":"","source_url":"","updated_at":"2026-09-19T10:00:00Z","lua_content":"","settings":{}}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
	})
}

func assertNoExtensionEvent(t *testing.T, subscriber *eventSubscriber) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		t.Fatalf("\nwanted:\nno event\ngot:\n%s %s", event.name, event.data)
	default:
	}
}
