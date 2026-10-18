package service

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
	server.EnableRestart(func(context.Context, string, *string) (Successor, error) {
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
