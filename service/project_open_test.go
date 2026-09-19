package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func newProjectOpenServer(t *testing.T) (*Server, *ProjectLifecycle, string, string) {
	t.Helper()
	lifecycle, _, dir := newTestProjectLifecycle(t)
	current := canonicalProjectPath(t, filepath.Join(dir, "current.marasi"))
	if err := lifecycle.Open(context.Background(), current); err != nil {
		t.Fatalf("opening current project: %v", err)
	}
	server := NewServer(nil, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "dev", "default", current)
	return server, lifecycle, dir, current
}

func projectOpenBody(path string) string {
	return fmt.Sprintf(`{"path":%q}`, path)
}

func assertProjectError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertListenerError(t, response, status, code)
}

func TestProjectOpen(t *testing.T) {
	t.Run("should open a target and report it from status", func(t *testing.T) {
		server, _, dir, current := newProjectOpenServer(t)
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))

		response := requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(target))
		want := fmt.Sprintf("{\"project\":%q}\n", target)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 application/json %s\ngot:\n%d %s %s", want, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}

		status := requestListener(t, server, http.MethodGet, "/service/status", "")
		wantStatus := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":%q,\"proxy_listener\":null}\n", target)
		if status.Code != http.StatusOK || status.Body.String() != wantStatus {
			t.Fatalf("\nwanted:\n%s\ngot:\n%d %s", wantStatus, status.Code, status.Body.String())
		}

		same := requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(current))
		wantCurrent := fmt.Sprintf("{\"project\":%q}\n", current)
		if same.Code != http.StatusOK || same.Body.String() != wantCurrent {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", wantCurrent, same.Code, same.Body.String())
		}
		noop := requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(current))
		if noop.Code != http.StatusOK || noop.Body.String() != wantCurrent {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", wantCurrent, noop.Code, noop.Body.String())
		}
	})

	t.Run("should reject invalid open requests", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		dir := t.TempDir()
		existingParent := filepath.Join(dir, "foo.marasi")
		for _, test := range []struct {
			name string
			body string
		}{
			{name: "malformed JSON", body: "{"},
			{name: "trailing JSON", body: `{"path":"/tmp/foo.marasi"} {}`},
			{name: "null object", body: "null"},
			{name: "empty body", body: ""},
			{name: "unknown field", body: `{"name":"foo"}`},
			{name: "missing path", body: "{}"},
			{name: "empty path", body: `{"path":""}`},
			{name: "null path", body: `{"path":null}`},
			{name: "relative path", body: `{"path":"foo.marasi"}`},
			{name: "relative nested path", body: `{"path":"dir/foo.marasi"}`},
			{name: "missing extension", body: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "foo"))},
			{name: "missing parent", body: fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "missing", "foo.marasi"))},
			{name: "extra field", body: fmt.Sprintf(`{"path":%q,"name":"foo"}`, existingParent)},
		} {
			t.Run(test.name, func(t *testing.T) {
				response := requestListener(t, server, http.MethodPost, "/project/open", test.body)
				assertProjectError(t, response, http.StatusBadRequest, "invalid_project_request")
			})
		}
	})

	t.Run("should reject other methods and unknown project routes", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		for _, test := range []struct {
			method string
			path   string
			status int
			allow  string
		}{
			{method: http.MethodGet, path: "/project/open", status: http.StatusMethodNotAllowed, allow: http.MethodPost},
			{method: http.MethodHead, path: "/project/open", status: http.StatusMethodNotAllowed, allow: http.MethodPost},
			{method: http.MethodPut, path: "/project/open", status: http.StatusMethodNotAllowed, allow: http.MethodPost},
			{method: http.MethodPost, path: "/project/close", status: http.StatusNotFound},
			{method: http.MethodGet, path: "/project/close", status: http.StatusNotFound},
			{method: http.MethodPost, path: "/project/unknown", status: http.StatusNotFound},
		} {
			response := requestListener(t, server, test.method, test.path, "{}")
			if response.Code != test.status {
				t.Fatalf("\n%s %s wanted:\n%d\ngot:\n%d", test.method, test.path, test.status, response.Code)
			}
			if test.allow != "" && response.Header().Get("Allow") != test.allow {
				t.Fatalf("\n%s %s wanted Allow:\n%s\ngot:\n%s", test.method, test.path, test.allow, response.Header().Get("Allow"))
			}
		}
	})

	t.Run("should map owned busy and cleanup failures", func(t *testing.T) {
		server, lifecycle, dir, current := newProjectOpenServer(t)
		owned := canonicalProjectPath(t, filepath.Join(dir, "owned.marasi"))
		held, err := acquireProjectOwnership(owned)
		if err != nil {
			t.Fatalf("holding target ownership: %v", err)
		}
		defer held()
		response := requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(owned))
		assertProjectError(t, response, http.StatusConflict, "project_already_open")
		wantCurrent := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":%q,\"proxy_listener\":null}\n", current)
		status := requestListener(t, server, http.MethodGet, "/service/status", "")
		if status.Code != http.StatusOK || status.Body.String() != wantCurrent {
			t.Fatalf("\nwanted:\n%s\ngot:\n%d %s", wantCurrent, status.Code, status.Body.String())
		}

		busy := canonicalProjectPath(t, filepath.Join(dir, "busy.marasi"))
		lifecycle.open.resources.Armory = busyArmory{}
		response = requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(busy))
		assertProjectError(t, response, http.StatusConflict, "project_busy")
		lifecycle.open.resources.Armory = nil

		target := canonicalProjectPath(t, filepath.Join(dir, "cleanup.marasi"))
		realUnlock := lifecycle.open.unlock
		lifecycle.open.unlock = func() error {
			return errors.Join(realUnlock(), errors.New("release failed"))
		}
		response = requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(target))
		assertProjectError(t, response, http.StatusInternalServerError, "project_cleanup_failed")
		wantStatus := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":%q,\"proxy_listener\":null}\n", target)
		status = requestListener(t, server, http.MethodGet, "/service/status", "")
		if status.Code != http.StatusOK || status.Body.String() != wantStatus {
			t.Fatalf("\nwanted:\n%s\ngot:\n%d %s", wantStatus, status.Code, status.Body.String())
		}
	})

	t.Run("should refuse open when intercepts are queued", func(t *testing.T) {
		server, lifecycle, dir, current := newProjectOpenServer(t)
		intercepted := &marasi.Intercepted{Type: "request", Channel: make(chan marasi.InterceptionTuple)}
		lifecycle.proxy.InterceptedQueue = []*marasi.Intercepted{intercepted}
		target := canonicalProjectPath(t, filepath.Join(dir, "queued.marasi"))
		response := requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(target))
		assertProjectError(t, response, http.StatusConflict, "project_busy")
		wantCurrent := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":%q,\"proxy_listener\":null}\n", current)
		status := requestListener(t, server, http.MethodGet, "/service/status", "")
		if status.Code != http.StatusOK || status.Body.String() != wantCurrent {
			t.Fatalf("\nwanted:\n%s\ngot:\n%d %s", wantCurrent, status.Code, status.Body.String())
		}
		if len(lifecycle.proxy.InterceptedQueue) != 1 || lifecycle.proxy.InterceptedQueue[0] != intercepted {
			t.Fatal("\nwanted:\nqueued intercept left untouched\ngot:\nqueue changed")
		}
		select {
		case <-intercepted.Channel:
			t.Fatal("\nwanted:\nintercept still waiting\ngot:\nopen finished the intercept")
		default:
		}
	})

	t.Run("should keep the current project when an open is cancelled", func(t *testing.T) {
		server, lifecycle, dir, current := newProjectOpenServer(t)
		target := canonicalProjectPath(t, filepath.Join(dir, "canceled.marasi"))
		release, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting work: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodPost, "/project/open", strings.NewReader(projectOpenBody(target))).WithContext(ctx)
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			server.ServeHTTP(response, request)
			close(done)
		}()
		deadline := time.Now().Add(time.Second)
		for {
			if _, err := os.Stat(target); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				release()
				t.Fatal("target was not prepared")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			release()
			t.Fatal("open did not return after cancel")
		}
		release()
		assertProjectError(t, response, http.StatusInternalServerError, "internal_server_error")
		if lifecycle.Path() != current {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", current, lifecycle.Path())
		}
		wantCurrent := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":%q,\"proxy_listener\":null}\n", current)
		status := requestListener(t, server, http.MethodGet, "/service/status", "")
		if status.Code != http.StatusOK || status.Body.String() != wantCurrent {
			t.Fatalf("\nwanted:\n%s\ngot:\n%d %s", wantCurrent, status.Code, status.Body.String())
		}
	})

	t.Run("should serialize concurrent open requests", func(t *testing.T) {
		server, lifecycle, dir, _ := newProjectOpenServer(t)
		first := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		second := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		results := make(chan *httptest.ResponseRecorder, 2)
		for _, path := range []string{first, second} {
			go func() {
				request := httptest.NewRequest(http.MethodPost, "/project/open", strings.NewReader(projectOpenBody(path)))
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				results <- response
			}()
		}
		bodies := map[string]bool{}
		for range 2 {
			response := <-results
			if response.Code != http.StatusOK {
				t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
			}
			bodies[response.Body.String()] = true
		}
		wantFirst := fmt.Sprintf("{\"project\":%q}\n", first)
		wantSecond := fmt.Sprintf("{\"project\":%q}\n", second)
		if !bodies[wantFirst] || !bodies[wantSecond] {
			t.Fatalf("\nwanted:\n%s and %s\ngot:\n%v", wantFirst, wantSecond, bodies)
		}
		if got := lifecycle.Path(); got != first && got != second {
			t.Fatalf("\nwanted:\none concurrent target\ngot:\n%s", got)
		}
	})

	t.Run("should wait to persist until the new project is published", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		current := canonicalProjectPath(t, filepath.Join(dir, "current.marasi"))
		if err := lifecycle.Open(context.Background(), current); err != nil {
			t.Fatalf("opening current project: %v", err)
		}
		server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "dev", "default", current)
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		release, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting work: %v", err)
		}
		openResult := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			openResult <- requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(target))
		}()
		deadline := time.Now().Add(time.Second)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			extra, err := lifecycle.Admit(ctx)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			if extra != nil {
				extra()
			}
			if time.Now().After(deadline) {
				release()
				t.Fatal("open did not pause project work")
			}
		}
		createResult := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			createResult <- requestLaunchpad(server, http.MethodPost, "/launchpad", `{"name":"During"}`)
		}()
		select {
		case got := <-createResult:
			release()
			t.Fatalf("\nwanted:\ncreate waiting through handoff\ngot:\n%d %s", got.Code, got.Body.String())
		case <-time.After(50 * time.Millisecond):
		}
		if names := launchpadNamesInProject(t, target); len(names) != 0 {
			release()
			t.Fatalf("\nwanted:\nno launchpad in new project before publication\ngot:\n%v", names)
		}
		list := requestLaunchpad(server, http.MethodGet, "/launchpad", "")
		if list.Code != http.StatusOK || list.Body.String() != "{\"items\":[]}\n" {
			release()
			t.Fatalf("\nwanted:\nempty launchpads on the old project\ngot:\n%d %s", list.Code, list.Body.String())
		}
		status := requestListener(t, server, http.MethodGet, "/service/status", "")
		wantCurrent := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":%q,\"proxy_listener\":null}\n", current)
		if status.Code != http.StatusOK || status.Body.String() != wantCurrent {
			release()
			t.Fatalf("\nwanted:\n%s\ngot:\n%d %s", wantCurrent, status.Code, status.Body.String())
		}
		release()
		openResponse := <-openResult
		if openResponse.Code != http.StatusOK {
			t.Fatalf("opening target: %d %s", openResponse.Code, openResponse.Body.String())
		}
		createResponse := <-createResult
		if createResponse.Code != http.StatusOK || !strings.Contains(createResponse.Body.String(), `"name":"During"`) {
			t.Fatalf("\nwanted:\ncreated During after publication\ngot:\n%d %s", createResponse.Code, createResponse.Body.String())
		}
		if err := lifecycle.Shutdown(); err != nil {
			t.Fatalf("shutting down: %v", err)
		}
		if names := launchpadNamesInProject(t, current); len(names) != 0 {
			t.Fatalf("\nwanted:\nno launchpad in old project\ngot:\n%v", names)
		}
		if names := launchpadNamesInProject(t, target); len(names) != 1 || names[0] != "During" {
			t.Fatalf("\nwanted:\nDuring in new project\ngot:\n%v", names)
		}
	})
}

func launchpadNamesInProject(t *testing.T, path string) []string {
	t.Helper()
	connection, err := db.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	repository := db.NewProxyRepo(connection)
	t.Cleanup(func() { _ = repository.Close() })
	launchpads, err := repository.GetLaunchpads()
	if err != nil {
		t.Fatalf("listing launchpads in %s: %v", path, err)
	}
	names := make([]string, 0, len(launchpads))
	for _, launchpad := range launchpads {
		names = append(names, launchpad.Name)
	}
	return names
}

func TestProjectEvents(t *testing.T) {
	t.Run("should stay silent for startup and a same-path no-op", func(t *testing.T) {
		server, _, _, current := newProjectOpenServer(t)
		server.heartbeatInterval = 80 * time.Millisecond
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()

		response := requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(current))
		if response.Code != http.StatusOK {
			t.Fatalf("reopening current project: %d %s", response.Code, response.Body.String())
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat after no-op: %v", err)
		}
		blank, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading heartbeat terminator: %v", err)
		}
		if got := line + blank; got != ": heartbeat\n\n" {
			t.Fatalf("\nwanted:\nno project.opened event\ngot:\n%s", got)
		}
	})

	t.Run("should publish project.opened before new-project traffic", func(t *testing.T) {
		server, lifecycle, dir, current := newProjectOpenServer(t)
		target := canonicalProjectPath(t, filepath.Join(dir, "switched.marasi"))
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		stream, reader := connectEventStream(t, httpServer.URL)
		defer stream.Body.Close()

		release, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting work: %v", err)
		}
		events := make(chan string, 1)
		go func() {
			var frame string
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				frame += line
				if line == "\n" {
					events <- frame
					return
				}
			}
		}()
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			result <- requestListener(t, server, http.MethodPost, "/project/open", projectOpenBody(target))
		}()
		deadline := time.Now().Add(time.Second)
		for {
			if _, err := os.Stat(target); err == nil {
				break
			}
			if time.Now().After(deadline) {
				release()
				t.Fatal("target was not prepared")
			}
			time.Sleep(time.Millisecond)
		}
		status := requestListener(t, server, http.MethodGet, "/service/status", "")
		wantCurrent := fmt.Sprintf("{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"default\",\"project\":%q,\"proxy_listener\":null}\n", current)
		if status.Body.String() != wantCurrent {
			release()
			t.Fatalf("\nwanted:\nold project before publication\ngot:\n%s", status.Body.String())
		}
		select {
		case got := <-events:
			release()
			t.Fatalf("\nwanted:\nno event before publication\ngot:\n%s", got)
		case <-time.After(50 * time.Millisecond):
		}
		release()
		wantOpened := fmt.Sprintf("event: project.opened\ndata: {\"project\":%q}\n\n", target)
		select {
		case got := <-events:
			if got != wantOpened {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantOpened, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("project.opened did not arrive after publication")
		}
		response := <-result
		if response.Code != http.StatusOK {
			t.Fatalf("opening target: %d %s", response.Code, response.Body.String())
		}
		if err := server.HandleRequest(domain.ProxyRequest{Path: "/after-open"}); err != nil {
			t.Fatalf("publishing traffic: %v", err)
		}
		if got := readEventFrame(t, reader); !strings.HasPrefix(got, "event: traffic.request\n") || !strings.Contains(got, `"path":"/after-open"`) {
			t.Fatalf("\nwanted:\ntraffic.request after project.opened\ngot:\n%s", got)
		}
	})
}

func TestProjectOpenHTTPServer(t *testing.T) {
	t.Run("should create a missing project through the control listener", func(t *testing.T) {
		server, _, dir, _ := newProjectOpenServer(t)
		target := canonicalProjectPath(t, filepath.Join(dir, "created.marasi"))
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, err := http.Post(httpServer.URL+"/project/open", "application/json", strings.NewReader(projectOpenBody(target)))
		if err != nil {
			t.Fatalf("opening project: %v", err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatalf("reading response: %v", readErr)
		}
		want := fmt.Sprintf("{\"project\":%q}\n", target)
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || string(body) != want {
			t.Fatalf("\nwanted:\n200 application/json %s\ngot:\n%d %s %s", want, response.StatusCode, response.Header.Get("Content-Type"), body)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("\nwanted:\ncreated project file\ngot:\n%v", err)
		}
	})
}
