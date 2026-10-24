package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// restartListener is a proxy listener at 127.0.0.1:8080 that runs onStop
// when a restart stops it.
type restartListener struct {
	mu     sync.Mutex
	status ListenerStatus
	onStop func()
}

func newRestartListener() *restartListener {
	address := "127.0.0.1:8080"
	return &restartListener{status: ListenerStatus{Status: ListenerActive, ProxyListener: &address}}
}

func (l *restartListener) Status() ListenerStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneListenerStatus(l.status)
}

func (l *restartListener) Start(context.Context, ListenerSettings) (ListenerStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	address := "127.0.0.1:8080"
	l.status = ListenerStatus{Status: ListenerActive, ProxyListener: &address}
	return cloneListenerStatus(l.status), nil
}

func (l *restartListener) Stop(context.Context) (ListenerStatus, error) {
	if l.onStop != nil {
		l.onStop()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status = ListenerStatus{Status: ListenerInactive}
	return cloneListenerStatus(l.status), nil
}

func (l *restartListener) Update(context.Context, ListenerSettings) (ListenerStatus, error) {
	panic("unexpected listener update")
}
func (l *restartListener) Shutdown() error { return nil }
func (l *restartListener) controlEvents() (*eventBroadcaster, io.Writer) {
	return nil, nil
}

// newRestartServer is an instance with an open project and a proxy
// listener whose successors fail the test if one starts.
func newRestartServer(t *testing.T, listener *restartListener) (*Server, *ProjectLifecycle) {
	t.Helper()
	lifecycle, _, dir := newTestProjectLifecycle(t)
	current := canonicalProjectPath(t, filepath.Join(dir, "current.marasi"))
	if err := lifecycle.Open(context.Background(), current); err != nil {
		t.Fatalf("opening current project: %v", err)
	}
	server := NewServer(lifecycle.proxy, listener, lifecycle, func() {}, "01.10.2026", "work", current)
	t.Cleanup(server.Close)
	server.EnableRestart(func(context.Context, Handover) (Successor, error) {
		t.Error("a successor started")
		return Successor{}, &RestartError{Err: context.Canceled}
	})
	return server, lifecycle
}

func TestServiceRestartRefusals(t *testing.T) {
	t.Run("should refuse while an install is running", func(t *testing.T) {
		github := newInstallGitHub(t)
		github.publish("18.10.2026", "new build 18.10.2026")
		release := github.hold()
		defer release()
		server, _ := newRestartServer(t, newRestartListener())
		server.SetUpdates(newInstallUpdates(t, t.TempDir(), installedBuildFile(t), github))
		if response := requestUpdate(server, http.MethodPost, "/update/install", ""); response.Code != http.StatusAccepted {
			t.Fatalf("\nwanted:\n202\ngot:\n%d %s", response.Code, response.Body.String())
		}
		github.waitForDownload(t)

		response := requestUpdate(server, http.MethodPost, "/service/restart", "")

		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"update_in_progress"`) {
			t.Fatalf("\nwanted:\n409 update_in_progress\ngot:\n%d %s", response.Code, response.Body.String())
		}
		if state := server.installer.current(); state.State != installRunning {
			t.Fatalf("\nwanted:\ninstall still running\ngot:\n%+v", state)
		}
	})

	t.Run("should keep an item held after the check unless forced", func(t *testing.T) {
		listener := newRestartListener()
		server, lifecycle := newRestartServer(t, listener)
		var holdDone <-chan error
		listener.onStop = func() {
			_, holdDone = startCheckpointWebSocketHold(t, lifecycle.proxy)
			waitForCheckpointItems(t, lifecycle.proxy, 1)
		}

		response := requestUpdate(server, http.MethodPost, "/service/restart", "")

		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"checkpoint_items_held"`) {
			t.Fatalf("\nwanted:\n409 checkpoint_items_held\ngot:\n%d %s", response.Code, response.Body.String())
		}
		if items := lifecycle.proxy.CheckpointItems(); len(items) != 1 {
			t.Fatalf("\nwanted:\nthe item still held\ngot:\n%d items", len(items))
		}
		if status := listener.Status(); status.ProxyListener == nil {
			t.Fatal("\nwanted:\nproxy listener restarted\ngot:\nstopped")
		}
		if lifecycle.Path() == "" {
			t.Fatal("\nwanted:\nproject reopened\ngot:\nnone open")
		}
		server.dropPendingCheckpoint()
		drainCheckpointHold(t, holdDone)
	})
}

func TestServiceRestartHandover(t *testing.T) {
	t.Run("should let the old instance exit when a response arrives after the handover", func(t *testing.T) {
		harness := newHungOriginHarness(t, false)
		harness.server.EnableRestart(func(context.Context, Handover) (Successor, error) {
			return Successor{Version: "21.10.2026"}, nil
		})
		client := harness.sendHung(t, "/hung")

		response := requestUpdate(harness.server, http.MethodPost, "/service/restart", "")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		harness.release()
		client.SetReadDeadline(time.Now().Add(5 * time.Second))
		late, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatalf("reading the late response: %v", err)
		}
		body, _ := io.ReadAll(late.Body)
		if late.StatusCode != http.StatusOK || string(body) != "late answer" {
			t.Fatalf("\nwanted:\n200 late answer\ngot:\n%d %q", late.StatusCode, body)
		}
		host := strings.TrimPrefix(harness.origin.URL, "http://")
		fmt.Fprintf(client, "GET %s/after HTTP/1.1\r\nHost: %s\r\n\r\n", harness.origin.URL, host)
		if after, err := http.ReadResponse(bufio.NewReader(client), nil); err == nil {
			t.Fatalf("\nwanted:\nthe connection closed for a request after the handover\ngot:\n%d", after.StatusCode)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		closed := make(chan error, 1)
		go func() { closed <- harness.lifecycle.proxy.CloseTransportContext(ctx) }()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			// Let the stuck response through so the test can end.
			gate := harness.lifecycle.gate
			gate.mu.Lock()
			gate.blocked = false
			gate.signal()
			gate.mu.Unlock()
			<-closed
			t.Fatal("the proxy of the handed-over instance did not close")
		}
	})
}
