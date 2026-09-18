package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

type listenerTestProxy struct {
	mu                   sync.Mutex
	served               chan net.Listener
	cleanupCalls         int
	cleanupErr           error
	closeWebSocketCalls  int
	closeWebSocketCode   int
	closeWebSocketReason string
	closeCalls           int
	current              net.Listener
	bind                 func(string, string) (net.Listener, error)
	serve                func(net.Listener) error
}

type closeErrorListener struct {
	net.Listener
	err error
}

func (l closeErrorListener) Close() error {
	_ = l.Listener.Close()
	return l.err
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

func (p *listenerTestProxy) CloseTransport() error {
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

func (p *listenerTestProxy) CloseWebSockets(code int, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeWebSocketCalls++
	p.closeWebSocketCode = code
	p.closeWebSocketReason = reason
	return p.cleanupErr
}

func (p *listenerTestProxy) webSocketClose() (int, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeWebSocketCalls, p.closeWebSocketCode, p.closeWebSocketReason
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

func endpointSettings(t *testing.T, status ListenerStatus) ListenerSettings {
	t.Helper()
	address, portText, err := net.SplitHostPort(statusAddress(t, status))
	if err != nil {
		t.Fatalf("splitting listener endpoint: %v", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatalf("parsing listener port: %v", err)
	}
	return listenerSettings(address, uint16(port))
}

func statusAddress(t *testing.T, status ListenerStatus) string {
	t.Helper()
	if status.ProxyListener == nil {
		t.Fatal("\nwanted:\nproxy listener address\ngot:\nnull")
	}
	return *status.ProxyListener
}

func TestListenerLifecycle(t *testing.T) {
	t.Run("should close WebSockets without flushing and ignore close errors", func(t *testing.T) {
		proxy := newListenerTestProxy()
		closeErr := errors.New("closing WebSockets failed")
		proxy.cleanupErr = closeErr
		var log bytes.Buffer
		lifecycle := newListenerLifecycle(proxy, &log)
		t.Cleanup(func() { lifecycle.Shutdown() })
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}

		status, err := lifecycle.Stop(context.Background())
		if err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		calls, code, reason := proxy.webSocketClose()
		if calls != 1 || code != marasiws.CloseGoingAway || reason != "" {
			t.Fatalf("\nwanted:\n1 WebSocket close with %d and no reason\ngot:\n%d closes with %d and %q", marasiws.CloseGoingAway, calls, code, reason)
		}
		if proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\nno flushing cleanup\ngot:\n%d cleanups", proxy.cleanupCount())
		}
		if status.Status != ListenerInactive || status.ProxyListener != nil {
			t.Fatalf("\nwanted:\ninactive status\ngot:\n%+v", status)
		}
		if !bytes.Contains(log.Bytes(), []byte(closeErr.Error())) {
			t.Fatalf("\nwanted:\nWebSocket close error in log\ngot:\n%s", log.String())
		}
	})

	t.Run("should close WebSockets without flushing during update", func(t *testing.T) {
		proxy := newListenerTestProxy()
		closeErr := errors.New("closing WebSockets failed")
		proxy.cleanupErr = closeErr
		var log bytes.Buffer
		lifecycle := newListenerLifecycle(proxy, &log)
		t.Cleanup(func() { lifecycle.Shutdown() })
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		oldAddress := statusAddress(t, started)

		updated, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("updating listener: %v", err)
		}
		calls, code, reason := proxy.webSocketClose()
		if calls != 1 || code != marasiws.CloseGoingAway || reason != "" {
			t.Fatalf("\nwanted:\n1 WebSocket close with %d and no reason\ngot:\n%d closes with %d and %q", marasiws.CloseGoingAway, calls, code, reason)
		}
		if proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\nno flushing cleanup\ngot:\n%d cleanups", proxy.cleanupCount())
		}
		if updated.Status != ListenerActive || statusAddress(t, updated) == oldAddress {
			t.Fatalf("\nwanted:\nactive replacement\ngot:\n%+v", updated)
		}
		if !bytes.Contains(log.Bytes(), []byte(closeErr.Error())) {
			t.Fatalf("\nwanted:\nWebSocket close error in log\ngot:\n%s", log.String())
		}
	})

	t.Run("should succeed when closing the listening socket returns an error", func(t *testing.T) {
		proxy := newListenerTestProxy()
		closeErr := errors.New("closing listener failed")
		proxy.bind = func(address, port string) (net.Listener, error) {
			listener, err := net.Listen("tcp", net.JoinHostPort(address, port))
			if err != nil {
				return nil, err
			}
			return closeErrorListener{Listener: listener, err: closeErr}, nil
		}
		var log bytes.Buffer
		lifecycle := newListenerLifecycle(proxy, &log)
		t.Cleanup(func() { lifecycle.Shutdown() })
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}

		status, err := lifecycle.Stop(context.Background())
		if err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		if status.Status != ListenerInactive || status.ProxyListener != nil {
			t.Fatalf("\nwanted:\ninactive status\ngot:\n%+v", status)
		}
		if !bytes.Contains(log.Bytes(), []byte(closeErr.Error())) {
			t.Fatalf("\nwanted:\nlistener close error in log\ngot:\n%s", log.String())
		}
	})

	t.Run("should wait for real WebSockets to close before publishing stopped", func(t *testing.T) {
		originClose := make(chan marasiws.Frame, 1)
		releaseOrigin := make(chan struct{})
		origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			connection, buffered, err := response.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijacking origin connection: %v", err)
				return
			}
			defer connection.Close()
			key := request.Header.Get("Sec-WebSocket-Key")
			accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			_, err = fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
			if err != nil {
				t.Errorf("writing origin handshake: %v", err)
				return
			}
			if err := buffered.Flush(); err != nil {
				t.Errorf("flushing origin handshake: %v", err)
				return
			}
			frame, err := marasiws.ReadFrame(buffered)
			if err != nil {
				t.Errorf("reading origin close: %v", err)
				return
			}
			originClose <- frame
			<-releaseOrigin
		}))
		defer origin.Close()

		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
		if err != nil {
			t.Fatalf("opening project: %v", err)
		}
		repository := db.NewProxyRepo(database)
		extensions, err := repository.GetExtensions()
		if err != nil {
			t.Fatalf("getting default extensions: %v", err)
		}
		requestIDs := make(chan domain.ProxyRequest, 1)
		proxy, err := marasi.New(
			marasi.WithLogger(logger),
			marasi.WithDefaultRepositories(repository),
			marasi.WithExtensions(extensions),
			marasi.WithRequestHandler(func(request domain.ProxyRequest) error {
				requestIDs <- request
				return nil
			}),
			marasi.WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
			marasi.WithLogHandler(func(domain.Log) error { return nil }),
			marasi.WithBasePipeline(),
			marasi.WithDefaultModifierPipeline(),
		)
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		lifecycle := newListenerLifecycle(proxy, io.Discard).(*listenerLifecycle)
		t.Cleanup(func() {
			close(releaseOrigin)
			if err := lifecycle.Shutdown(); err != nil {
				t.Fatalf("shutting down lifecycle: %v", err)
			}
		})
		subscriber := lifecycle.events.subscribe()
		defer lifecycle.events.unsubscribe(subscriber)
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		<-subscriber.events

		client, err := net.DialTimeout("tcp", statusAddress(t, started), time.Second)
		if err != nil {
			t.Fatalf("dialing proxy: %v", err)
		}
		defer client.Close()
		if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("setting client deadline: %v", err)
		}
		buffered := bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client))
		originAddress := origin.Listener.Addr().String()
		_, err = fmt.Fprintf(buffered, "GET %s/socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", origin.URL, originAddress)
		if err != nil {
			t.Fatalf("writing upgrade request: %v", err)
		}
		if err := buffered.Flush(); err != nil {
			t.Fatalf("flushing upgrade request: %v", err)
		}
		response, err := http.ReadResponse(buffered.Reader, &http.Request{Method: http.MethodGet})
		if err != nil {
			t.Fatalf("reading upgrade response: %v", err)
		}
		if response.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("\nwanted:\n101 Switching Protocols\ngot:\n%s", response.Status)
		}
		request := <-requestIDs
		deadline := time.Now().Add(time.Second)
		for {
			if _, exists := proxy.WebSocketRegistry.GetByRequestID(request.ID); exists {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for live WebSocket registration")
			}
			time.Sleep(time.Millisecond)
		}

		stopResult := make(chan error, 1)
		go func() {
			_, stopErr := lifecycle.Stop(context.Background())
			stopResult <- stopErr
		}()
		upstreamFrame := <-originClose
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno stopped event before WebSocket session ends\ngot:\n%s", event.name)
		default:
		}
		select {
		case err := <-stopResult:
			if err != nil {
				t.Fatalf("stopping listener: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("\nwanted:\nStop to finish through the bounded WebSocket close fallback\ngot:\ntimeout")
		}
		clientFrame, err := marasiws.ReadFrame(buffered)
		if err != nil {
			t.Fatalf("reading client close: %v", err)
		}
		for peer, frame := range map[string]marasiws.Frame{"client": clientFrame, "upstream": upstreamFrame} {
			code, reason, err := marasiws.ParseClosePayload(frame.Payload)
			if err != nil || frame.Opcode != marasiws.OpClose || code != marasiws.CloseGoingAway || reason != "" {
				t.Fatalf("\nwanted:\n%s close 1001 with no reason\ngot:\nopcode %d, code %d, reason %q, error %v", peer, frame.Opcode, code, reason, err)
			}
		}
		event := <-subscriber.events
		if event.name != "listener.stopped" || !bytes.Contains(event.data, []byte(`"reason":"requested"`)) {
			t.Fatalf("\nwanted:\nrequested listener stop event\ngot:\n%s %s", event.name, event.data)
		}
	})

	t.Run("should wait for real WebSockets to close before serving a replacement", func(t *testing.T) {
		originClose := make(chan marasiws.Frame, 1)
		releaseOrigin := make(chan struct{})
		origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			connection, buffered, err := response.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijacking origin connection: %v", err)
				return
			}
			defer connection.Close()
			key := request.Header.Get("Sec-WebSocket-Key")
			accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			_, err = fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
			if err != nil {
				t.Errorf("writing origin handshake: %v", err)
				return
			}
			if err := buffered.Flush(); err != nil {
				t.Errorf("flushing origin handshake: %v", err)
				return
			}
			frame, err := marasiws.ReadFrame(buffered)
			if err != nil {
				t.Errorf("reading origin close: %v", err)
				return
			}
			originClose <- frame
			<-releaseOrigin
		}))
		defer origin.Close()

		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
		if err != nil {
			t.Fatalf("opening project: %v", err)
		}
		repository := db.NewProxyRepo(database)
		extensions, err := repository.GetExtensions()
		if err != nil {
			t.Fatalf("getting default extensions: %v", err)
		}
		requestIDs := make(chan domain.ProxyRequest, 1)
		proxy, err := marasi.New(
			marasi.WithLogger(logger),
			marasi.WithDefaultRepositories(repository),
			marasi.WithExtensions(extensions),
			marasi.WithRequestHandler(func(request domain.ProxyRequest) error {
				requestIDs <- request
				return nil
			}),
			marasi.WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
			marasi.WithLogHandler(func(domain.Log) error { return nil }),
			marasi.WithBasePipeline(),
			marasi.WithDefaultModifierPipeline(),
		)
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		lifecycle := newListenerLifecycle(proxy, io.Discard).(*listenerLifecycle)
		t.Cleanup(func() {
			close(releaseOrigin)
			if err := lifecycle.Shutdown(); err != nil {
				t.Fatalf("shutting down lifecycle: %v", err)
			}
		})
		subscriber := lifecycle.events.subscribe()
		defer lifecycle.events.unsubscribe(subscriber)
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		<-subscriber.events

		client, err := net.DialTimeout("tcp", statusAddress(t, started), time.Second)
		if err != nil {
			t.Fatalf("dialing proxy: %v", err)
		}
		defer client.Close()
		if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("setting client deadline: %v", err)
		}
		buffered := bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client))
		originAddress := origin.Listener.Addr().String()
		_, err = fmt.Fprintf(buffered, "GET %s/socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", origin.URL, originAddress)
		if err != nil {
			t.Fatalf("writing upgrade request: %v", err)
		}
		if err := buffered.Flush(); err != nil {
			t.Fatalf("flushing upgrade request: %v", err)
		}
		response, err := http.ReadResponse(buffered.Reader, &http.Request{Method: http.MethodGet})
		if err != nil {
			t.Fatalf("reading upgrade response: %v", err)
		}
		if response.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("\nwanted:\n101 Switching Protocols\ngot:\n%s", response.Status)
		}
		request := <-requestIDs
		deadline := time.Now().Add(time.Second)
		for {
			if _, exists := proxy.WebSocketRegistry.GetByRequestID(request.ID); exists {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for live WebSocket registration")
			}
			time.Sleep(time.Millisecond)
		}

		updateResult := make(chan struct {
			status ListenerStatus
			err    error
		}, 1)
		go func() {
			status, updateErr := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", 0))
			updateResult <- struct {
				status ListenerStatus
				err    error
			}{status, updateErr}
		}()
		upstreamFrame := <-originClose
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno updated event before WebSocket session ends\ngot:\n%s", event.name)
		default:
		}
		select {
		case result := <-updateResult:
			if result.err != nil {
				t.Fatalf("updating listener: %v", result.err)
			}
			if result.status.Status != ListenerActive || statusAddress(t, result.status) == statusAddress(t, started) {
				t.Fatalf("\nwanted:\nactive replacement\ngot:\n%+v", result.status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("\nwanted:\nUpdate to finish through the bounded WebSocket close fallback\ngot:\ntimeout")
		}
		clientFrame, err := marasiws.ReadFrame(buffered)
		if err != nil {
			t.Fatalf("reading client close: %v", err)
		}
		for peer, frame := range map[string]marasiws.Frame{"client": clientFrame, "upstream": upstreamFrame} {
			code, reason, err := marasiws.ParseClosePayload(frame.Payload)
			if err != nil || frame.Opcode != marasiws.OpClose || code != marasiws.CloseGoingAway || reason != "" {
				t.Fatalf("\nwanted:\n%s close 1001 with no reason\ngot:\nopcode %d, code %d, reason %q, error %v", peer, frame.Opcode, code, reason, err)
			}
		}
		event := <-subscriber.events
		if event.name != "listener.updated" {
			t.Fatalf("\nwanted:\nlistener updated event\ngot:\n%s %s", event.name, event.data)
		}
		if connection, err := net.DialTimeout("tcp", statusAddress(t, started), 50*time.Millisecond); err == nil {
			connection.Close()
			t.Fatal("\nwanted:\nold listener closed after Update\ngot:\naccepted connection")
		}
		connection, err := net.DialTimeout("tcp", statusAddress(t, lifecycle.Status()), time.Second)
		if err != nil {
			t.Fatalf("dialing replacement listener: %v", err)
		}
		connection.Close()
	})

	t.Run("should let accepted HTTP traffic continue without closing the project", func(t *testing.T) {
		requestStarted := make(chan struct{})
		releaseResponse := make(chan struct{})
		origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/accepted-before-stop" {
				close(requestStarted)
				<-releaseResponse
			}
			_, _ = response.Write([]byte(request.URL.Path))
		}))
		defer origin.Close()

		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
		if err != nil {
			t.Fatalf("opening project: %v", err)
		}
		repository := db.NewProxyRepo(database)
		extensions, err := repository.GetExtensions()
		if err != nil {
			t.Fatalf("getting default extensions: %v", err)
		}
		proxy, err := marasi.New(
			marasi.WithLogger(logger),
			marasi.WithDefaultRepositories(repository),
			marasi.WithExtensions(extensions),
			marasi.WithBasePipeline(),
			marasi.WithDefaultModifierPipeline(),
		)
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
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
		proxyURL := &url.URL{Scheme: "http", Host: statusAddress(t, started)}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

		firstResult := make(chan error, 1)
		go func() {
			response, requestErr := client.Get(origin.URL + "/accepted-before-stop")
			if requestErr != nil {
				firstResult <- requestErr
				return
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if readErr == nil && string(body) != "/accepted-before-stop" {
				readErr = fmt.Errorf("unexpected response body %q", body)
			}
			firstResult <- readErr
		}()
		<-requestStarted

		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		if connection, err := net.DialTimeout("tcp", statusAddress(t, started), 50*time.Millisecond); err == nil {
			connection.Close()
			t.Fatal("\nwanted:\nnew TCP connections rejected after Stop\ngot:\naccepted connection")
		}
		close(releaseResponse)
		if err := <-firstResult; err != nil {
			t.Fatalf("finishing accepted request: %v", err)
		}

		response, err := client.Get(origin.URL + "/keep-alive-after-stop")
		if err != nil {
			t.Fatalf("sending request on accepted keep-alive connection: %v", err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "/keep-alive-after-stop" {
			t.Fatalf("reading keep-alive response: %q, %v", body, err)
		}

		deadline := time.Now().Add(5 * time.Second)
		for {
			summaries, summaryErr := repository.GetRequestResponseSummary()
			if summaryErr == nil && len(summaries) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("waiting for continued traffic persistence: %d rows, %v", len(summaries), summaryErr)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

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

		_, err = lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		if _, err := repository.GetExtensions(); err != nil {
			t.Fatalf("reading the open project after listener stop: %v", err)
		}
		restarted, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("restarting listener: %v", err)
		}
		connection, err := net.DialTimeout("tcp", statusAddress(t, restarted), time.Second)
		if err != nil {
			t.Fatalf("dialing restarted proxy listener: %v", err)
		}
		connection.Close()
	})

	t.Run("should require a complete endpoint after stop", func(t *testing.T) {
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
		closeCalls, _, _ := proxy.webSocketClose()
		if closeCalls != 1 || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\n1 WebSocket close without flushing\ngot:\n%d closes and %d cleanups", closeCalls, proxy.cleanupCount())
		}
		if proxy.closeCount() != 0 {
			t.Fatalf("\nwanted:\nopen proxy and project\ngot:\n%d proxy closes", proxy.closeCount())
		}
		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping inactive listener: %v", err)
		}
		closeCalls, _, _ = proxy.webSocketClose()
		if closeCalls != 1 || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\nidempotent stop without cleanup\ngot:\n%d closes and %d cleanups", closeCalls, proxy.cleanupCount())
		}
		if connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond); err == nil {
			connection.Close()
			t.Fatal("\nwanted:\nclosed listener\ngot:\naccepted connection")
		}

		host := "127.0.0.1"
		for _, settings := range []ListenerSettings{{}, {Address: &host}} {
			status, err := lifecycle.Start(context.Background(), settings)
			if !errors.Is(err, ErrListenerUnavailable) || status.Status != ListenerInactive || status.ProxyListener != nil {
				t.Fatalf("\nwanted:\ninactive status and %v\ngot:\n%+v and %v", ErrListenerUnavailable, status, err)
			}
		}
		restarted, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", uint16(mustPort(t, port))))
		if err != nil {
			t.Fatalf("restarting listener with a complete endpoint: %v", err)
		}
		if got := statusAddress(t, restarted); got != address {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", address, got)
		}
	})

	t.Run("should enforce states and require complete updates", func(t *testing.T) {
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
		incomplete, err := lifecycle.Update(context.Background(), ListenerSettings{Address: &address})
		if !errors.Is(err, ErrListenerUnavailable) || statusAddress(t, incomplete) != statusAddress(t, before) {
			t.Fatalf("\nwanted:\nactive status and %v\ngot:\n%+v and %v", ErrListenerUnavailable, incomplete, err)
		}
		unchanged, err := lifecycle.Update(context.Background(), endpointSettings(t, before))
		if err != nil {
			t.Fatalf("updating with the unchanged endpoint: %v", err)
		}
		if statusAddress(t, unchanged) != statusAddress(t, before) || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\nunchanged listener without cleanup\ngot:\n%+v and %d cleanups", unchanged, proxy.cleanupCount())
		}

		port := uint16(0)
		updated, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", port))
		if err != nil {
			t.Fatalf("updating listener port: %v", err)
		}
		if statusAddress(t, updated) == statusAddress(t, before) {
			t.Fatalf("\nwanted:\nreplacement assigned endpoint\ngot:\n%s", statusAddress(t, updated))
		}
		calls, code, reason := proxy.webSocketClose()
		if calls != 1 || code != marasiws.CloseGoingAway || reason != "" || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\n1 WebSocket close with %d and no flush\ngot:\n%d closes with %d, %q, and %d cleanups", marasiws.CloseGoingAway, calls, code, reason, proxy.cleanupCount())
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

		status, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", port))
		if !errors.Is(err, ErrListenerUnavailable) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrListenerUnavailable, err)
		}
		calls, _, _ := proxy.webSocketClose()
		if statusAddress(t, status) != oldAddress || calls != 0 || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\noriginal active listener without cleanup\ngot:\n%+v, %d closes, and %d cleanups", status, calls, proxy.cleanupCount())
		}
		connection, err := net.DialTimeout("tcp", oldAddress, time.Second)
		if err != nil {
			t.Fatalf("dialing preserved listener: %v", err)
		}
		connection.Close()
	})

	t.Run("should remain inactive when serving fails before start readiness", func(t *testing.T) {
		proxy := newListenerTestProxy()
		serveErr := errors.New("serve failed before readiness")
		proxy.serve = func(net.Listener) error { return serveErr }
		lifecycle := newListenerLifecycle(proxy, nil).(*listenerLifecycle)
		t.Cleanup(func() { lifecycle.Shutdown() })
		subscriber := lifecycle.events.subscribe()
		defer lifecycle.events.unsubscribe(subscriber)

		status, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if !errors.Is(err, ErrListenerUnavailable) || !errors.Is(err, serveErr) {
			t.Fatalf("\nwanted:\nserving failure classified as %v\ngot:\n%v", ErrListenerUnavailable, err)
		}
		if status.Status != ListenerInactive || status.ProxyListener != nil || lifecycle.Status().Status != ListenerInactive {
			t.Fatalf("\nwanted:\ninactive status\ngot:\n%+v and %+v", status, lifecycle.Status())
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno success event\ngot:\n%s", event.name)
		default:
		}
	})

	t.Run("should not report start success before serving reaches the accept loop", func(t *testing.T) {
		proxy := newListenerTestProxy()
		serveEntered := make(chan struct{})
		enterAccept := make(chan struct{})
		proxy.serve = func(listener net.Listener) error {
			close(serveEntered)
			<-enterAccept
			_, err := listener.Accept()
			return err
		}
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		started := make(chan error, 1)
		go func() {
			_, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
			started <- err
		}()
		<-serveEntered
		select {
		case err := <-started:
			t.Fatalf("\nwanted:\nstart to wait for the accept loop\ngot:\n%v", err)
		default:
		}
		close(enterAccept)
		if err := <-started; err != nil {
			t.Fatalf("starting ready listener: %v", err)
		}
	})

	t.Run("should remain inactive when replacement serving fails before readiness", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil).(*listenerLifecycle)
		t.Cleanup(func() { lifecycle.Shutdown() })
		subscriber := lifecycle.events.subscribe()
		defer lifecycle.events.unsubscribe(subscriber)
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		oldAddress := statusAddress(t, started)
		<-subscriber.events
		serveErr := errors.New("replacement failed before readiness")
		proxy.serve = func(net.Listener) error { return serveErr }

		status, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", 0))
		if !errors.Is(err, ErrListenerUnavailable) || !errors.Is(err, serveErr) {
			t.Fatalf("\nwanted:\nreplacement serving failure classified as %v\ngot:\n%v", ErrListenerUnavailable, err)
		}
		if status.Status != ListenerInactive || status.ProxyListener != nil || lifecycle.Status().Status != ListenerInactive {
			t.Fatalf("\nwanted:\ninactive status\ngot:\n%+v and %+v", status, lifecycle.Status())
		}
		if connection, dialErr := net.DialTimeout("tcp", oldAddress, 50*time.Millisecond); dialErr == nil {
			connection.Close()
			t.Fatal("\nwanted:\nold listener to remain closed\ngot:\naccepted connection")
		}
		event := <-subscriber.events
		if event.name != "listener.stopped" || !bytes.Contains(event.data, []byte(`"reason":"failed"`)) {
			t.Fatalf("\nwanted:\nfailed listener stop event\ngot:\n%s %s", event.name, event.data)
		}
	})

	t.Run("should not report update success before replacement serving reaches the accept loop", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		serveEntered := make(chan struct{})
		enterAccept := make(chan struct{})
		proxy.serve = func(listener net.Listener) error {
			close(serveEntered)
			<-enterAccept
			_, err := listener.Accept()
			return err
		}
		updated := make(chan error, 1)
		go func() {
			_, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", 0))
			updated <- err
		}()
		<-serveEntered
		select {
		case err := <-updated:
			t.Fatalf("\nwanted:\nupdate to wait for the replacement accept loop\ngot:\n%v", err)
		default:
		}
		close(enterAccept)
		if err := <-updated; err != nil {
			t.Fatalf("updating to ready listener: %v", err)
		}
	})

	t.Run("should publish one failed stop when the old and replacement serving runs fail", func(t *testing.T) {
		proxy := newListenerTestProxy()
		secondBind := make(chan struct{})
		releaseBind := make(chan struct{})
		bindCalls := 0
		proxy.bind = func(address, port string) (net.Listener, error) {
			bindCalls++
			if bindCalls == 2 {
				close(secondBind)
				<-releaseBind
			}
			return net.Listen("tcp", net.JoinHostPort(address, port))
		}
		firstServeEnded := make(chan struct{})
		serveCalls := 0
		proxy.serve = func(listener net.Listener) error {
			serveCalls++
			if serveCalls == 1 {
				_, err := listener.Accept()
				close(firstServeEnded)
				return err
			}
			return errors.New("replacement failed before readiness")
		}
		lifecycle := newListenerLifecycle(proxy, nil).(*listenerLifecycle)
		t.Cleanup(func() { lifecycle.Shutdown() })
		subscriber := lifecycle.events.subscribe()
		defer lifecycle.events.unsubscribe(subscriber)
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		<-subscriber.events
		old := <-proxy.served
		updated := make(chan error, 1)
		go func() {
			_, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", 0))
			updated <- err
		}()
		<-secondBind
		if err := old.Close(); err != nil {
			t.Fatalf("failing old listener: %v", err)
		}
		<-firstServeEnded
		close(releaseBind)
		if err := <-updated; !errors.Is(err, ErrListenerUnavailable) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrListenerUnavailable, err)
		}
		event := <-subscriber.events
		if event.name != "listener.stopped" || !bytes.Contains(event.data, []byte(`"reason":"failed"`)) {
			t.Fatalf("\nwanted:\none failed listener stop event\ngot:\n%s %s", event.name, event.data)
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("\nwanted:\nno duplicate event\ngot:\n%s %s", event.name, event.data)
		default:
		}
	})

	t.Run("should succeed when WebSocket closure fails during update", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil)
		t.Cleanup(func() { lifecycle.Shutdown() })
		started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		oldAddress := statusAddress(t, started)
		proxy.cleanupErr = errors.New("closing WebSockets failed")
		port := uint16(0)

		updated, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", port))
		if err != nil {
			t.Fatalf("updating listener: %v", err)
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
		if err != nil {
			t.Fatalf("stopping listener after WebSocket close failure: %v", err)
		}
		if stopped.Status != ListenerInactive || stopped.ProxyListener != nil {
			t.Fatalf("\nwanted:\ninactive status after cleanup failure\ngot:\n%+v", stopped)
		}
	})

	t.Run("should not publish a failed stop after requested stop or update", func(t *testing.T) {
		proxy := newListenerTestProxy()
		lifecycle := newListenerLifecycle(proxy, nil).(*listenerLifecycle)
		t.Cleanup(func() { lifecycle.Shutdown() })
		subscriber := lifecycle.events.subscribe()
		defer lifecycle.events.unsubscribe(subscriber)
		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		<-subscriber.events
		if _, err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatalf("stopping listener: %v", err)
		}
		event := <-subscriber.events
		if event.name != "listener.stopped" || !bytes.Contains(event.data, []byte(`"reason":"requested"`)) {
			t.Fatalf("\nwanted:\nrequested listener stop event\ngot:\n%s %s", event.name, event.data)
		}
		assertNoListenerEvent(t, subscriber)

		if _, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("restarting listener: %v", err)
		}
		<-subscriber.events
		if _, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", 0)); err != nil {
			t.Fatalf("updating listener: %v", err)
		}
		event = <-subscriber.events
		if event.name != "listener.updated" {
			t.Fatalf("\nwanted:\nlistener updated event\ngot:\n%s %s", event.name, event.data)
		}
		assertNoListenerEvent(t, subscriber)
	})

	t.Run("should recover after an unexpected serving failure", func(t *testing.T) {
		proxy := newListenerTestProxy()
		var log bytes.Buffer
		lifecycle := newListenerLifecycle(proxy, &log)
		t.Cleanup(func() { lifecycle.Shutdown() })
		_, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("starting listener: %v", err)
		}
		served := <-proxy.served
		if err := served.Close(); err != nil {
			t.Fatalf("failing listener: %v", err)
		}
		waitForListenerStatus(t, lifecycle, ListenerInactive)
		calls, code, reason := proxy.webSocketClose()
		if calls != 1 || code != marasiws.CloseGoingAway || reason != "" || proxy.cleanupCount() != 0 {
			t.Fatalf("\nwanted:\n1 WebSocket close with %d and no flush\ngot:\n%d closes with %d, %q, and %d cleanups", marasiws.CloseGoingAway, calls, code, reason, proxy.cleanupCount())
		}
		if !bytes.Contains(log.Bytes(), []byte("proxy listener stopped unexpectedly")) {
			t.Fatalf("\nwanted:\nfailure log\ngot:\n%s", log.String())
		}
		restarted, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
		if err != nil {
			t.Fatalf("restarting listener: %v", err)
		}
		statusAddress(t, restarted)
	})

	t.Run("should cancel waiting work but finish a transition after it starts", func(t *testing.T) {
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

		waitingContext, cancelWaiting := context.WithCancel(context.Background())
		waitingResult := make(chan error, 1)
		go func() {
			_, err := lifecycle.Stop(waitingContext)
			waitingResult <- err
		}()
		time.Sleep(10 * time.Millisecond)
		cancelWaiting()
		select {
		case err := <-waitingResult:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nprompt waiting cancellation\ngot:\ntimeout")
		}
		close(release)

		if err := <-firstResult; err != nil {
			t.Fatalf("\nwanted:\ncompleted started transition\ngot:\n%v", err)
		}
		if status := lifecycle.Status(); status.Status != ListenerActive {
			t.Fatalf("\nwanted:\ncanceled stop never to run\ngot:\n%+v", status)
		}

		canceledContext, cancel := context.WithCancel(context.Background())
		cancel()
		status, err := lifecycle.Stop(canceledContext)
		if !errors.Is(err, context.Canceled) || status.Status != ListenerActive {
			t.Fatalf("\nwanted:\npre-canceled stop rejected with active status\ngot:\n%+v and %v", status, err)
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
			_, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", firstPort))
			firstResult <- err
		}()
		<-blocked
		go func() {
			_, err := lifecycle.Update(context.Background(), listenerSettings("127.0.0.1", secondPort))
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

func assertNoListenerEvent(t *testing.T, subscriber *eventSubscriber) {
	t.Helper()
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case event := <-subscriber.events:
		t.Fatalf("\nwanted:\nno extra listener event\ngot:\n%s %s", event.name, event.data)
	case <-timer.C:
	}
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
