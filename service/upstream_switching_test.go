package service

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/upstreamtest"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// editConfig rewrites marasi_config.yaml in configDir by hand, replacing old
// with replacement, and moves its modification time forward so the change
// is seen even within the file system's time resolution.
func editConfig(t *testing.T, configDir, old, replacement string) {
	t.Helper()
	path := filepath.Join(configDir, "marasi_config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("marasi_config.yaml holds no %q:\n%s", old, data)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	bumpModTime(t, configDir)
}

// bumpModTime moves marasi_config.yaml's modification time forward.
func bumpModTime(t *testing.T, configDir string) {
	t.Helper()
	path := filepath.Join(configDir, "marasi_config.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

// assertNoUpstreamProxyEvent fails if subscriber holds an upstream_proxy.*
// event. Traffic events are expected and skipped.
func assertNoUpstreamProxyEvent(t *testing.T, subscriber *eventSubscriber) {
	t.Helper()
	for {
		select {
		case event := <-subscriber.events:
			if strings.HasPrefix(event.name, "upstream_proxy.") {
				t.Fatalf("\nwanted:\nno upstream proxy event\ngot:\n%s %s", event.name, event.data)
			}
		default:
			return
		}
	}
}

func TestUpstreamProxySwitchingAcrossInstances(t *testing.T) {
	t.Run("should apply an update of the selected entry in a second instance on its next request, publishing nothing there", func(t *testing.T) {
		configDir := t.TempDir()
		editor := newUpstreamServiceIn(t, configDir)
		user := newUpstreamServiceIn(t, configDir)
		first := upstreamtest.NewHTTP(t)
		second := upstreamtest.NewHTTP(t)
		editor.addUpstreamProxy(t, "corp", first.URL("http", "", ""))
		user.use(t, "corp")
		// Keep-alive: the origin connection through first stays idle.
		user.get(t, user.origin.URL+"/one")
		subscriber := user.server.events.subscribe()
		defer user.server.events.unsubscribe(subscriber)

		editor.mustCall(t, http.MethodPost, "/upstream-proxy/corp", `{"url":"`+second.URL("http", "", "")+`"}`)
		bumpModTime(t, configDir)
		user.get(t, user.origin.URL+"/two")

		if !carried(second, "GET /two HTTP/1.1\r\n") || carried(first, "GET /two HTTP/1.1\r\n") {
			t.Fatalf("\nwanted:\n/two through the updated entry\ngot:\nfirst %v, second %v", first.Targets(), second.Targets())
		}
		assertNoUpstreamProxyEvent(t, subscriber)
	})

	t.Run("should apply a hand edit of the selected entry's credentials on the next request, on a new connection", func(t *testing.T) {
		configDir := t.TempDir()
		editor := newUpstreamServiceIn(t, configDir)
		user := newUpstreamServiceIn(t, configDir)
		fake := upstreamtest.NewHTTP(t)
		editor.addUpstreamProxy(t, "corp", fake.URL("http", "alice", "old"))
		user.use(t, "corp")
		user.get(t, user.origin.URL+"/one")
		subscriber := user.server.events.subscribe()
		defer user.server.events.unsubscribe(subscriber)

		editConfig(t, configDir, "alice:old@", "bob:new@")
		user.get(t, user.origin.URL+"/two")

		tunnels := fake.Tunnels()
		if len(tunnels) != 2 {
			t.Fatalf("\nwanted:\na second tunnel for /two\ngot:\n%d tunnels", len(tunnels))
		}
		if tunnels[1].Username != "bob" || tunnels[1].Password != "new" || !strings.Contains(string(tunnels[1].Sent()), "GET /two ") {
			t.Fatalf("\nwanted:\n/two in a tunnel opened with bob's credentials\ngot:\n%q %q %q", tunnels[1].Username, tunnels[1].Password, tunnels[1].Sent())
		}
		assertNoUpstreamProxyEvent(t, subscriber)
	})
}

// heldRelay forwards connections to target, holding each one until release
// is called, so a dial to an upstream proxy can be caught in progress.
type heldRelay struct {
	addr    string
	entered chan struct{}
	release func()
}

func newHeldRelay(t *testing.T, target string) *heldRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	released := make(chan struct{})
	var once sync.Once
	relay := &heldRelay{addr: listener.Addr().String(), entered: make(chan struct{}, 16)}
	relay.release = func() { once.Do(func() { close(released) }) }
	t.Cleanup(relay.release)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				relay.entered <- struct{}{}
				<-released
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer upstream.Close()
				go func() { _, _ = io.Copy(upstream, conn); upstream.Close() }()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	return relay
}

func TestUpstreamProxySwitchingInFlight(t *testing.T) {
	t.Run("should carry a request whose dial was under way during use on the new route", func(t *testing.T) {
		service := newUpstreamService(t)
		first := upstreamtest.NewHTTP(t)
		second := upstreamtest.NewHTTP(t)
		relay := newHeldRelay(t, first.Addr)
		service.addUpstreamProxy(t, "first", "http://"+relay.addr)
		service.addUpstreamProxy(t, "second", second.URL("http", "", ""))
		service.use(t, "first")

		done := service.getAsync(t, service.origin.URL+"/held")
		relay.awaitEntered(t)
		service.use(t, "second")
		relay.release()

		if got := awaitResult(t, done); got != "200 OK origin /held" {
			t.Fatalf("\nwanted:\n200 OK origin /held\ngot:\n%s", got)
		}
		onlyThrough(t, "GET /held HTTP/1.1\r\n", second, first)
	})

	t.Run("should fail closed a request whose dial was under way when the selected entry was removed", func(t *testing.T) {
		service := newUpstreamService(t)
		first := upstreamtest.NewHTTP(t)
		relay := newHeldRelay(t, first.Addr)
		service.addUpstreamProxy(t, "first", "http://"+relay.addr)
		service.use(t, "first")

		done := service.getAsync(t, service.origin.URL+"/held")
		relay.awaitEntered(t)
		service.mustCall(t, http.MethodDelete, "/upstream-proxy/first", "")
		// The removal takes effect when the next request resolves the route.
		if response, _ := service.get(t, service.origin.URL+"/after"); response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502 for /after\ngot:\n%d", response.StatusCode)
		}
		relay.release()

		if got := awaitResult(t, done); !strings.HasPrefix(got, "502 ") {
			t.Fatalf("\nwanted:\n502 for /held\ngot:\n%s", got)
		}
		if hits := service.originHits.Load(); hits != 0 || carried(first, "GET /held ") {
			t.Fatalf("\nwanted:\nno request reached the origin\ngot:\n%d hits, first %v", hits, first.Targets())
		}
	})
}

// awaitEntered waits for a dial to reach the relay.
func (relay *heldRelay) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-relay.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no dial reached the relay")
	}
}

func TestUpstreamProxySwitchingRemoval(t *testing.T) {
	t.Run("should fail closed after the selected entry is removed, never reusing an idle connection or going direct", func(t *testing.T) {
		configDir := t.TempDir()
		editor := newUpstreamServiceIn(t, configDir)
		user := newUpstreamServiceIn(t, configDir)
		fake := upstreamtest.NewHTTP(t)
		editor.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		user.use(t, "corp")
		user.get(t, user.origin.URL+"/one")

		editor.mustCall(t, http.MethodDelete, "/upstream-proxy/corp", "")
		bumpModTime(t, configDir)
		response, _ := user.get(t, user.origin.URL+"/two")

		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
		}
		if hits := user.originHits.Load(); hits != 1 || carried(fake, "GET /two ") {
			t.Fatalf("\nwanted:\nonly /one reached the origin\ngot:\n%d hits", hits)
		}
		if name, configured := user.status(t); name != "corp" || configured {
			t.Fatalf("\nwanted:\ncorp false\ngot:\n%q %v", name, configured)
		}
	})
}

// switchOrigin is an origin for switching tests. /slow waits for release,
// /ws upgrades and echoes WebSocket frames, and any other path answers with
// "origin <path>". It counts the plain HTTP connections it holds open.
type switchOrigin struct {
	*httptest.Server
	entered chan struct{}
	release func()
	open    atomic.Int64
}

func newSwitchOrigin(t *testing.T) *switchOrigin {
	t.Helper()
	released := make(chan struct{})
	var once sync.Once
	origin := &switchOrigin{entered: make(chan struct{}, 16)}
	origin.release = func() { once.Do(func() { close(released) }) }
	origin.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			origin.entered <- struct{}{}
			<-released
		case "/ws":
			serveEcho(w, r)
			return
		}
		_, _ = io.WriteString(w, "origin "+r.URL.Path)
	}))
	origin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			origin.open.Add(1)
		case http.StateClosed, http.StateHijacked:
			origin.open.Add(-1)
		}
	}
	origin.Start()
	t.Cleanup(origin.Close)
	t.Cleanup(origin.release)
	return origin
}

// serveEcho upgrades to WebSocket and echoes each data frame back.
func serveEcho(w http.ResponseWriter, r *http.Request) {
	conn, buffered, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
	if buffered.Flush() != nil {
		return
	}
	for {
		frame, err := marasiws.ReadFrame(buffered)
		if err != nil || frame.Opcode == marasiws.OpClose {
			return
		}
		reply := marasiws.Frame{Fin: true, Opcode: frame.Opcode, Payload: frame.Payload}
		if marasiws.WriteFrame(buffered, reply, false) != nil || buffered.Flush() != nil {
			return
		}
	}
}

// awaitEntered waits for a request to reach /slow.
func (origin *switchOrigin) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-origin.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached /slow")
	}
}

// awaitOpen waits until the origin holds want plain HTTP connections.
func (origin *switchOrigin) awaitOpen(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for origin.open.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("\nwanted:\n%d open origin connections\ngot:\n%d", want, origin.open.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// getAsync sends a GET through the listener from a client of its own, as
// another browser would, and delivers "<status> <body>" or the error text.
func (s *upstreamService) getAsync(t *testing.T, target string) <-chan string {
	transport := s.client.Transport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: s.client.Timeout}
	done := make(chan string, 1)
	go func() {
		response, err := client.Get(target)
		if err != nil {
			done <- err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		done <- response.Status + " " + string(body)
	}()
	return done
}

// awaitResult waits for a getAsync result.
func awaitResult(t *testing.T, done <-chan string) string {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not finish")
		return ""
	}
}

// getReusing sends a GET through the listener and returns "<status> <body>"
// and whether the client reused its connection to the listener.
func (s *upstreamService) getReusing(t *testing.T, target string) (string, bool) {
	t.Helper()
	var reused atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.Status + " " + string(body), reused.Load()
}

// listenerAddress returns the proxy listener's address from /service/status.
func (s *upstreamService) listenerAddress(t *testing.T) string {
	t.Helper()
	status := decodeJSON[struct {
		ProxyListener *string `json:"proxy_listener"`
	}](t, s.mustCall(t, http.MethodGet, "/service/status", ""))
	if status.ProxyListener == nil {
		t.Fatal("no proxy listener in /service/status")
	}
	return *status.ProxyListener
}

// onlyThrough fails unless text went through want and not through other.
func onlyThrough(t *testing.T, text string, want, other *upstreamtest.Proxy) {
	t.Helper()
	if !carried(want, text) || carried(other, text) {
		t.Fatalf("\nwanted:\n%q through %s only\ngot:\n%s %v, %s %v", text, want.Addr, want.Addr, want.Targets(), other.Addr, other.Targets())
	}
}

// twoUpstreamProxies lists first and second on service and selects first.
func twoUpstreamProxies(t *testing.T, service *upstreamService) (first, second *upstreamtest.Proxy) {
	t.Helper()
	first, second = upstreamtest.NewHTTP(t), upstreamtest.NewHTTP(t)
	service.addUpstreamProxy(t, "first", first.URL("http", "", ""))
	service.addUpstreamProxy(t, "second", second.URL("http", "", ""))
	service.use(t, "first")
	return first, second
}

func TestUpstreamProxySwitchingWithUse(t *testing.T) {
	t.Run("should let a request in flight through the old upstream proxy finish there and send the next through the new one on a new connection", func(t *testing.T) {
		service := newUpstreamService(t)
		origin := newSwitchOrigin(t)
		first, second := twoUpstreamProxies(t, service)
		address := service.listenerAddress(t)
		if got, _ := service.getReusing(t, origin.URL+"/warm"); got != "200 OK origin /warm" {
			t.Fatalf("\nwanted:\n200 OK origin /warm\ngot:\n%s", got)
		}

		slow := service.getAsync(t, origin.URL+"/slow")
		origin.awaitEntered(t)
		service.use(t, "second")
		got, reused := service.getReusing(t, origin.URL+"/next")
		if got != "200 OK origin /next" {
			t.Fatalf("\nwanted:\n200 OK origin /next\ngot:\n%s", got)
		}
		origin.release()
		if got := awaitResult(t, slow); got != "200 OK origin /slow" {
			t.Fatalf("\nwanted:\n200 OK origin /slow\ngot:\n%s", got)
		}

		onlyThrough(t, "GET /slow HTTP/1.1\r\n", first, second)
		onlyThrough(t, "GET /next HTTP/1.1\r\n", second, first)
		if tunnels := second.Tunnels(); len(tunnels) != 1 || !strings.HasPrefix(string(tunnels[0].Sent()), "GET /next ") {
			t.Fatalf("\nwanted:\n/next first on a new tunnel through second\ngot:\n%d tunnels", len(tunnels))
		}
		// The client never noticed: the same listener, the same connection.
		if !reused {
			t.Fatal("the client's connection to the listener was not reused after use")
		}
		if got := service.listenerAddress(t); got != address {
			t.Fatalf("\nwanted:\nlistener %s\ngot:\n%s", address, got)
		}
	})

	t.Run("should close an idle keep-alive connection through the old upstream proxy rather than reuse it", func(t *testing.T) {
		service := newUpstreamService(t)
		origin := newSwitchOrigin(t)
		first, second := twoUpstreamProxies(t, service)
		service.get(t, origin.URL+"/one")
		origin.awaitOpen(t, 1)

		service.use(t, "second")
		origin.awaitOpen(t, 0)
		service.get(t, origin.URL+"/two")

		onlyThrough(t, "GET /two HTTP/1.1\r\n", second, first)
		if len(first.Tunnels()) != 1 {
			t.Fatalf("\nwanted:\none tunnel through first\ngot:\n%v", first.Targets())
		}
	})

	t.Run("should keep an open WebSocket connection through the old upstream proxy working until it closes", func(t *testing.T) {
		service := newUpstreamService(t)
		origin := newSwitchOrigin(t)
		first, second := twoUpstreamProxies(t, service)
		conn, reader := dialWebSocket(t, service.address, origin.URL)
		echo(t, conn, reader, "before")

		tunnels := first.Tunnels()
		if len(tunnels) != 1 || !strings.HasPrefix(string(tunnels[0].Sent()), "GET /ws ") {
			t.Fatalf("\nwanted:\nthe WebSocket connection through first\ngot:\n%v", first.Targets())
		}
		socket := tunnels[0]
		before := len(socket.Sent())

		service.use(t, "second")
		service.get(t, origin.URL+"/after")
		echo(t, conn, reader, "still first")

		// The proxy masks frames again toward the origin, so look for more
		// bytes in the WebSocket tunnel, not for the text.
		if len(socket.Sent()) <= before || len(first.Tunnels()) != 1 {
			t.Fatalf("\nwanted:\nthe later frame through the same tunnel\ngot:\n%d bytes before, %d after, %d tunnels", before, len(socket.Sent()), len(first.Tunnels()))
		}
		onlyThrough(t, "GET /after HTTP/1.1\r\n", second, first)
		if got := countCarried(second, " HTTP/1.1\r\n"); got != 1 {
			t.Fatalf("\nwanted:\nonly /after through second\ngot:\n%d requests", got)
		}
	})
}

// dialWebSocket upgrades /ws on origin through the listener and reads the
// 101 response.
func dialWebSocket(t *testing.T, address, originURL string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	host := strings.TrimPrefix(originURL, "http://")
	fmt.Fprintf(conn, "GET %s/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", originURL, host)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("\nwanted:\n101\ngot:\n%v %v", response, err)
	}
	return conn, reader
}

// echo sends text on a WebSocket connection and expects it back.
func echo(t *testing.T, conn net.Conn, reader *bufio.Reader, text string) {
	t.Helper()
	if err := marasiws.WriteFrame(conn, marasiws.Frame{Fin: true, Opcode: marasiws.OpText, Payload: []byte(text)}, true); err != nil {
		t.Fatalf("sending %q: %v", text, err)
	}
	frame, err := marasiws.ReadFrame(reader)
	if err != nil || string(frame.Payload) != text {
		t.Fatalf("\nwanted:\necho %q\ngot:\n%q %v", text, frame.Payload, err)
	}
}

func TestUpstreamProxySwitchingWithProject(t *testing.T) {
	t.Run("should change the route with the project, dialing nothing and waiting on nothing during the switch", func(t *testing.T) {
		service := newUpstreamService(t)
		origin := newSwitchOrigin(t)
		first := upstreamtest.NewHTTP(t)
		second := upstreamtest.NewHTTP(t)
		relay := newHeldRelay(t, second.Addr)
		relay.release()
		service.addUpstreamProxy(t, "first", first.URL("http", "", ""))
		service.addUpstreamProxy(t, "second", "http://"+relay.addr)
		service.use(t, "first")
		address := service.listenerAddress(t)
		if got, _ := service.getReusing(t, origin.URL+"/warm"); got != "200 OK origin /warm" {
			t.Fatalf("\nwanted:\n200 OK origin /warm\ngot:\n%s", got)
		}
		selectsSecond := canonicalProjectPath(t, filepath.Join(t.TempDir(), "second.marasi"))
		seedSelection(t, selectsSecond, "second")
		selectsNone := canonicalProjectPath(t, filepath.Join(t.TempDir(), "none.marasi"))
		seedSelection(t, selectsNone, "")

		slow := service.getAsync(t, origin.URL+"/slow")
		origin.awaitEntered(t)
		prepared := awaitTargetPrepared(t, service.lifecycle)
		opened := make(chan error, 1)
		go func() { opened <- service.lifecycle.Open(context.Background(), selectsSecond) }()
		prepared()
		select {
		case err := <-opened:
			if err != nil {
				t.Fatalf("opening the second project: %v", err)
			}
		case <-time.After(time.Second):
			origin.release()
			<-opened
			t.Fatal("the switch waited on the request in flight through first")
		}
		select {
		case <-relay.entered:
			t.Fatal("the switch dialed the second upstream proxy")
		default:
		}

		got, reused := service.getReusing(t, origin.URL+"/next")
		if got != "200 OK origin /next" {
			t.Fatalf("\nwanted:\n200 OK origin /next\ngot:\n%s", got)
		}
		onlyThrough(t, "GET /next HTTP/1.1\r\n", second, first)
		origin.release()
		if got := awaitResult(t, slow); got != "200 OK origin /slow" {
			t.Fatalf("\nwanted:\n200 OK origin /slow\ngot:\n%s", got)
		}
		onlyThrough(t, "GET /slow HTTP/1.1\r\n", first, second)

		service.mustCall(t, http.MethodPost, "/project/open", projectOpenBody(selectsNone))
		if got, _ := service.getReusing(t, origin.URL+"/direct"); got != "200 OK origin /direct" {
			t.Fatalf("\nwanted:\n200 OK origin /direct\ngot:\n%s", got)
		}
		if carried(first, "GET /direct ") || carried(second, "GET /direct ") {
			t.Fatalf("\nwanted:\n/direct sent directly\ngot:\nfirst %v, second %v", first.Targets(), second.Targets())
		}

		if !reused {
			t.Fatal("the client's connection to the listener was not reused after the switch")
		}
		if got := service.listenerAddress(t); got != address {
			t.Fatalf("\nwanted:\nlistener %s\ngot:\n%s", address, got)
		}
	})
}
