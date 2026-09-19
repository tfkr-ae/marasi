package service

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
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

	t.Run("should return print logs oldest first from the in-memory buffer", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43")
		older := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
		newer := time.Date(2026, 9, 19, 10, 0, 1, 0, time.UTC)
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{
				Data: &domain.Extension{ID: id, Name: "workshop", Settings: map[string]any{}},
				Logs: []extensions.ExtensionLog{
					{Time: older, Text: "first"},
					{Time: newer, Text: "second"},
				},
			},
		}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String()+"/logs", "")
		want := `{"items":[{"time":"2026-09-19T10:00:00Z","text":"first"},{"time":"2026-09-19T10:00:01Z","text":"second"}]}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should return empty print logs as an empty items array", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{Data: &domain.Extension{ID: id, Name: "compass", Settings: map[string]any{}}},
		}}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String()+"/logs", "")
		assertControlAPIResponse(t, response, http.StatusOK, `{"items":[]}`+"\n")
	})

	t.Run("should reject missing and malformed ids when reading logs", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{Data: &domain.Extension{ID: id, Name: "compass", Settings: map[string]any{}}},
		}}, func() {})

		missing := requestControlAPI(server, http.MethodGet, "/extension/01937d13-9632-75b1-9e73-c5129b06fa8c/logs", "")
		assertControlAPIResponse(t, missing, http.StatusNotFound, `{"error":"not_found"}`+"\n")

		malformed := requestControlAPI(server, http.MethodGet, "/extension/not-a-uuid/logs", "")
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

	t.Run("should persist lua, eval it, bump updated_at, and publish the list item", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `called = 0
function startup()
  called = called + 1
end`)
		before := runtime.Data.UpdatedAt
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		lua := "print(1)"
		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String(), `{"lua_content":"print(1)"}`)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || !strings.HasSuffix(response.Body.String(), "\n") {
			t.Fatalf("\nwanted:\n200 application/json with trailing newline\ngot:\n%d %s %s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}

		var detail extensionDetail
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decoding update response: %v", err)
		}
		if detail.ID != runtime.Data.ID || detail.Name != "workshop" || detail.LuaContent != lua || !detail.UpdatedAt.After(before) {
			t.Fatalf("\nwanted:\nworkshop lua %q with bumped updated_at\ngot:\n%+v", lua, detail)
		}

		stored, err := repo.GetExtensionByUUID(runtime.Data.ID)
		if err != nil {
			t.Fatalf("getting persisted workshop: %v", err)
		}
		if stored.LuaContent != lua || runtime.Data.LuaContent != lua {
			t.Fatalf("\nwanted:\npersisted and loaded lua %q\ngot:\nrepo %q runtime %q", lua, stored.LuaContent, runtime.Data.LuaContent)
		}
		if got := runtime.GetGlobal("called"); got != float64(1) {
			t.Fatalf("\nwanted:\nstartup left called at 1\ngot:\n%v", got)
		}

		event := <-subscriber.events
		var payload map[string]json.RawMessage
		if event.name != "extension.updated" {
			t.Fatalf("\nwanted:\nextension.updated\ngot:\n%s %s", event.name, event.data)
		}
		if err := json.Unmarshal(event.data, &payload); err != nil {
			t.Fatalf("decoding event: %v", err)
		}
		if _, ok := payload["lua_content"]; ok {
			t.Fatalf("\nwanted:\nlist-item event without lua_content\ngot:\n%s", event.data)
		}
		if string(payload["id"]) != `"`+runtime.Data.ID.String()+`"` || string(payload["name"]) != `"workshop"` {
			t.Fatalf("\nwanted:\nlist-item for workshop\ngot:\n%s", event.data)
		}

		get := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String(), "")
		if get.Body.String() != response.Body.String() {
			t.Fatalf("\nwanted:\nGET to match update body\ngot:\n%s", get.Body.String())
		}
	})

	t.Run("should keep persisted lua and publish when eval fails", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		oldLogs := append([]extensions.ExtensionLog(nil), runtime.Logs...)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		lua := "this is not lua"
		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String(), `{"lua_content":"this is not lua"}`)
		assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"lua_error"}`+"\n")

		stored, err := repo.GetExtensionByUUID(runtime.Data.ID)
		if err != nil {
			t.Fatalf("getting persisted workshop: %v", err)
		}
		if stored.LuaContent != lua || runtime.Data.LuaContent != lua {
			t.Fatalf("\nwanted:\npersisted lua %q\ngot:\nrepo %q runtime %q", lua, stored.LuaContent, runtime.Data.LuaContent)
		}
		if len(runtime.Logs) != len(oldLogs) {
			t.Fatalf("\nwanted:\nprint logs to survive failed eval\ngot:\n%d logs", len(runtime.Logs))
		}
		event := <-subscriber.events
		if event.name != "extension.updated" {
			t.Fatalf("\nwanted:\nextension.updated after persist\ngot:\n%s %s", event.name, event.data)
		}
	})

	t.Run("should allow empty lua and keep print logs", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		oldText := runtime.Logs[0].Text
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})

		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String(), `{"lua_content":""}`)
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		var detail extensionDetail
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decoding update response: %v", err)
		}
		if detail.LuaContent != "" {
			t.Fatalf("\nwanted:\nempty lua_content\ngot:\n%q", detail.LuaContent)
		}
		stored, err := repo.GetExtensionByUUID(runtime.Data.ID)
		if err != nil || stored.LuaContent != "" {
			t.Fatalf("\nwanted:\nempty stored lua\ngot:\n%q %v", stored.LuaContent, err)
		}
		logs := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String()+"/logs", "")
		if !strings.Contains(logs.Body.String(), oldText) {
			t.Fatalf("\nwanted:\nprint logs to keep %q\ngot:\n%s", oldText, logs.Body.String())
		}
	})

	t.Run("should get settings as an envelope", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43")
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{Data: &domain.Extension{ID: id, Name: "workshop", Settings: map[string]any{"theme": "dark"}}},
		}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String()+"/settings", "")
		assertControlAPIResponse(t, response, http.StatusOK, `{"settings":{"theme":"dark"}}`+"\n")
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should encode missing settings as an empty object envelope", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{Data: &domain.Extension{ID: id, Name: "compass"}},
		}}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String()+"/settings", "")
		assertControlAPIResponse(t, response, http.StatusOK, `{"settings":{}}`+"\n")
	})

	t.Run("should replace settings, return the stored envelope, and publish", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/settings", `{"settings":{"count":1,"theme":"dark"}}`)
		want := `{"settings":{"count":1,"theme":"dark"}}` + "\n"
		assertControlAPIResponse(t, response, http.StatusOK, want)

		stored, err := repo.GetExtensionSettingsByUUID(runtime.Data.ID)
		if err != nil {
			t.Fatalf("getting stored settings: %v", err)
		}
		if !reflect.DeepEqual(stored, map[string]any{"count": float64(1), "theme": "dark"}) {
			t.Fatalf("\nwanted:\nstored theme dark count 1\ngot:\n%v", stored)
		}

		get := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String()+"/settings", "")
		if get.Body.String() != response.Body.String() {
			t.Fatalf("\nwanted:\nGET to match replace body\ngot:\n%s", get.Body.String())
		}

		event := <-subscriber.events
		if event.name != "extension.settings.updated" || string(event.data) != `{"settings":{"count":1,"theme":"dark"}}` {
			t.Fatalf("\nwanted:\nextension.settings.updated %s\ngot:\n%s %s", want, event.name, event.data)
		}
	})

	t.Run("should clear settings with an empty object", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		if err := repo.SetExtensionSettingsByUUID(runtime.Data.ID, map[string]any{"theme": "dark"}); err != nil {
			t.Fatalf("seeding settings: %v", err)
		}
		runtime.Data.Settings = map[string]any{"theme": "dark"}
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})

		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/settings", `{"settings":{}}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"settings":{}}`+"\n")

		stored, err := repo.GetExtensionSettingsByUUID(runtime.Data.ID)
		if err != nil || len(stored) != 0 {
			t.Fatalf("\nwanted:\nempty stored settings\ngot:\n%v %v", stored, err)
		}
	})

	t.Run("should let lua settings get read the write without a reload", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})

		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/settings", `{"settings":{"theme":"dark"}}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"settings":{"theme":"dark"}}`+"\n")

		if err := runtime.ExecuteLua(`got = marasi.settings:get()`); err != nil {
			t.Fatalf("running settings get: %v", err)
		}
		if !reflect.DeepEqual(runtime.GetGlobal("got"), map[string]any{"theme": "dark"}) {
			t.Fatalf("\nwanted:\nlua get {theme:dark}\ngot:\n%#v", runtime.GetGlobal("got"))
		}
	})

	t.Run("should reject a missing id and invalid settings bodies without publishing", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		missing := requestControlAPI(server, http.MethodPost, "/extension/01937d13-9632-75b1-9e73-c5129b06fa8c/settings", `{"settings":{"theme":"dark"}}`)
		assertControlAPIResponse(t, missing, http.StatusNotFound, `{"error":"not_found"}`+"\n")

		malformed := requestControlAPI(server, http.MethodPost, "/extension/not-a-uuid/settings", `{"settings":{"theme":"dark"}}`)
		assertControlAPIResponse(t, malformed, http.StatusBadRequest, `{"error":"bad_request"}`+"\n")

		for _, body := range []string{
			`{}`,
			`{"settings":null}`,
			`{"settings":[]}`,
			`{"settings":"dark"}`,
			`{"settings":1}`,
			`{"settings":{"theme":"dark"},"extra":true}`,
			`{"settings":{"theme":"dark"}}{"extra":true}`,
			`[]`,
			``,
		} {
			response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/settings", body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"invalid_extension_request"}`+"\n")
		}
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should reject missing and malformed ids when reading settings", func(t *testing.T) {
		id := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{
			{Data: &domain.Extension{ID: id, Name: "compass", Settings: map[string]any{}}},
		}}, func() {})

		missing := requestControlAPI(server, http.MethodGet, "/extension/01937d13-9632-75b1-9e73-c5129b06fa8c/settings", "")
		assertControlAPIResponse(t, missing, http.StatusNotFound, `{"error":"not_found"}`+"\n")

		malformed := requestControlAPI(server, http.MethodGet, "/extension/not-a-uuid/settings", "")
		assertControlAPIResponse(t, malformed, http.StatusBadRequest, `{"error":"bad_request"}`+"\n")
	})

	t.Run("should reject a missing id and invalid update bodies without publishing", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		missing := requestControlAPI(server, http.MethodPost, "/extension/01937d13-9632-75b1-9e73-c5129b06fa8c", `{"lua_content":"print(1)"}`)
		assertControlAPIResponse(t, missing, http.StatusNotFound, `{"error":"not_found"}`+"\n")

		malformed := requestControlAPI(server, http.MethodPost, "/extension/not-a-uuid", `{"lua_content":"print(1)"}`)
		assertControlAPIResponse(t, malformed, http.StatusBadRequest, `{"error":"bad_request"}`+"\n")

		for _, body := range []string{
			`{}`,
			`{"lua_content":null}`,
			`{"lua_content":"print(1)","extra":true}`,
			`{"lua_content":"print(1)"}{"extra":true}`,
			`[]`,
			``,
		} {
			response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String(), body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"invalid_extension_request"}`+"\n")
		}
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should call a global function and return called without publishing", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `processed = 0
function processRequest(request)
  processed = processed + 1
end
function poke()
  poked = true
end`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", `{"function":"poke"}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"status":"called"}`+"\n")
		if got := runtime.GetGlobal("poked"); got != true {
			t.Fatalf("\nwanted:\npoke to set poked true\ngot:\n%v", got)
		}
		if got := runtime.GetGlobal("processed"); got != float64(0) {
			t.Fatalf("\nwanted:\nprocessRequest left uncalled\ngot:\n%v", got)
		}
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should push json args in order and treat omitted args as none", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `function poke(s, n, tbl)
  got_s = s
  got_n = n
  got_key = tbl.key
end
function count(...)
  argc = select("#", ...)
end`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})

		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", `{"function":"poke","args":["hello",2,{"key":"val"}]}`)
		assertControlAPIResponse(t, response, http.StatusOK, `{"status":"called"}`+"\n")
		if runtime.GetGlobal("got_s") != "hello" || runtime.GetGlobal("got_n") != float64(2) || runtime.GetGlobal("got_key") != "val" {
			t.Fatalf("\nwanted:\nhello, 2, val\ngot:\n%v %v %v", runtime.GetGlobal("got_s"), runtime.GetGlobal("got_n"), runtime.GetGlobal("got_key"))
		}

		omitted := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", `{"function":"count"}`)
		assertControlAPIResponse(t, omitted, http.StatusOK, `{"status":"called"}`+"\n")
		if runtime.GetGlobal("argc") != float64(0) {
			t.Fatalf("\nwanted:\nomitted args to pass none\ngot:\n%v", runtime.GetGlobal("argc"))
		}

		empty := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", `{"function":"count","args":[]}`)
		assertControlAPIResponse(t, empty, http.StatusOK, `{"status":"called"}`+"\n")
		if runtime.GetGlobal("argc") != float64(0) {
			t.Fatalf("\nwanted:\nempty args to pass none\ngot:\n%v", runtime.GetGlobal("argc"))
		}
	})

	t.Run("should reject a missing function, lua errors, missing ids, and invalid call bodies without publishing", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `function boom()
  error("nope")
end
function poke()
end`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		missingFn := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", `{"function":"nope"}`)
		assertControlAPIResponse(t, missingFn, http.StatusNotFound, `{"error":"function_not_found"}`+"\n")

		luaErr := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", `{"function":"boom"}`)
		assertControlAPIResponse(t, luaErr, http.StatusBadRequest, `{"error":"lua_error"}`+"\n")

		missing := requestControlAPI(server, http.MethodPost, "/extension/01937d13-9632-75b1-9e73-c5129b06fa8c/call", `{"function":"poke"}`)
		assertControlAPIResponse(t, missing, http.StatusNotFound, `{"error":"not_found"}`+"\n")

		malformed := requestControlAPI(server, http.MethodPost, "/extension/not-a-uuid/call", `{"function":"poke"}`)
		assertControlAPIResponse(t, malformed, http.StatusBadRequest, `{"error":"bad_request"}`+"\n")

		for _, body := range []string{
			`{}`,
			`{"function":""}`,
			`{"function":null}`,
			`{"function":"poke","extra":true}`,
			`{"function":"poke"}{"extra":true}`,
			`{"function":"poke","args":null}`,
			`{"function":"poke","args":{}}`,
			`{"function":"poke","args":"hello"}`,
			`{"function":"poke","args":1}`,
			`[]`,
			``,
		} {
			response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"invalid_extension_request"}`+"\n")
		}
		assertNoExtensionEvent(t, subscriber)
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

func preparedWorkshop(t *testing.T, lua string) (domain.ExtensionRepository, *extensions.Runtime) {
	t.Helper()
	conn, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening project db: %v", err)
	}
	repo := db.NewProxyRepo(conn)
	t.Cleanup(func() { repo.Close() })

	id := uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43")
	stored, err := repo.GetExtensionByUUID(id)
	if err != nil {
		t.Fatalf("getting workshop: %v", err)
	}
	stored.LuaContent = lua
	runtime := &extensions.Runtime{Data: stored}
	if err := runtime.PrepareState(&marasi.Proxy{ExtensionRepo: repo}, nil); err != nil {
		t.Fatalf("preparing workshop: %v", err)
	}
	return repo, runtime
}
