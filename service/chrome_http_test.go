package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi"
)

func TestChromeControlRoutes(t *testing.T) {
	t.Run("should list add and remove paths with exact responses and events", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		assertChromeResponse(t, requestChrome(server, http.MethodGet, "/chrome/path", ""), http.StatusOK, `{"items":[]}`+"\n")
		assertNoChromeEvent(t, subscriber)

		body := `{"os":"linux","path":"/custom/chrome"}`
		want := `{"items":[{"os":"linux","path":"/custom/chrome"}]}` + "\n"
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/path", body), http.StatusOK, want)
		assertChromeEvent(t, subscriber, "chrome.path.added", strings.TrimSpace(want))
		assertChromeError(t, requestChrome(server, http.MethodPost, "/chrome/path", body), http.StatusConflict, "path_already_exists")
		assertNoChromeEvent(t, subscriber)

		assertChromeResponse(t, requestChrome(server, http.MethodDelete, "/chrome/path", body), http.StatusOK, `{"items":[]}`+"\n")
		assertChromeEvent(t, subscriber, "chrome.path.removed", `{"items":[]}`)
		assertChromeError(t, requestChrome(server, http.MethodDelete, "/chrome/path", body), http.StatusNotFound, "not_found")
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should list add and remove profiles with exact responses and events", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		assertChromeResponse(t, requestChrome(server, http.MethodGet, "/chrome/profile", ""), http.StatusOK, `{"items":[]}`+"\n")
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/profile", `{"name":" pentest "}`), http.StatusOK, `{"items":[{"name":"pentest"}]}`+"\n")
		assertChromeEvent(t, subscriber, "chrome.profile.added", `{"items":[{"name":"pentest"}]}`)
		assertChromeError(t, requestChrome(server, http.MethodPost, "/chrome/profile", `{"name":"pentest"}`), http.StatusConflict, "profile_already_exists")
		assertNoChromeEvent(t, subscriber)

		assertChromeResponse(t, requestChrome(server, http.MethodDelete, "/chrome/profile/pentest", ""), http.StatusOK, `{"items":[]}`+"\n")
		assertChromeEvent(t, subscriber, "chrome.profile.removed", `{"items":[]}`)
		assertChromeError(t, requestChrome(server, http.MethodDelete, "/chrome/profile/pentest", ""), http.StatusNotFound, "not_found")
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should reject invalid JSON and values without events", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		pathBodies := []string{
			``, `null`, `{}`, `{"os":"linux"}`, `{"os":null,"path":"/chrome"}`,
			`{"os":"linux","path":null}`, `{"os":"linux","path":""}`,
			`{"os":"plan9","path":"/chrome"}`, `{"os":"linux","path":"/chrome","extra":true}`,
			`{"os":"linux","path":"/chrome"} {}`,
		}
		for _, body := range pathBodies {
			assertChromeError(t, requestChrome(server, http.MethodPost, "/chrome/path", body), http.StatusBadRequest, "invalid_chrome_request")
		}

		profileBodies := []string{
			`null`, `{}`, `{"name":null}`, `{"name":""}`, `{"name":"../pentest"}`,
			`{"name":"profiles/pentest"}`, `{"name":"/pentest"}`, `{"name":"pentest","extra":true}`,
		}
		for _, body := range profileBodies {
			assertChromeError(t, requestChrome(server, http.MethodPost, "/chrome/profile", body), http.StatusBadRequest, "invalid_chrome_request")
		}

		startBodies := []string{`null`, `{"profile":null}`, `{"profile":""}`, `{"unknown":true}`, `{} {}`}
		for _, body := range startBodies {
			assertChromeError(t, requestChrome(server, http.MethodPost, "/chrome/start", body), http.StatusBadRequest, "invalid_chrome_request")
		}
		assertChromeError(t, requestChrome(server, http.MethodDelete, "/chrome/profile/pentest", `{}`), http.StatusBadRequest, "invalid_chrome_request")
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should reject unsupported methods", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		for _, request := range []struct {
			method string
			path   string
		}{
			{http.MethodPut, "/chrome/path"},
			{http.MethodDelete, "/chrome/profile"},
			{http.MethodGet, "/chrome/start"},
		} {
			response := requestChrome(server, request.method, request.path, "")
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("\n%s %s wanted:\n405\ngot:\n%d %s", request.method, request.path, response.Code, response.Body.String())
			}
		}
	})
}

func TestChromeStartRoute(t *testing.T) {
	t.Run("should reject an inactive listener without publishing an event", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		assertChromeError(t, requestChrome(server, http.MethodPost, "/chrome/start", ""), http.StatusConflict, "listener_inactive")
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should start the default and named profiles without publishing events", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test executable is a shell script")
		}
		server := newChromeHTTPServer(t, activeListenerStatus("127.0.0.1:43210"))
		executable := writeChromeHTTPExecutable(t, server.proxy.ConfigDir, "chrome", "#!/bin/sh\nexit 0\n")
		pathBody := `{"os":"` + runtime.GOOS + `","path":"` + executable + `"}`
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/path", pathBody), http.StatusOK, `{"items":[{"os":"`+runtime.GOOS+`","path":"`+executable+`"}]}`+"\n")
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/profile", `{"name":"pentest"}`), http.StatusOK, `{"items":[{"name":"pentest"}]}`+"\n")

		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/start", ""), http.StatusOK, `{"status":"started","profile":"default-profile"}`+"\n")
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/start", `{}`), http.StatusOK, `{"status":"started","profile":"default-profile"}`+"\n")
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/start", `{"profile":"pentest"}`), http.StatusOK, `{"status":"started","profile":"pentest"}`+"\n")
		assertChromeError(t, requestChrome(server, http.MethodPost, "/chrome/start", `{"profile":"missing"}`), http.StatusNotFound, "not_found")
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should hide an unavailable executable error and write it to the instance log", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("invalid executable behavior differs on Windows")
		}
		server := newChromeHTTPServer(t, activeListenerStatus("127.0.0.1:43210"))
		badExecutable := writeChromeHTTPExecutable(t, server.proxy.ConfigDir, "bad-chrome", "not an executable format")
		pathBody := `{"os":"` + runtime.GOOS + `","path":"` + badExecutable + `"}`
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/chrome/path", pathBody), http.StatusOK, `{"items":[{"os":"`+runtime.GOOS+`","path":"`+badExecutable+`"}]}`+"\n")

		var log bytes.Buffer
		server.chrome.logWriter = &log
		response := requestChrome(server, http.MethodPost, "/chrome/start", "")
		assertChromeError(t, response, http.StatusConflict, "chrome_unavailable")
		if strings.Contains(response.Body.String(), "exec format") || !strings.Contains(log.String(), "exec format") {
			t.Fatalf("\nwanted:\nOS error only in instance log\ngot response:\n%s\ngot log:\n%s", response.Body.String(), log.String())
		}
	})
}

func newChromeHTTPServer(t *testing.T, status ListenerStatus) *Server {
	t.Helper()
	proxy, err := marasi.New(marasi.WithConfigDir(t.TempDir()))
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	return NewServer(proxy, &statusListener{status: status}, nil, func() {}, "dev", "default", "scratchpad")
}

func requestChrome(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertChromeResponse(t *testing.T, response *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if response.Code != status || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != body {
		t.Fatalf("\nwanted:\n%d application/json %s\ngot:\n%d %s %s", status, body, response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func assertChromeError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertChromeResponse(t, response, status, `{"error":"`+code+`"}`+"\n")
}

func assertChromeEvent(t *testing.T, subscriber *eventSubscriber, name, data string) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		if event.name != name || string(event.data) != data {
			t.Fatalf("\nwanted:\n%s %s\ngot:\n%s %s", name, data, event.name, event.data)
		}
	default:
		t.Fatalf("\nwanted:\n%s event\ngot:\nno event", name)
	}
}

func assertNoChromeEvent(t *testing.T, subscriber *eventSubscriber) {
	t.Helper()
	select {
	case event := <-subscriber.events:
		t.Fatalf("\nwanted:\nno event\ngot:\n%s %s", event.name, event.data)
	default:
	}
}

func writeChromeHTTPExecutable(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
		t.Fatalf("writing fake Chrome executable: %v", err)
	}
	return path
}
