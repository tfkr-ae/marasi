package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
)

type listenerTestProxy struct {
	mu           sync.Mutex
	served       chan net.Listener
	cleanupCalls int
	cleanupErr   error
	closeCalls   int
	current      net.Listener
	bind         func(string, string) (net.Listener, error)
	serve        func(net.Listener) error
}

func newListenerTestProxy() *listenerTestProxy {
	return &listenerTestProxy{served: make(chan net.Listener, 16)}
}

func (p *listenerTestProxy) GetListener(address, port string) (net.Listener, error) {
	if p.bind != nil {
		return p.bind(address, port)
	}
	return net.Listen("tcp", net.JoinHostPort(address, port))
}

func (p *listenerTestProxy) Serve(listener net.Listener) error {
	p.mu.Lock()
	p.current = listener
	p.mu.Unlock()
	p.served <- listener
	if p.serve != nil {
		return p.serve(listener)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		connection.Close()
	}
}

func (p *listenerTestProxy) Close() error {
	p.mu.Lock()
	p.closeCalls++
	listener := p.current
	p.mu.Unlock()
	if listener == nil {
		return nil
	}
	return listener.Close()
}

func (p *listenerTestProxy) CloseWebSocketsAndFlush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupCalls++
	return p.cleanupErr
}

func (p *listenerTestProxy) cleanupCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleanupCalls
}

func (p *listenerTestProxy) closeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeCalls
}

func listenerSettings(address string, port uint16) ListenerSettings {
	return ListenerSettings{Address: &address, Port: &port}
}

func statusAddress(t *testing.T, status ListenerStatus) string {
	t.Helper()
	if status.ProxyListener == nil {
		t.Fatal("\nwanted:\nproxy listener address\ngot:\nnull")
	}
	return *status.ProxyListener
}

func TestListenerLifecycle(t *testing.T) {
	t.Run("should restart a real proxy without closing its open project", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		proxy, err := marasi.New(marasi.WithLogger(logger))
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
		if err != nil {
			t.Fatalf("opening project: %v", err)
		}
		repository := db.NewProxyRepo(database)
		if err := proxy.WithOptions(marasi.WithDefaultRepositories(repository)); err != nil {
			t.Fatalf("configuring proxy repositories: %v", err)
		}
		lifecycle := NewListenerLifecycle(proxy, io.Discard)
		t.Cleanup(func() {
			if err := lifecycle.Shutdown(); err != nil {
				t.Fatalf("shutting down lifecycle: %v", err)
			}
		})

		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		endpoint := statusAddress(t, started)
		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		if _, err := repository.GetExtensions(); err != nil {
			t.Fatalf("reading the open project after listener stop: %v", err)
		}
		restarted, err := lifecycle.Start(context.Background(), ListenerSettings{})
		if err != nil {
			t.Fatalf("restarting listener: %v", err)
		}
		if got := statusAddress(t, restarted); got != endpoint {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", endpoint, got)
		}
		connection, err := net.DialTimeout("tcp", endpoint, time.Second)
		if err != nil {
			t.Fatalf("dialing restarted proxy listener: %v", err)
		}
		connection.Close()
	})

	t.Run("should stop and restart on the retained assigned endpoint", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })

		initial := lifecycle.Status()
		if initial.Status != ListenerInactive || initial.ProxyListener != nil {
			t.Fatalf("\nwanted:\ninactive status\ngot:\n%+v", initial)
		}

		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		address := statusAddress(t, started)
		_, port, splitErr := net.SplitHostPort(address)
		if splitErr != nil || port == "0" {
			t.Fatalf("\nwanted:\nassigned endpoint\ngot:\n%s (%v)", address, splitErr)
		}

		stopped, err := lifecycle.Stop(context.Background())
		if err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		if stopped.Status != ListenerInactive || stopped.ProxyListener != nil {
			t.Fatalf("\nwanted:\ninactive status\ngot:\n%+v", stopped)
		}
		if proxy.cleanupCount() != 1 {
			t.Fatalf("\nwanted:\n1 WebSocket cleanup\ngot:\n%d", proxy.cleanupCount())
		}
		if proxy.closeCount() != 0 {
			t.Fatalf("\nwanted:\nopen proxy and project\ngot:\n%d proxy closes", proxy.closeCount())
		}
		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping inactive listener: %v", err)
		}
		if proxy.cleanupCount() != 1 {
			t.Fatalf("\nwanted:\nidempotent stop without cleanup\ngot:\n%d cleanups", proxy.cleanupCount())
		}
		if connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond); err == nil {
			connection.Close()
			t.Fatal("\nwanted:\nclosed listener\ngot:\naccepted connection")
		}

		restarted, err := lifecycle.Start(context.Background(), ListenerSettings{})
		if err != nil {
			t.Fatalf("restarting listener: %v", err)
		}
		if got := statusAddress(t, restarted); got != address {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", address, got)
		}
		if host, gotPort, err := net.SplitHostPort(statusAddress(t, lifecycle.Status())); err != nil || host != "127.0.0.1" || gotPort != strconv.Itoa(mustPort(t, port)) {
			t.Fatalf("\nwanted:\nretained active endpoint\ngot:\n%+v (%v)", lifecycle.Status(), err)
		}
		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping restarted listener: %v", err)
		}
		host := "127.0.0.1"
		partial, err := lifecycle.Start(context.Background(), ListenerSettings{Address: &host})
		if err != nil {
			t.Fatalf("starting listener with a partial override: %v", err)
		}
		if got := statusAddress(t, partial); got != address {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", address, got)
		}
	})

	t.Run("should enforce states and apply partial updates", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })

		if _, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", 0)); !errors.Is(err, ErrListenerInactive) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrListenerInactive, err)
		}
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		if _, err := lifecycle.Start(context.Background(), ListenerSettings{}); !errors.Is(err, ErrListenerAlreadyActive) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrListenerAlreadyActive, err)
		}

		before := lifecycle.Status()
		address := "127.0.0.1"
		unchanged, err := lifecycle.Update(context.Background(), ListenerSettings{Address: &address})
		if err != nil {
			t.Fatalf("updating unchanged listener: %v", err)
		}
		if statusAddress(t, unchanged) != statusAddress(t, before) || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\nunchanged listener without cleanup\ngot:\n%+v and %d cleanups", unchanged, proxy.cleanupCount())
		}

		port := uint16(0)
		updated, err := lifecycle.Update(context.Background(), ListenerSettings{Port: &port})
		if err != nil {
			t.Fatalf("updating listener port: %v", err)
		}
		if statusAddress(t, updated) == statusAddress(t, before) {
			t.Fatalf("\nwanted:\nreplacement assigned endpoint\ngot:\n%s", statusAddress(t, updated))
		}
		if proxy.cleanupCount() != 1 {
			t.Fatalf("\nwanted:\n1 WebSocket cleanup\ngot:\n%d", proxy.cleanupCount())
		}
	})

	t.Run("should preserve the active listener when replacement binding fails", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		oldAddress := statusAddress(t, started)
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("occupying endpoint: %v", err)
		}
		defer occupied.Close()
		port := uint16(occupied.Addr().(*net.TCPAddr).Port)

		status, err := lifecycle.Update(context.Background(), ListenerSettings{Port: &port})
		if !errors.Is(err, ErrListenerUnavailable) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrListenerUnavailable, err)
		}
		if statusAddress(t, status) != oldAddress || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\noriginal active listener without cleanup\ngot:\n%+v and %d cleanups", status, proxy.cleanupCount())
		}
		connection, err := net.DialTimeout("tcp", oldAddress, time.Second)
		if err != nil {
			t.Fatalf("dialing preserved listener: %v", err)
		}
		connection.Close()
	})

	t.Run("should move forward after replacement cleanup fails", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		oldAddress := statusAddress(t, started)
		proxy.cleanupErr = errors.New("flush failed")
		port := uint16(0)

		updated, err := lifecycle.Update(context.Background(), ListenerSettings{Port: &port})
		if !errors.Is(err, ErrListenerCleanup) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrListenerCleanup, err)
		}
		newAddress := statusAddress(t, updated)
		if newAddress == oldAddress || updated.Status != ListenerActive {
			t.Fatalf("\nwanted:\nactive replacement\ngot:\n%+v", updated)
		}
		connection, err := net.DialTimeout("tcp", newAddress, time.Second)
		if err != nil {
			t.Fatalf("dialing replacement listener: %v", err)
		}
		connection.Close()
		stopped, err := lifecycle.Stop(context.Background())
		if !errors.Is(err, ErrListenerCleanup) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrListenerCleanup, err)
		}
		if stopped.Status != ListenerInactive || stopped.ProxyListener != nil {
			t.Fatalf("\nwanted:\ninactive status after cleanup failure\ngot:\n%+v", stopped)
		}
	})

	t.Run("should recover after an unexpected serving failure", func(t *testing.T) {
		proxy := newListenerTestProxy()
		var log bytes.Buffer
		lifecycle := newListenerLifecycle(proxy, &log)
		t.Cleanup(func() { lifecycle.Shutdown() })
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		served := <-proxy.served
		if err := served.Close(); err != nil {
			t.Fatalf("failing listener: %v", err)
		}
		waitForListenerStatus(t, lifecycle, ListenerInactive)
		if proxy.cleanupCount() != 1 || !bytes.Contains(log.Bytes(), []byte("proxy listener stopped unexpectedly")) {
			t.Fatalf("\nwanted:\nfailure log and WebSocket cleanup\ngot:\n%s and %d cleanups", log.String(), proxy.cleanupCount())
		}
		restarted, err := lifecycle.Start(context.Background(), ListenerSettings{})
		if err != nil {
			t.Fatalf("restarting listener: %v", err)
		}
		if statusAddress(t, restarted) != statusAddress(t, started) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", statusAddress(t, started), statusAddress(t, restarted))
		}
	})

	t.Run("should cancel queued work but finish a transition after it starts", func(t *testing.T) {
		proxy := newListenerTestProxy()
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		proxy.bind = func(address, port string) (net.Listener, error) {
			entered <- struct{}{}
			<-release
			return net.Listen("tcp", net.JoinHostPort(address, port))
		}
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })

		firstContext, cancelFirst := context.WithCancel(context.Background())
		firstResult := make(chan error, 1)
		go func() {
			_, err := lifecycle.Start(firstContext, listenerSettings("127.0.0.1", 0))
			firstResult <- err
		}()
		<-entered
		cancelFirst()

		queuedContext, cancelQueued := context.WithCancel(context.Background())
		queuedResult := make(chan error, 1)
		go func() {
			_, err := lifecycle.Update(queuedContext, ListenerSettings{})
			queuedResult <- err
		}()
		time.Sleep(10 * time.Millisecond)
		cancelQueued()
		select {
		case err := <-queuedResult:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nprompt queued cancellation\ngot:\ntimeout")
		}
		close(release)

		if err := <-firstResult; err != nil {
			t.Fatalf("\nwanted:\ncompleted started transition\ngot:\n%v", err)
		}
	})

	t.Run("should complete accepted transitions in order", func(t *testing.T) {
		proxy := newListenerTestProxy()
		var mu sync.Mutex
		var boundPorts []string
		blocked := make(chan struct{})
		release := make(chan struct{})
		proxy.bind = func(address, port string) (net.Listener, error) {
			mu.Lock()
			boundPorts = append(boundPorts, port)
			call := len(boundPorts)
			mu.Unlock()
			if call == 2 {
				close(blocked)
				<-release
			}
			return net.Listen("tcp", net.JoinHostPort(address, port))
		}
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		firstPort := availablePort(t)
		secondPort := availablePort(t)
		firstResult := make(chan error, 1)
		secondResult := make(chan error, 1)
		go func() {
			_, err := lifecycle.Update(context.Background(), ListenerSettings{Port: &firstPort})
			firstResult <- err
		}()
		<-blocked
		go func() {
			_, err := lifecycle.Update(context.Background(), ListenerSettings{Port: &secondPort})
			secondResult <- err
		}()
		time.Sleep(10 * time.Millisecond)
		close(release)
		if err := <-firstResult; err != nil {
			t.Fatalf("first update: %v", err)
		}
		if err := <-secondResult; err != nil {
			t.Fatalf("second update: %v", err)
		}

		mu.Lock()
		gotOrder := append([]string(nil), boundPorts...)
		mu.Unlock()
		wantOrder := []string{"0", strconv.Itoa(int(firstPort)), strconv.Itoa(int(secondPort))}
		if len(gotOrder) != len(wantOrder) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantOrder, gotOrder)
		}
		for index := range wantOrder {
			if gotOrder[index] != wantOrder[index] {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantOrder, gotOrder)
			}
		}
		_, finalPort, err := net.SplitHostPort(statusAddress(t, lifecycle.Status()))
		if err != nil || finalPort != strconv.Itoa(int(secondPort)) {
			t.Fatalf("\nwanted:\nfinal port %d\ngot:\n%s (%v)", secondPort, finalPort, err)
		}
	})

	t.Run("should close the proxy only during service shutdown", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}

		if err := lifecycle.Shutdown(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("shutting down listener lifecycle: %v", err)
		}
		if proxy.closeCount() != 1 || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\n1 proxy close and no separate WebSocket cleanup\ngot:\n%d closes and %d cleanups", proxy.closeCount(), proxy.cleanupCount())
		}
		if status := lifecycle.Status(); status.Status != ListenerInactive || status.ProxyListener != nil {
			t.Fatalf("\nwanted:\ninactive status\ngot:\n%+v", status)
		}
	})
}

func availablePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding available port: %v", err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing available port: %v", err)
	}
	return port
}

func waitForListenerStatus(t *testing.T, lifecycle ListenerLifecycle, want ListenerState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for lifecycle.Status().Status != want {
		if time.Now().After(deadline) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%+v", want, lifecycle.Status())
		}
		time.Sleep(time.Millisecond)
	}
}

func mustPort(t *testing.T, value string) int {
	t.Helper()
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("parsing port: %v", err)
	}
	return port
}
