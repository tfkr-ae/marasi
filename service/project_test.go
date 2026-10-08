package service

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shopify/go-lua"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	marasiws "github.com/tfkr-ae/marasi/websocket"
	"github.com/tfkr-ae/marasi/wordlist"
)

type busyArmory struct{}

func (busyArmory) Repo() domain.ArmoryRepository       { return nil }
func (busyArmory) ValidateRun(*domain.ArmoryRun) error { return nil }
func (busyArmory) StartRun(uuid.UUID) error            { return nil }
func (busyArmory) CancelRun(uuid.UUID) error           { return nil }
func (busyArmory) ActiveRunIDs() []uuid.UUID           { return []uuid.UUID{uuid.Nil} }
func (busyArmory) Shutdown()                           {}

func newTestProjectLifecycle(t *testing.T, extensionOptions ...func(*extensions.Runtime) error) (*ProjectLifecycle, *marasi.Proxy, string) {
	t.Helper()
	configDir := t.TempDir()
	manager, err := wordlist.NewManager(configDir)
	if err != nil {
		t.Fatalf("creating wordlist manager: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy, err := marasi.New(marasi.WithLogger(logger), marasi.WithConfigDir(configDir))
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	lifecycle := NewProjectLifecycle(proxy, configDir, manager, logger, extensionOptions...)
	t.Cleanup(func() { _ = lifecycle.Shutdown() })
	return lifecycle, proxy, configDir
}

func holdPendingWebSocket(t *testing.T, proxy *marasi.Proxy) func() {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating message id: %v", err)
	}
	message := &marasiws.Message{ID: id, ConnectionID: uuid.New()}
	done := make(chan struct{})
	go func() {
		_ = proxy.WebSocketInterceptor.Intercept(message, nil)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if proxy.HasPendingCheckpoint() {
			return func() {
				if err := proxy.DropCheckpoint(id); err != nil {
					t.Fatalf("dropping websocket hold: %v", err)
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out releasing websocket hold")
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for websocket hold")
	return nil
}

func canonicalProjectPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := ResolveProjectPath(path)
	if err != nil {
		t.Fatalf("resolving project path: %v", err)
	}
	return canonical
}

func TestProjectLifecycle(t *testing.T) {
	for _, mode := range []string{"sync", "async", "checkpoint", "sync-https", "async-https", "sync-checkpoint"} {
		t.Run("should exclude unrelated work during a late WebSocket Lua callback "+mode, func(t *testing.T) {
			async := strings.HasPrefix(mode, "async") || mode == "checkpoint"
			checkpoint := strings.Contains(mode, "checkpoint")
			httpsNested := strings.Contains(mode, "https")
			server, lifecycle, dir, current := newProjectOpenServer(t)
			proxy := lifecycle.proxy
			if err := proxy.WithOptions(marasi.WithBasePipeline(), marasi.WithDefaultModifierPipeline(), marasi.WithRequestHandler(server.HandleRequest), marasi.WithResponseHandler(server.HandleResponse)); err != nil {
				t.Fatal(err)
			}
			if httpsNested {
				if err := proxy.WithOptions(marasi.WithTLS()); err != nil {
					t.Fatal(err)
				}
				// Exercise CONNECT and client TLS at the proxy; the origin speaks HTTP.
				proxy.AddRequestModifier(func(_ *marasi.Proxy, req *http.Request) error { req.URL.Scheme = "http"; return nil })
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseLua := func() { once.Do(func() { close(release) }) }
			t.Cleanup(releaseLua)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/hold" {
					close(entered)
					<-release
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.URL.Path == "/unrelated" {
					w.WriteHeader(http.StatusOK)
					return
				}
				conn, buffered, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
				buffered.Flush()
				for {
					frame, err := marasiws.ReadFrame(buffered)
					if err != nil {
						return
					}
					if frame.Opcode == marasiws.OpClose {
						marasiws.WriteFrame(buffered, frame, false)
						buffered.Flush()
						return
					}
				}
			}))
			t.Cleanup(func() { releaseLua(); origin.Close() })
			holdURL := origin.URL + "/hold"
			if httpsNested {
				holdURL = strings.Replace(holdURL, "http://", "https://", 1)
			}
			runtime := findExtensionRuntime(proxy, uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43"))
			runtime.Data.Enabled = true
			checkpointHeld := proxy.CheckpointHoldStarted()
			script := fmt.Sprintf(`function processWebSocketMessage(message)
  local res, err = marasi:builder():set_method("GET"):set_url("%s"):send()
  if not res then error(err) end
  marasi.settings:set({phase = "closed"})
end`, holdURL)
			if async {
				script = fmt.Sprintf(`function processWebSocketMessage(message)
  marasi:builder():set_method("GET"):set_url("%s"):send_async(function(res, err)
    if not res then error(err) end
    marasi.settings:set({phase = "closed"})
  end)
  print("sent")
end`, holdURL)
				runtime.OnLog = func(extensions.ExtensionLog) error {
					if checkpoint {
						select {
						case <-checkpointHeld:
						case <-release:
						}
						close(entered)
						return nil
					}
					select {
					case <-entered:
					case <-release:
					}
					return nil
				}
			}
			if err := runtime.ExecuteLua(script); err != nil {
				t.Fatal(err)
			}
			listener := NewListenerLifecycle(proxy, io.Discard)
			listener.BindProject(lifecycle)
			t.Cleanup(func() { releaseLua(); listener.Shutdown() })
			if _, err := listener.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
				t.Fatal(err)
			}
			address := net.JoinHostPort(proxy.Addr, proxy.Port)
			conn, request := upgradeWebSocketThroughProxy(t, address, origin.URL)
			t.Cleanup(func() { conn.Close() })
			response, err := http.ReadResponse(bufio.NewReader(conn), request)
			if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("upgrade: %v %v", response, err)
			}
			target := canonicalProjectPath(t, filepath.Join(dir, "late-callback.marasi"))
			opened := make(chan error, 1)
			if checkpoint {
				proxy.SetIntercept(true)
			}
			enteredWait := (<-chan struct{})(entered)
			if checkpoint {
				enteredWait = checkpointHeld
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go func() { opened <- lifecycle.Open(ctx, target) }()
			select {
			case <-enteredWait:
			case <-time.After(5 * time.Second):
				t.Fatal("flush did not execute the peer close frame's Lua callback")
			}
			if lifecycle.Path() != current {
				t.Fatal("published while close callback is running")
			}
			if checkpoint {
				var err error
				select {
				case err = <-opened:
				case <-time.After(500 * time.Millisecond):
					t.Error("handoff did not abort when late Lua created a Checkpoint hold")
					for _, item := range proxy.CheckpointItems() {
						proxy.DropCheckpoint(item.ID)
					}
					err = <-opened
				}
				proxy.SetIntercept(false)
				for _, item := range proxy.CheckpointItems() {
					proxy.DropCheckpoint(item.ID)
				}
				releaseLua()
				if !errors.Is(err, ErrProjectBusy) {
					t.Fatalf("late Checkpoint hold should abort handoff, got %v", err)
				}
				status := requestControlAPI(server, http.MethodGet, "/traffic", "")
				if status.Code != http.StatusOK {
					t.Fatalf("admission did not reopen: %s", status.Body.String())
				}
				return
			}
			select {
			case err := <-opened:
				releaseLua()
				t.Fatalf("handoff returned before late nested request completed: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			for _, kind := range []string{"control", "proxy"} {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				var err error
				if kind == "control" {
					req := httptest.NewRequest(http.MethodPost, "/scope/check", strings.NewReader(`{"url":"http://example.com"}`)).WithContext(ctx)
					recorder := httptest.NewRecorder()
					server.ServeHTTP(recorder, req)
					err = ctx.Err()
				} else {
					proxyURL, _ := url.Parse("http://" + address)
					transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
					client := &http.Client{Transport: transport}
					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/unrelated", nil)
					// Skip the busy VM so its mutex cannot hide an admission leak.
					// An extension ID alone is not proof that this is nested work.
					req.Header.Set("x-extension-id", runtime.Data.ID.String())
					res, requestErr := client.Do(req)
					if res != nil {
						res.Body.Close()
					}
					transport.CloseIdleConnections()
					err = requestErr
				}
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("unrelated %s work crossed the blocked handoff: %v", kind, err)
				}
			}
			releaseLua()
			select {
			case err := <-opened:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handoff deadlocked on nested Lua proxy request")
			}
			settingsPath := "/extension/" + runtime.Data.ID.String() + "/settings"
			settings := requestControlAPI(server, http.MethodGet, settingsPath, "")
			if strings.Contains(settings.Body.String(), `"phase":"closed"`) {
				t.Fatalf("old callback wrote settings into the target: %s", settings.Body.String())
			}
			traffic := requestControlAPI(server, http.MethodGet, "/traffic", "")
			if strings.Contains(traffic.Body.String(), `"path":"/hold"`) {
				t.Fatalf("nested traffic leaked into target: %s", traffic.Body.String())
			}
			if err := lifecycle.Open(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			settings = requestControlAPI(server, http.MethodGet, settingsPath, "")
			assertControlAPIResponse(t, settings, http.StatusOK, `{"settings":{"phase":"closed"}}`+"\n")
			traffic = requestControlAPI(server, http.MethodGet, "/traffic", "")
			var captured trafficList
			if err := json.Unmarshal(traffic.Body.Bytes(), &captured); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range captured.Items {
				if item.Path == "/hold" && item.StatusCode == http.StatusOK {
					found = true
				}
			}
			if !found {
				t.Fatalf("old project lost the nested response: %s", traffic.Body.String())
			}
		})
	}
	t.Run("should cancel non-returning Lua before releasing project ownership during shutdown", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		path := canonicalProjectPath(t, filepath.Join(dir, "hang.marasi"))
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		runtime := findExtensionRuntime(proxy, uuid.MustParse("01937d13-9632-7f84-add5-14ec2c2c7f43"))
		if err := runtime.ExecuteLua(`function hang() print("entered"); while true do end end`); err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		runtime.OnLog = func(_ extensions.ExtensionLog) error { close(entered); return nil }
		done := make(chan error, 1)
		go func() { done <- runtime.CallFunction("hang") }()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("Lua did not enter hang")
		}
		if _, err := acquireProjectOwnership(path); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("project ownership released under running VM: %v", err)
		}
		stopped := make(chan error, 1)
		go func() { stopped <- lifecycle.Shutdown() }()
		select {
		case err := <-stopped:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("shutdown did not cancel non-returning Lua")
		}
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "context canceled") {
				t.Fatalf("wanted cancellation, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("shutdown released ownership before Lua stopped")
		}
		unlock, err := acquireProjectOwnership(path)
		if err != nil {
			t.Fatalf("project ownership remains held: %v", err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("should publish all target resources and release the previous ownership", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		first := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		second := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		if err := lifecycle.Open(context.Background(), first); err != nil {
			t.Fatalf("opening first project: %v", err)
		}
		oldRepository := proxy.TrafficRepo
		if _, err := acquireProjectOwnership(first); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nowned first project\ngot:\n%v", err)
		}

		if err := lifecycle.Open(context.Background(), second); err != nil {
			t.Fatalf("opening second project: %v", err)
		}
		if lifecycle.Path() != second {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", second, lifecycle.Path())
		}
		if proxy.TrafficRepo == oldRepository || any(proxy.TrafficRepo) != any(proxy.ConfigRepo) || any(proxy.TrafficRepo) != any(proxy.WaypointRepo) || any(proxy.TrafficRepo) != any(proxy.ReportingRepo) {
			t.Fatal("\nwanted:\nall repositories replaced together\ngot:\nmixed project repositories")
		}
		if proxy.Scope == nil || proxy.Waypoints == nil || proxy.Armory == nil || proxy.ReportGenerator == nil {
			t.Fatal("\nwanted:\nprepared scope, waypoints, armory, and reports\ngot:\nmissing project dependency")
		}
		unlock, err := acquireProjectOwnership(first)
		if err != nil {
			t.Fatalf("acquiring released first project: %v", err)
		}
		_ = unlock()
		if _, err := acquireProjectOwnership(second); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nowned second project\ngot:\n%v", err)
		}
	})

	t.Run("should acquire ownership before opening the target database", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		path := canonicalProjectPath(t, filepath.Join(dir, "project.marasi"))
		var order []string
		realLock := lifecycle.lock
		lifecycle.lock = func(path string) (func() error, error) {
			order = append(order, "lock")
			return realLock(path)
		}
		realPrepare := lifecycle.prepare
		lifecycle.prepare = func(ctx context.Context, path string) (marasi.ProjectResources, error) {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("\nwanted:\nlock before the project file exists\ngot:\nproject file already created")
			}
			order = append(order, "open")
			return realPrepare(ctx, path)
		}
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("opening project: %v", err)
		}
		if got := strings.Join(order, ","); got != "lock,open" {
			t.Fatalf("\nwanted:\nlock,open\ngot:\n%s", got)
		}
	})

	t.Run("should make the same canonical path a successful no-op", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		path := canonicalProjectPath(t, filepath.Join(dir, "project.marasi"))
		lockCalls := 0
		realLock := lifecycle.lock
		lifecycle.lock = func(path string) (func() error, error) {
			lockCalls++
			return realLock(path)
		}
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("opening project: %v", err)
		}
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("reopening project: %v", err)
		}
		if lockCalls != 1 {
			t.Fatalf("\nwanted:\n1 ownership acquisition\ngot:\n%d", lockCalls)
		}
	})

	t.Run("should retain the old project when finalization fails", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		lifecycle.flushOpenProject = func() error { return errors.New("flush failed") }
		if err := lifecycle.Open(context.Background(), target); err == nil || err.Error() != "flush failed" {
			t.Fatalf("\nwanted:\nflush failed\ngot:\n%v", err)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("\nwanted:\nprepared target preserved\ngot:\n%v", err)
		}
	})

	t.Run("should remove a newly created failed target and preserve an existing target", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		lifecycle.prepare = func(_ context.Context, path string) (marasi.ProjectResources, error) {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				_, err = file.WriteString("attempt")
				_ = file.Close()
			}
			if err != nil && !errors.Is(err, os.ErrExist) {
				t.Fatalf("creating attempted target: %v", err)
			}
			return marasi.ProjectResources{}, errors.New("prepare failed")
		}
		created := canonicalProjectPath(t, filepath.Join(dir, "created.marasi"))
		if err := lifecycle.Open(context.Background(), created); err == nil {
			t.Fatal("\nwanted:\npreparation failure\ngot:\nnil")
		}
		if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\ncreated target removed\ngot:\n%v", err)
		}
		existing := canonicalProjectPath(t, filepath.Join(dir, "existing.marasi"))
		if err := os.WriteFile(existing, []byte("original"), 0600); err != nil {
			t.Fatalf("creating existing target: %v", err)
		}
		if err := lifecycle.Open(context.Background(), existing); err == nil {
			t.Fatal("\nwanted:\npreparation failure\ngot:\nnil")
		}
		contents, err := os.ReadFile(existing)
		if err != nil || string(contents) != "original" {
			t.Fatalf("\nwanted:\nexisting target preserved\ngot:\n%q %v", contents, err)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
	})

	t.Run("should release ownership during shutdown", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		path := canonicalProjectPath(t, filepath.Join(dir, "project.marasi"))
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("opening project: %v", err)
		}
		if err := lifecycle.Shutdown(); err != nil {
			t.Fatalf("shutting down: %v", err)
		}
		if lifecycle.Path() != "" {
			t.Fatalf("\nwanted:\nno project after shutdown\ngot:\n%s", lifecycle.Path())
		}
		unlock, err := acquireProjectOwnership(path)
		if err != nil {
			t.Fatalf("acquiring project after shutdown: %v", err)
		}
		_ = unlock()
	})

	t.Run("should cancel before publication and retain the old project", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		release, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting work: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- lifecycle.Open(ctx, target) }()
		deadline := time.Now().Add(time.Second)
		for {
			if _, err := os.Stat(target); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("target was not prepared")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("\nwanted:\ncontext canceled\ngot:\n%v", err)
		}
		release()
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("\nwanted:\ncanceled prepared target preserved\ngot:\n%v", err)
		}
	})

	t.Run("should wait for admitted work and hold new work until publication", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		releaseOld, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting old work: %v", err)
		}
		result := make(chan error, 1)
		go func() { result <- lifecycle.Open(context.Background(), target) }()
		for {
			lifecycle.gate.mu.Lock()
			blocked := lifecycle.gate.blocked
			lifecycle.gate.mu.Unlock()
			if blocked {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\nold path before publication\ngot:\n%s", lifecycle.Path())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := lifecycle.Admit(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("\nwanted:\nnew work blocked\ngot:\n%v", err)
		}
		releaseOld()
		if err := <-result; err != nil {
			t.Fatalf("opening target: %v", err)
		}
		if lifecycle.Path() != target {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", target, lifecycle.Path())
		}
		releaseNew, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting new work: %v", err)
		}
		releaseNew()
	})

	t.Run("should admit nested work during extension execution and hold it after", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		releaseOld, err := lifecycle.Admit(context.Background())
		if err != nil {
			t.Fatalf("admitting extension execution: %v", err)
		}
		endExecution := lifecycle.gate.trackExtension()
		result := make(chan error, 1)
		go func() { result <- lifecycle.Open(context.Background(), target) }()
		deadline := time.Now().Add(time.Second)
		for {
			lifecycle.gate.mu.Lock()
			blocked := lifecycle.gate.blocked
			lifecycle.gate.mu.Unlock()
			if blocked {
				break
			}
			if time.Now().After(deadline) {
				endExecution()
				releaseOld()
				t.Fatal("handoff did not block")
			}
			time.Sleep(time.Millisecond)
		}

		nested, err := lifecycle.gate.admit(context.Background(), true)
		if err != nil {
			endExecution()
			releaseOld()
			t.Fatalf("admitting nested work: %v", err)
		}
		nested()
		endExecution()

		plain, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := lifecycle.Admit(plain); !errors.Is(err, context.DeadlineExceeded) {
			releaseOld()
			t.Fatalf("\nwanted:\nnew work to wait after extension execution\ngot:\n%v", err)
		}
		releaseOld()
		if err := <-result; err != nil {
			t.Fatalf("opening target: %v", err)
		}
		if lifecycle.Path() != target {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", target, lifecycle.Path())
		}
	})

	t.Run("should serialize simultaneous handoffs", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		initial := canonicalProjectPath(t, filepath.Join(dir, "initial.marasi"))
		first := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		second := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		if err := lifecycle.Open(context.Background(), initial); err != nil {
			t.Fatalf("opening initial project: %v", err)
		}
		realPrepare := lifecycle.prepare
		entered := make(chan string, 2)
		continueFirst := make(chan struct{})
		lifecycle.prepare = func(ctx context.Context, path string) (marasi.ProjectResources, error) {
			entered <- path
			if path == first {
				<-continueFirst
			}
			return realPrepare(ctx, path)
		}
		firstResult := make(chan error, 1)
		secondResult := make(chan error, 1)
		go func() { firstResult <- lifecycle.Open(context.Background(), first) }()
		if got := <-entered; got != first {
			t.Fatalf("\nwanted:\n%s first\ngot:\n%s", first, got)
		}
		go func() { secondResult <- lifecycle.Open(context.Background(), second) }()
		select {
		case got := <-entered:
			t.Fatalf("\nwanted:\nsecond handoff waiting\ngot:\nprepared %s", got)
		case <-time.After(20 * time.Millisecond):
		}
		close(continueFirst)
		if err := <-firstResult; err != nil {
			t.Fatalf("opening first target: %v", err)
		}
		if got := <-entered; got != second {
			t.Fatalf("\nwanted:\n%s second\ngot:\n%s", second, got)
		}
		if err := <-secondResult; err != nil {
			t.Fatalf("opening second target: %v", err)
		}
		if lifecycle.Path() != second {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", second, lifecycle.Path())
		}
	})

	t.Run("should retain the old project when the target is already owned", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		held, err := acquireProjectOwnership(target)
		if err != nil {
			t.Fatalf("holding target ownership: %v", err)
		}
		defer held()
		if err := lifecycle.Open(context.Background(), target); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nproject already open\ngot:\n%v", err)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
		if _, err := acquireProjectOwnership(oldPath); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nold project still owned\ngot:\n%v", err)
		}
	})

	t.Run("should retain the old project when it is busy", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		lifecycle.open.resources.Armory = busyArmory{}
		if err := lifecycle.Open(context.Background(), target); !errors.Is(err, ErrProjectBusy) {
			t.Fatalf("\nwanted:\nproject busy\ngot:\n%v", err)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
		if _, err := acquireProjectOwnership(oldPath); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nold project still owned\ngot:\n%v", err)
		}
	})

	t.Run("should retain the old project when intercepts are queued", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		release := holdPendingWebSocket(t, proxy)
		defer release()
		if err := lifecycle.Open(context.Background(), target); !errors.Is(err, ErrProjectBusy) {
			t.Fatalf("\nwanted:\nproject busy\ngot:\n%v", err)
		}
		if lifecycle.Path() != oldPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", oldPath, lifecycle.Path())
		}
		if !proxy.HasPendingCheckpoint() {
			t.Fatal("\nwanted:\nqueued intercept left untouched\ngot:\nqueue changed")
		}
		if _, err := acquireProjectOwnership(oldPath); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\nold project still owned\ngot:\n%v", err)
		}
	})

	t.Run("should flush the old project before publication", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		realFlush := lifecycle.flushOpenProject
		flushed := false
		lifecycle.flushOpenProject = func() error {
			flushed = true
			if lifecycle.Path() != oldPath {
				t.Fatalf("\nwanted:\n%s during flush\ngot:\n%s", oldPath, lifecycle.Path())
			}
			return realFlush()
		}
		if err := lifecycle.Open(context.Background(), target); err != nil {
			t.Fatalf("opening target: %v", err)
		}
		if !flushed {
			t.Fatal("\nwanted:\nflush before publication\ngot:\nno flush")
		}
		if lifecycle.Path() != target {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", target, lifecycle.Path())
		}
	})

	t.Run("should retain the published target when old cleanup fails", func(t *testing.T) {
		lifecycle, _, dir := newTestProjectLifecycle(t)
		oldPath := canonicalProjectPath(t, filepath.Join(dir, "old.marasi"))
		target := canonicalProjectPath(t, filepath.Join(dir, "target.marasi"))
		if err := lifecycle.Open(context.Background(), oldPath); err != nil {
			t.Fatalf("opening old project: %v", err)
		}
		realUnlock := lifecycle.open.unlock
		lifecycle.open.unlock = func() error {
			return errors.Join(realUnlock(), errors.New("release failed"))
		}
		err := lifecycle.Open(context.Background(), target)
		if !errors.Is(err, ErrProjectCleanup) {
			t.Fatalf("\nwanted:\nproject cleanup failure\ngot:\n%v", err)
		}
		if lifecycle.Path() != target {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", target, lifecycle.Path())
		}
		if _, err := acquireProjectOwnership(target); !errors.Is(err, ErrProjectAlreadyOpen) {
			t.Fatalf("\nwanted:\npublished target still owned\ngot:\n%v", err)
		}
	})

	t.Run("should apply extension options before extension startup", func(t *testing.T) {
		called := make(chan string, 1)
		registerEmbedded := func(extension *extensions.Runtime) error {
			extension.LuaState.Register("embedded", func(*lua.State) int {
				called <- extension.Data.Name
				return 0
			})
			return nil
		}
		lifecycle, _, configDir := newTestProjectLifecycle(t, registerEmbedded)
		target := canonicalProjectPath(t, filepath.Join(configDir, "embedded.marasi"))
		conn, err := db.New(target, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("creating project: %v", err)
		}
		repository := db.NewProxyRepo(conn)
		workshop, err := repository.GetExtensionByName("workshop")
		if err != nil {
			t.Fatalf("finding workshop: %v", err)
		}
		if err := repository.UpdateExtensionLuaCodeByUUID(workshop.ID, `function startup() embedded() end`); err != nil {
			t.Fatalf("storing workshop startup: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("closing seeded project: %v", err)
		}

		if err := lifecycle.Open(context.Background(), target); err != nil {
			t.Fatalf("\nwanted:\nproject opened\ngot:\n%v", err)
		}
		select {
		case name := <-called:
			if name != "workshop" {
				t.Fatalf("\nwanted:\nworkshop startup\ngot:\n%s", name)
			}
		default:
			t.Fatal("\nwanted:\nstartup called the embedded function\ngot:\nno call")
		}
	})
}
