package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
		repo, _ := preparedWorkshop(t, "")
		updatedAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
		id := uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43")
		if err := repo.SetExtensionSettingsByUUID(id, map[string]any{"theme": "dark"}); err != nil {
			t.Fatalf("seeding settings: %v", err)
		}
		server := newTestServer(&marasi.Proxy{ExtensionRepo: repo, Extensions: []*extensions.Runtime{
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
		repo, _ := preparedWorkshop(t, "")
		id := uuid.MustParse("01937d13-9632-72aa-83b9-c10ea1abbdd6")
		updatedAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
		server := newTestServer(&marasi.Proxy{ExtensionRepo: repo, Extensions: []*extensions.Runtime{
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
		repo, runtime := preparedWorkshop(t, "")
		id := runtime.Data.ID
		if err := repo.SetExtensionSettingsByUUID(id, map[string]any{"theme": "dark"}); err != nil {
			t.Fatalf("seeding settings: %v", err)
		}
		server := newTestServer(&marasi.Proxy{ExtensionRepo: repo, Extensions: []*extensions.Runtime{
			runtime,
		}}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String()+"/settings", "")
		assertControlAPIResponse(t, response, http.StatusOK, `{"settings":{"theme":"dark"}}`+"\n")
		assertNoExtensionEvent(t, subscriber)
	})

	t.Run("should encode missing settings as an empty object envelope", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, "")
		id := runtime.Data.ID
		server := newTestServer(&marasi.Proxy{ExtensionRepo: repo, Extensions: []*extensions.Runtime{
			runtime,
		}}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/extension/"+id.String()+"/settings", "")
		assertControlAPIResponse(t, response, http.StatusOK, `{"settings":{}}`+"\n")
	})

	t.Run("should replace settings, return the stored envelope, and publish", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		if err := repo.SetExtensionSettingsByUUID(runtime.Data.ID, map[string]any{"old": "discard"}); err != nil {
			t.Fatalf("seeding settings: %v", err)
		}
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
		detail := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String(), "")
		if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"settings":{"count":1,"theme":"dark"}`) {
			t.Fatalf("wanted replaced settings in detail, got %d %s", detail.Code, detail.Body.String())
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
		get := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String()+"/settings", "")
		assertControlAPIResponse(t, get, http.StatusOK, `{"settings":{}}`+"\n")
		detail := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String(), "")
		if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"settings":{}`) {
			t.Fatalf("wanted empty settings in detail, got %d %s", detail.Code, detail.Body.String())
		}
	})

	t.Run("should read persisted settings after lua update and call without changing the cache", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, "")
		runtime.Data.Settings = map[string]any{"stale": true}
		server := newTestServer(&marasi.Proxy{Extensions: []*extensions.Runtime{runtime}, ExtensionRepo: repo}, func() {})
		path := "/extension/" + runtime.Data.ID.String()
		lua := `marasi.settings:set({token = "update"})
function poke()
  marasi.settings:set({token = "call"})
end`
		body, err := json.Marshal(map[string]string{"lua_content": lua})
		if err != nil {
			t.Fatal(err)
		}
		updated := requestControlAPI(server, http.MethodPost, path, string(body))
		if updated.Code != http.StatusOK {
			t.Fatalf("updating lua: %d %s", updated.Code, updated.Body.String())
		}
		var detail extensionDetail
		if err := json.Unmarshal(updated.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(detail.Settings, map[string]any{"token": "update"}) {
			t.Fatalf("wanted update settings, got %v", detail.Settings)
		}
		for _, token := range []string{"update", "call"} {
			if token == "call" {
				called := requestControlAPI(server, http.MethodPost, path+"/call", `{"function":"poke"}`)
				assertControlAPIResponse(t, called, http.StatusOK, `{"status":"called"}`+"\n")
			}
			settings := requestControlAPI(server, http.MethodGet, path+"/settings", "")
			assertControlAPIResponse(t, settings, http.StatusOK, `{"settings":{"token":"`+token+`"}}`+"\n")
			get := requestControlAPI(server, http.MethodGet, path, "")
			if get.Code != http.StatusOK {
				t.Fatalf("getting detail: %d %s", get.Code, get.Body.String())
			}
			if err := json.Unmarshal(get.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(detail.Settings, map[string]any{"token": token}) {
				t.Fatalf("wanted %s settings, got %v", token, detail.Settings)
			}
		}
		if !reflect.DeepEqual(runtime.Data.Settings, map[string]any{"stale": true}) {
			t.Fatalf("runtime cache changed: %v", runtime.Data.Settings)
		}
	})

	t.Run("should map missing persisted ids and repository failures instead of returning cached settings", func(t *testing.T) {
		for _, failure := range []string{"missing row", "closed repository", "no repository"} {
			t.Run(failure, func(t *testing.T) {
				repo, runtime := preparedWorkshop(t, "")
				proxy := &marasi.Proxy{Extensions: []*extensions.Runtime{runtime}, ExtensionRepo: repo}
				status, code := http.StatusInternalServerError, "internal_server_error"
				switch failure {
				case "missing row":
					runtime.Data.ID = uuid.New()
					status, code = http.StatusNotFound, "not_found"
				case "closed repository":
					if err := repo.(io.Closer).Close(); err != nil {
						t.Fatal(err)
					}
				case "no repository":
					proxy.ExtensionRepo = nil
				}
				server := newTestServer(proxy, func() {})
				subscriber := server.events.subscribe()
				defer server.events.unsubscribe(subscriber)
				path := "/extension/" + runtime.Data.ID.String()
				for _, request := range []struct{ method, path, body string }{
					{http.MethodGet, path, ""},
					{http.MethodGet, path + "/settings", ""},
					{http.MethodPost, path + "/settings", `{"settings":{}}`},
					{http.MethodPost, path, `{"lua_content":""}`},
				} {
					response := requestControlAPI(server, request.method, request.path, request.body)
					assertControlAPIResponse(t, response, status, `{"error":"`+code+`"}`+"\n")
				}
				assertNoExtensionEvent(t, subscriber)
			})
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

	t.Run("should persist enabled, update the runtime, return the list item, and publish", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `function poke()
  poked = true
end`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/enable", `{"enabled":false}`)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || !strings.HasSuffix(response.Body.String(), "\n") {
			t.Fatalf("\nwanted:\n200 application/json with trailing newline\ngot:\n%d %s %s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}

		var summary extensionSummary
		if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
			t.Fatalf("decoding enable response: %v", err)
		}
		if summary.ID != runtime.Data.ID || summary.Name != "workshop" || summary.Enabled {
			t.Fatalf("\nwanted:\nworkshop list item enabled false\ngot:\n%+v", summary)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decoding enable fields: %v", err)
		}
		if _, ok := payload["lua_content"]; ok {
			t.Fatalf("\nwanted:\nlist item without lua_content\ngot:\n%s", response.Body.String())
		}
		if _, ok := payload["settings"]; ok {
			t.Fatalf("\nwanted:\nlist item without settings\ngot:\n%s", response.Body.String())
		}
		if _, ok := payload["logs"]; ok {
			t.Fatalf("\nwanted:\nlist item without logs\ngot:\n%s", response.Body.String())
		}

		stored, err := repo.GetExtensionByUUID(runtime.Data.ID)
		if err != nil {
			t.Fatalf("getting persisted workshop: %v", err)
		}
		if stored.Enabled || runtime.Data.Enabled {
			t.Fatalf("\nwanted:\npersisted and loaded enabled false\ngot:\nrepo %v runtime %v", stored.Enabled, runtime.Data.Enabled)
		}

		event := <-subscriber.events
		if event.name != "extension.enabled" || string(event.data) != strings.TrimSuffix(response.Body.String(), "\n") {
			t.Fatalf("\nwanted:\nextension.enabled %s\ngot:\n%s %s", strings.TrimSuffix(response.Body.String(), "\n"), event.name, event.data)
		}

		enabled := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/enable", `{"enabled":true}`)
		if enabled.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", enabled.Code, enabled.Body.String())
		}
		if err := json.Unmarshal(enabled.Body.Bytes(), &summary); err != nil {
			t.Fatalf("decoding re-enable response: %v", err)
		}
		if !summary.Enabled || !runtime.Data.Enabled {
			t.Fatalf("\nwanted:\nenabled true\ngot:\nsummary %v runtime %v", summary.Enabled, runtime.Data.Enabled)
		}
	})

	t.Run("should keep get update call logs and settings working on a disabled extension", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `function poke()
  poked = true
end`)
		runtime.Data.Enabled = false
		if err := repo.SetExtensionEnabledByUUID(runtime.Data.ID, false); err != nil {
			t.Fatalf("disabling workshop: %v", err)
		}
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})

		get := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String(), "")
		if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"enabled":false`) {
			t.Fatalf("\nwanted:\nGET disabled workshop\ngot:\n%d %s", get.Code, get.Body.String())
		}

		logs := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String()+"/logs", "")
		assertControlAPIResponse(t, logs, http.StatusOK, `{"items":[]}`+"\n")

		settings := requestControlAPI(server, http.MethodGet, "/extension/"+runtime.Data.ID.String()+"/settings", "")
		assertControlAPIResponse(t, settings, http.StatusOK, `{"settings":{}}`+"\n")

		set := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/settings", `{"settings":{"theme":"dark"}}`)
		assertControlAPIResponse(t, set, http.StatusOK, `{"settings":{"theme":"dark"}}`+"\n")

		call := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/call", `{"function":"poke"}`)
		assertControlAPIResponse(t, call, http.StatusOK, `{"status":"called"}`+"\n")
		if runtime.GetGlobal("poked") != true {
			t.Fatalf("\nwanted:\npoke to run while disabled\ngot:\n%v", runtime.GetGlobal("poked"))
		}

		update := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String(), `{"lua_content":"print(1)"}`)
		if update.Code != http.StatusOK {
			t.Fatalf("\nwanted:\nupdate while disabled\ngot:\n%d %s", update.Code, update.Body.String())
		}
	})

	t.Run("should reject a missing id and invalid enable bodies without publishing", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `print("ready")`)
		server := newTestServer(&marasi.Proxy{
			Extensions:    []*extensions.Runtime{runtime},
			ExtensionRepo: repo,
		}, func() {})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		missing := requestControlAPI(server, http.MethodPost, "/extension/01937d13-9632-75b1-9e73-c5129b06fa8c/enable", `{"enabled":false}`)
		assertControlAPIResponse(t, missing, http.StatusNotFound, `{"error":"not_found"}`+"\n")

		malformed := requestControlAPI(server, http.MethodPost, "/extension/not-a-uuid/enable", `{"enabled":false}`)
		assertControlAPIResponse(t, malformed, http.StatusBadRequest, `{"error":"bad_request"}`+"\n")

		for _, body := range []string{
			`{}`,
			`{"enabled":null}`,
			`{"enabled":"true"}`,
			`{"enabled":1}`,
			`{"enabled":true,"extra":true}`,
			`{"enabled":false}{"extra":true}`,
			`[]`,
			``,
		} {
			response := requestControlAPI(server, http.MethodPost, "/extension/"+runtime.Data.ID.String()+"/enable", body)
			assertControlAPIResponse(t, response, http.StatusBadRequest, `{"error":"invalid_extension_request"}`+"\n")
		}
		assertNoExtensionEvent(t, subscriber)
	})
}

func TestExtensionLogsDuringLuaExecution(t *testing.T) {
	repo, runtime := preparedWorkshop(t, "")
	server := newTestServer(&marasi.Proxy{ExtensionRepo: repo, Extensions: []*extensions.Runtime{runtime}}, func() {})
	path := "/extension/" + runtime.Data.ID.String() + "/logs"
	started := make(chan struct{})
	finish := make(chan struct{})
	printed := make(chan struct{})
	runtime.OnLog = func(entry extensions.ExtensionLog) error {
		if entry.Text == "1" {
			// OnLog runs inside Lua execution: snapshots must not re-lock the VM
			// or expose its live backing array to callback consumers.
			snapshot := runtime.LogSnapshot()
			snapshot[0].Text = "changed snapshot"
			close(started)
		}
		if entry.Text == "2000" {
			close(printed)
			<-finish
		}
		return nil
	}
	done := make(chan error, 1)
	defer func() {
		close(finish)
		if err := <-done; err != nil {
			t.Errorf("Lua execution: %v", err)
		}
	}()
	go func() { done <- runtime.ExecuteLua(`for i = 1, 2000 do print(i) end`) }()
	<-started
	readLogs := func() extensionLogs {
		t.Helper()
		responses := make(chan *httptest.ResponseRecorder, 1)
		go func() { responses <- requestControlAPI(server, http.MethodGet, path, "") }()
		select {
		case response := <-responses:
			if response.Code != http.StatusOK {
				t.Fatalf("logs status: %d", response.Code)
			}
			var logs extensionLogs
			if err := json.Unmarshal(response.Body.Bytes(), &logs); err != nil {
				t.Fatal(err)
			}
			return logs
		case <-time.After(2 * time.Second):
			t.Fatal("logs API waited for Lua execution to finish")
			return extensionLogs{}
		}
	}
	var first extensionLogs
	for i := 0; i < 100; i++ {
		logs := readLogs()
		if i == 0 {
			first = logs
		}
		for j, entry := range logs.Items {
			if entry.Text != fmt.Sprint(j+1) || entry.Time.IsZero() {
				t.Fatalf("log %d: %+v", j, entry)
			}
		}
	}
	<-printed
	logs := readLogs()
	if len(logs.Items) != 2000 || first.Items[0].Text != "1" {
		t.Fatalf("snapshot changed or missing logs: first=%v final=%d", first.Items[0], len(logs.Items))
	}
}

func TestExtensionMetadataDuringControlUpdates(t *testing.T) {
	t.Run("metadata remains available while Lua is held", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, `function hold() print("holding") end`)
		server := newTestServer(&marasi.Proxy{ExtensionRepo: repo, Extensions: []*extensions.Runtime{runtime}}, func() {})
		path := "/extension/" + runtime.Data.ID.String()
		response := requestControlAPI(server, http.MethodPost, path+"/enable", `{"enabled":false}`)
		if response.Code != http.StatusOK {
			t.Fatalf("disabling before Lua execution: %d %s", response.Code, response.Body.String())
		}
		entered, release := make(chan struct{}), make(chan struct{})
		runtime.OnLog = func(entry extensions.ExtensionLog) error {
			if entry.Text == "holding" {
				close(entered)
				<-release
			}
			return nil
		}
		var requests sync.WaitGroup
		var releaseOnce sync.Once
		releaseLua := func() { releaseOnce.Do(func() { close(release) }) }
		defer func() { releaseLua(); requests.Wait() }()
		start := func(method, endpoint, body string) <-chan *httptest.ResponseRecorder {
			responses := make(chan *httptest.ResponseRecorder, 1)
			requests.Add(1)
			go func() {
				defer requests.Done()
				responses <- requestControlAPI(server, method, endpoint, body)
			}()
			return responses
		}
		await := func(responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
			t.Helper()
			select {
			case response := <-responses:
				if response.Code != http.StatusOK {
					t.Fatalf("HTTP status: %d %s", response.Code, response.Body.String())
				}
				return response
			case <-time.After(2 * time.Second):
				t.Fatal("metadata HTTP operation waited for held Lua execution")
				return nil
			}
		}
		call := start(http.MethodPost, path+"/call", `{"function":"hold"}`)
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("Lua did not reach its print barrier")
		}
		await(start(http.MethodGet, path, ""))
		await(start(http.MethodGet, "/extension", ""))
		response = await(start(http.MethodPost, path+"/enable", `{"enabled":true}`))
		var summary extensionSummary
		if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil || !summary.Enabled {
			t.Fatalf("enable did not publish true while Lua was held: %s, %v", response.Body.String(), err)
		}
		response = await(start(http.MethodGet, path, ""))
		var detail extensionDetail
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || !detail.Enabled {
			t.Fatalf("metadata did not reflect enable while Lua was held: %s, %v", response.Body.String(), err)
		}
		select {
		case response := <-call:
			t.Fatalf("Lua returned before barrier release: %d %s", response.Code, response.Body.String())
		default:
		}
		releaseLua()
		await(call)
	})

	t.Run("overlapping persistence cannot publish a stale enabled flag", func(t *testing.T) {
		repo, runtime := preparedWorkshop(t, "")
		id := runtime.Data.ID
		if err := repo.SetExtensionEnabledByUUID(id, false); err != nil {
			t.Fatal(err)
		}
		held := &extensionSnapshotHoldRepository{
			ExtensionRepository: repo,
			captured:            make(chan struct{}), release: make(chan struct{}), enabledPersisted: make(chan struct{}),
		}
		server := newTestServer(&marasi.Proxy{ExtensionRepo: held, Extensions: []*extensions.Runtime{runtime}}, func() {})
		path := "/extension/" + id.String()
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(held.release) }) }
		var requests sync.WaitGroup
		defer func() { release(); requests.Wait() }()
		start := func(endpoint, body string) <-chan *httptest.ResponseRecorder {
			responses := make(chan *httptest.ResponseRecorder, 1)
			requests.Add(1)
			go func() {
				defer requests.Done()
				responses <- requestControlAPI(server, http.MethodPost, endpoint, body)
			}()
			return responses
		}
		await := func(responses <-chan *httptest.ResponseRecorder) {
			t.Helper()
			select {
			case response := <-responses:
				if response.Code != http.StatusOK {
					t.Fatalf("HTTP status: %d %s", response.Code, response.Body.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("metadata update did not finish after repository release")
			}
		}
		update := start(path, `{"lua_content":"print('updated')"}`)
		select {
		case <-held.captured:
		case <-time.After(2 * time.Second):
			t.Fatal("source update did not reach the repository snapshot barrier")
		}
		enable := start(path+"/enable", `{"enabled":true}`)
		// The captured row has enabled=false. If enable can overtake the held
		// source publication, let it finish before returning that stale row.
		select {
		case <-held.enabledPersisted:
			await(enable)
			enable = nil
		case <-time.After(200 * time.Millisecond):
		}
		release()
		await(update)
		if enable != nil {
			await(enable)
		}
		var detail extensionDetail
		response := requestControlAPI(server, http.MethodGet, path, "")
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		var list extensionList
		response = requestControlAPI(server, http.MethodGet, "/extension", "")
		if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.GetExtensionByUUID(id)
		if err != nil {
			t.Fatal(err)
		}
		if !stored.Enabled || detail.Enabled != stored.Enabled || detail.LuaContent != "print('updated')" || detail.LuaContent != stored.LuaContent || detail.UpdatedAt != stored.UpdatedAt || len(list.Items) != 1 || list.Items[0] != detail.extensionSummary {
			t.Fatalf("inconsistent metadata after overlapping updates: detail=%+v list=%+v stored=%+v", detail, list, stored)
		}
	})
}

// Delay a real repository read after it captures the persisted source metadata.
// All writes still use SQLite; only return timing is controlled by the test.
type extensionSnapshotHoldRepository struct {
	domain.ExtensionRepository
	captured         chan struct{}
	release          chan struct{}
	enabledPersisted chan struct{}
}

func (repo *extensionSnapshotHoldRepository) GetExtensionByUUID(id uuid.UUID) (*domain.Extension, error) {
	stored, err := repo.ExtensionRepository.GetExtensionByUUID(id)
	close(repo.captured)
	<-repo.release
	return stored, err
}

func (repo *extensionSnapshotHoldRepository) SetExtensionEnabledByUUID(id uuid.UUID, enabled bool) error {
	err := repo.ExtensionRepository.SetExtensionEnabledByUUID(id, enabled)
	close(repo.enabledPersisted)
	return err
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

func TestExtensionExecutionOwnsProject(t *testing.T) {
	workshopID := uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43")

	for _, hook := range []string{"processRequest", "processResponse", "processRequest HTTPS", "processResponse HTTPS"} {
		t.Run("should keep traffic "+hook+" on its project through a nested proxy admit", func(t *testing.T) {
			function := strings.Fields(hook)[0]
			assertExtensionLuaOwnsProject(t, workshopID, strings.HasSuffix(hook, "HTTPS"), func(holdURL, nestURL string) string {
				return fmt.Sprintf(`function %s(item)
  local res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
  if not res then error(err) end
  res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
  if not res then error(err) end
  marasi.settings:set({phase = "saved"})
end`, function, holdURL, nestURL)
			}, func(t *testing.T, server *Server, id uuid.UUID, body string) {
				installed := requestControlAPI(server, http.MethodPost, "/extension/"+id.String(), body)
				if installed.Code != http.StatusOK {
					t.Fatalf("installing traffic hook: %d %s", installed.Code, installed.Body.String())
				}
			}, func(server *Server, id uuid.UUID, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "http://origin/outer", nil)
				runtime := findExtensionRuntime(server.proxy, id)
				var err error
				if function == "processRequest" {
					err = runtime.CallRequestHandler(req)
				} else {
					err = runtime.CallResponseHandler(&http.Response{Request: req})
				}
				response := httptest.NewRecorder()
				if err != nil {
					response.WriteHeader(http.StatusBadRequest)
					response.WriteString(err.Error())
				} else {
					response.WriteHeader(http.StatusOK)
				}
				return response
			})
		})
	}

	for _, httpsNested := range []bool{false, true} {
		t.Run(fmt.Sprintf("should keep a function call on its project through a nested proxy admit https=%t", httpsNested), func(t *testing.T) {
			assertExtensionLuaOwnsProject(t, workshopID, httpsNested, func(holdURL, nestURL string) string {
				return fmt.Sprintf(`function poke()
  local res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
  if not res then error(err) end
  res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
  if not res then error(err) end
  marasi.settings:set({phase = "saved"})
end`, holdURL, nestURL)
			}, func(t *testing.T, server *Server, id uuid.UUID, body string) {
				t.Helper()
				installed := requestControlAPI(server, http.MethodPost, "/extension/"+id.String(), body)
				if installed.Code != http.StatusOK {
					t.Fatalf("\nwanted:\n200 installing poke\ngot:\n%d %s", installed.Code, installed.Body.String())
				}
			}, func(server *Server, id uuid.UUID, body string) *httptest.ResponseRecorder {
				return requestControlAPI(server, http.MethodPost, "/extension/"+id.String()+"/call", `{"function":"poke"}`)
			})
		})
	}

	t.Run("should keep a lua update on its project through a nested proxy admit", func(t *testing.T) {
		assertExtensionLuaOwnsProject(t, workshopID, false, func(holdURL, nestURL string) string {
			return fmt.Sprintf(`local res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
if not res then error(err) end
res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
if not res then error(err) end
marasi.settings:set({phase = "saved"})
`, holdURL, nestURL)
		}, nil, func(server *Server, id uuid.UUID, body string) *httptest.ResponseRecorder {
			return requestControlAPI(server, http.MethodPost, "/extension/"+id.String(), body)
		})
	})
	t.Run("should keep an async callback on its project through a nested proxy admit", func(t *testing.T) {
		assertExtensionLuaOwnsProject(t, workshopID, false, func(holdURL, nestURL string) string {
			return fmt.Sprintf(`function poke()
  marasi:builder():set_method("GET"):set_url("%s"):send_async(function(res, err)
    if not res then error(err) end
    res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
    if not res then error(err) end
    marasi.settings:set({phase = "saved"})
  end)
end`, holdURL, nestURL)
		}, func(t *testing.T, server *Server, id uuid.UUID, body string) {
			installed := requestControlAPI(server, http.MethodPost, "/extension/"+id.String(), body)
			if installed.Code != http.StatusOK {
				t.Fatalf("installing async callback: %d %s", installed.Code, installed.Body.String())
			}
		}, func(server *Server, id uuid.UUID, body string) *httptest.ResponseRecorder {
			return requestControlAPI(server, http.MethodPost, "/extension/"+id.String()+"/call", `{"function":"poke"}`)
		})
	})
}

// assertExtensionLuaOwnsProject runs Lua that blocks, then admits like a nested
// proxy request, then writes settings. A concurrent project switch must wait
// until that Lua finishes, and the nested admit must not deadlock the switch.
func assertExtensionLuaOwnsProject(t *testing.T, id uuid.UUID, httpsNested bool, lua func(holdURL, nestURL string) string, prepare func(*testing.T, *Server, uuid.UUID, string), start func(*Server, uuid.UUID, string) *httptest.ResponseRecorder) {
	t.Helper()
	server, lifecycle, dir, current := newProjectOpenServer(t)
	if err := lifecycle.proxy.WithOptions(marasi.WithBasePipeline()); err != nil {
		t.Fatalf("installing proxy pipeline: %v", err)
	}
	if httpsNested {
		if err := lifecycle.proxy.WithOptions(marasi.WithTLS()); err != nil {
			t.Fatalf("configuring proxy TLS: %v", err)
		}
		lifecycle.proxy.AddRequestModifier(marasi.SkipConnectRequestModifier)
		// Terminate client TLS at the proxy; the test origin speaks plain HTTP.
		lifecycle.proxy.AddRequestModifier(func(_ *marasi.Proxy, req *http.Request) error {
			req.URL.Scheme = "http"
			return nil
		})
	}
	listener := NewListenerLifecycle(lifecycle.proxy, io.Discard)
	listener.BindProject(lifecycle)
	t.Cleanup(func() {
		if err := listener.Shutdown(); err != nil {
			t.Errorf("shutting down listener: %v", err)
		}
	})
	if _, err := listener.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
		t.Fatalf("starting proxy listener: %v", err)
	}
	proxyTransport := lifecycle.proxy.Client.Transport
	target := canonicalProjectPath(t, filepath.Join(dir, "during-lua.marasi"))
	hold := &extensionLuaHold{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		nested:  make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseHold := func() { releaseOnce.Do(func() { close(hold.release) }) }
	t.Cleanup(releaseHold)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hold":
			close(hold.entered)
			<-hold.release
		case "/nest":
			close(hold.nested)
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(origin.Close)
	nestURL := origin.URL + "/nest"
	if httpsNested {
		nestURL = strings.Replace(nestURL, "http://", "https://", 1)
	}

	direct := &http.Transport{}
	t.Cleanup(direct.CloseIdleConnections)
	lifecycle.proxy.Client.Transport = direct
	admittedWhileBlocked := false
	var admittedMu sync.Mutex
	if err := lifecycle.proxy.WithOptions(marasi.WithWorkAdmission(func(ctx context.Context, internal bool) (func(), error) {
		release, err := lifecycle.gate.admit(ctx, internal)
		if err != nil {
			return nil, err
		}
		lifecycle.gate.mu.Lock()
		blocked := lifecycle.gate.blocked
		lifecycle.gate.mu.Unlock()
		if blocked {
			admittedMu.Lock()
			admittedWhileBlocked = true
			admittedMu.Unlock()
		}
		return release, nil
	})); err != nil {
		t.Fatalf("wrapping work admission: %v", err)
	}

	script := lua(origin.URL+"/hold", nestURL)
	encoded, err := json.Marshal(map[string]string{"lua_content": script})
	if err != nil {
		t.Fatalf("encoding lua: %v", err)
	}
	body := string(encoded)
	if prepare != nil {
		prepare(t, server, id, body)
	}

	ownedDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		ownedDone <- start(server, id, body)
	}()
	var owned *httptest.ResponseRecorder
	select {
	case <-hold.entered:
	case response := <-ownedDone:
		owned = response
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\nlua to reach its proxy send\ngot:\n%d %s", response.Code, response.Body.String())
		}
		select {
		case <-hold.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for async lua to send")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for extension lua to send")
	}

	openDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/project/open", strings.NewReader(projectOpenBody(target)))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		openDone <- response
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case response := <-openDone:
			releaseHold()
			t.Fatalf("\nwanted:\nswitch to wait while extension lua runs\ngot:\n%d %s", response.Code, response.Body.String())
		default:
		}
		lifecycle.gate.mu.Lock()
		blocked := lifecycle.gate.blocked
		lifecycle.gate.mu.Unlock()
		if blocked && lifecycle.Path() == current {
			break
		}
		if time.Now().After(deadline) {
			releaseHold()
			t.Fatal("switch did not stop for in-flight extension lua")
		}
		time.Sleep(time.Millisecond)
	}

	lifecycle.proxy.Client.Transport = proxyTransport
	releaseHold()

	deadline = time.Now().Add(5 * time.Second)
	var opened *httptest.ResponseRecorder
	nestedSeen := false
	for !nestedSeen || owned == nil || opened == nil {
		var nested <-chan struct{}
		if !nestedSeen {
			nested = hold.nested
		}
		select {
		case <-nested:
			nestedSeen = true
		case response := <-openDone:
			opened = response
		case response := <-ownedDone:
			owned = response
		case <-time.After(time.Until(deadline)):
			t.Fatal("nested proxy admit deadlocked the project switch")
		}
	}
	admittedMu.Lock()
	blockedAdmit := admittedWhileBlocked
	admittedMu.Unlock()
	if !blockedAdmit {
		t.Fatal("\nwanted:\nnested proxy admit while the switch was blocked\ngot:\nadmit after the gate reopened")
	}
	if owned.Code != http.StatusOK {
		t.Fatalf("\nwanted:\n200 after settings write\ngot:\n%d %s", owned.Code, owned.Body.String())
	}
	if opened.Code != http.StatusOK {
		t.Fatalf("\nwanted:\nswitch after extension lua\ngot:\n%d %s", opened.Code, opened.Body.String())
	}
	if lifecycle.Path() != target {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", target, lifecycle.Path())
	}

	conn, err := db.New(current, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopening original project: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	stored, err := db.NewProxyRepo(conn).GetExtensionSettingsByUUID(id)
	if err != nil {
		t.Fatalf("reading settings from original project: %v", err)
	}
	if !reflect.DeepEqual(stored, map[string]any{"phase": "saved"}) {
		t.Fatalf("\nwanted:\nsettings written on the original project\ngot:\n%v", stored)
	}
}

type extensionLuaHold struct {
	entered chan struct{}
	release chan struct{}
	nested  chan struct{}
}
