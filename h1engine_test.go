package marasi

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// rawOrigin is an origin that records the bytes of each request head it reads
// and answers every request with the same raw response. It counts the
// connections it accepted.
type rawOrigin struct {
	addr     string
	response string

	mu          sync.Mutex
	requests    []string
	connections int

	// ended, when set, receives once for each connection the origin's
	// peer ends.
	ended chan struct{}
}

func newRawOrigin(t *testing.T, response string) *rawOrigin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	origin := &rawOrigin{addr: l.Addr().String(), response: response}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			origin.mu.Lock()
			origin.connections++
			origin.mu.Unlock()
			go origin.serve(conn)
		}
	}()
	return origin
}

// serve reads request heads, which carry no body in these tests, and answers
// each one.
func (origin *rawOrigin) serve(conn net.Conn) {
	defer conn.Close()
	if origin.ended != nil {
		defer func() { origin.ended <- struct{}{} }()
	}
	br := bufio.NewReader(conn)
	for {
		var head strings.Builder
		for {
			line, err := br.ReadString('\n')
			head.WriteString(line)
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		origin.mu.Lock()
		origin.requests = append(origin.requests, head.String())
		origin.mu.Unlock()
		if _, err := io.WriteString(conn, origin.response); err != nil {
			return
		}
	}
}

func (origin *rawOrigin) received() ([]string, int) {
	origin.mu.Lock()
	defer origin.mu.Unlock()
	return append([]string(nil), origin.requests...), origin.connections
}

// capturedPair is what the proxy stored for one pair.
type capturedPair struct {
	request  chan domain.ProxyRequest
	response chan domain.ProxyResponse
}

// serveH1EngineProxy serves a proxy on the engine with the base pipeline
// and the setup, filter and write modifiers, so pairs are captured. It returns
// the proxy listener's address and the captures.
func serveH1EngineProxy(t *testing.T) (string, *capturedPair) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(database)
	captured := &capturedPair{request: make(chan domain.ProxyRequest, 16), response: make(chan domain.ProxyResponse, 16)}
	proxy, err := New(
		WithLogger(logger),
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithRequestHandler(func(req domain.ProxyRequest) error { captured.request <- req; return nil }),
		WithResponseHandler(func(res domain.ProxyResponse) error { captured.response <- res; return nil }),
		WithBasePipeline(),
	)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	proxy.AddRequestPairModifier(SetupRequestModifier)
	proxy.AddRequestPairModifier(WriteRequestModifier)
	proxy.AddResponsePairModifier(ResponseFilterModifier)
	proxy.AddResponsePairModifier(WriteResponseModifier)
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- proxy.Serve(listener) }()
	t.Cleanup(func() {
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})
	return net.JoinHostPort(proxy.Addr, proxy.Port), captured
}

// dialProxy opens a client connection to the proxy listener.
func dialProxy(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dialing the proxy: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("setting the client deadline: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

// readExactly reads len(want) bytes and compares them with want.
func readExactly(t *testing.T, br *bufio.Reader, want string) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("reading %q from the proxy: %v (got %q)", want, err, got)
	}
	if string(got) != want {
		t.Fatalf("wanted the client to receive:\n%q\ngot:\n%q", want, got)
	}
}

// originResponse spells its header names in odd case and order.
const originResponse = "HTTP/1.1 200 OK\r\nx-ORIGIN-case: kept\r\nContent-Length: 2\r\naaa-Last: z\r\n\r\nok"

func TestH1Engine_CarriesTheWireForm(t *testing.T) {
	t.Run("should send an absolute-form GET to the origin in origin form with the client's header case and order", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		head := "host: " + origin.addr + "\r\nX-lower-UPPER: v\r\naccept:  */*\r\n\r\n"
		fmt.Fprintf(conn, "GET http://%s/path?q=1 HTTP/1.1\r\n%s", origin.addr, head)

		readExactly(t, br, originResponse)
		requests, _ := origin.received()
		if want := "GET /path?q=1 HTTP/1.1\r\n" + head; len(requests) != 1 || requests[0] != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, requests)
		}
		select {
		case req := <-captured.request:
			if want := "GET /path?q=1 HTTP/1.1\r\n" + head; string(req.Raw) != want {
				t.Fatalf("wanted the stored request:\n%q\ngot:\n%q", want, req.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
		select {
		case res := <-captured.response:
			if string(res.Raw) != originResponse {
				t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", originResponse, res.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
	})

	t.Run("should replace a Host that differs from an absolute-form target's authority in place", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/m HTTP/1.1\r\nx-First: 1\r\nhOST: other.example:81\r\nX-Last: 2\r\n\r\n", origin.addr)

		readExactly(t, br, originResponse)
		want := "GET /m HTTP/1.1\r\nx-First: 1\r\nhOST: " + origin.addr + "\r\nX-Last: 2\r\n\r\n"
		requests, _ := origin.received()
		if len(requests) != 1 || requests[0] != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, requests)
		}
		select {
		case req := <-captured.request:
			if string(req.Raw) != want {
				t.Fatalf("wanted the stored request:\n%q\ngot:\n%q", want, req.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
	})

	t.Run("should carry a request with a later HTTP/1 minor version as written", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		head := "Host: " + origin.addr + "\r\n\r\n"
		fmt.Fprintf(conn, "GET http://%s/v HTTP/1.2\r\n%s", origin.addr, head)

		readExactly(t, br, originResponse)
		requests, _ := origin.received()
		if want := "GET /v HTTP/1.2\r\n" + head; len(requests) != 1 || requests[0] != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, requests)
		}
	})

	t.Run("should carry a GET inside CONNECT to the origin with the client's header case and order", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		res, err := http.ReadResponse(br, nil)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("wanted the tunnel opened with a 200\ngot: %v, %v", res, err)
		}
		request := "GET /in-tunnel HTTP/1.1\r\nHOST: " + origin.addr + "\r\nzz-First: 1\r\nAccept-encoding: identity\r\n\r\n"
		io.WriteString(conn, request)

		readExactly(t, br, originResponse)
		requests, _ := origin.received()
		if len(requests) != 1 || requests[0] != request {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, requests)
		}
		select {
		case req := <-captured.request:
			if string(req.Raw) != request {
				t.Fatalf("wanted the stored request:\n%q\ngot:\n%q", request, req.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
		select {
		case res := <-captured.response:
			if string(res.Raw) != originResponse {
				t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", originResponse, res.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
	})
}

func TestH1Engine_ConnectInsideATunnel(t *testing.T) {
	t.Run("should forward a CONNECT sent inside a tunnel to the tunnel's origin as written", func(t *testing.T) {
		const refusal = "HTTP/1.1 405 Not Allowed\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
		origin := newRawOrigin(t, refusal)
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		res, err := http.ReadResponse(br, nil)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("wanted the tunnel opened with a 200\ngot: %v, %v", res, err)
		}
		// The Host names another server: the request is the tunnel origin's
		// to answer, as it would be without Marasi.
		request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
		io.WriteString(conn, request)

		readExactly(t, br, "HTTP/1.1 405 Not Allowed\r\nContent-Length: 0\r\n\r\n")
		requests, _ := origin.received()
		if len(requests) != 1 || requests[0] != request {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, requests)
		}
		select {
		case req := <-captured.request:
			if string(req.Raw) != request {
				t.Fatalf("wanted the stored request:\n%q\ngot:\n%q", request, req.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
		select {
		case res := <-captured.response:
			if want := "HTTP/1.1 405 Not Allowed\r\nContent-Length: 0\r\n\r\n"; string(res.Raw) != want {
				t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, res.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
	})
}

func TestH1Engine_OneOriginConnectionPerClientConnection(t *testing.T) {
	t.Run("should never let two client connections share an origin connection", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		addr, _ := serveH1EngineProxy(t)
		request := fmt.Sprintf("GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		first, firstReader := dialProxy(t, addr)
		second, secondReader := dialProxy(t, addr)
		for range 2 {
			io.WriteString(first, request)
			readExactly(t, firstReader, originResponse)
			io.WriteString(second, request)
			readExactly(t, secondReader, originResponse)
		}

		requests, connections := origin.received()
		if len(requests) != 4 || connections != 2 {
			t.Fatalf("wanted four requests on two origin connections, one per client connection\ngot: %d requests on %d connections", len(requests), connections)
		}
	})
}

func TestH1Engine_ProxyErrorResponse(t *testing.T) {
	t.Run("should answer a request to an unreachable origin with a 502 proxy error response", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listening: %v", err)
		}
		unreachable := l.Addr().String()
		l.Close()
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", unreachable, unreachable)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusBadGateway || !bytes.Contains(body, []byte(unreachable)) {
			t.Fatalf("wanted a 502 naming the origin\ngot: %d %q", res.StatusCode, body)
		}
		select {
		case stored := <-captured.response:
			if stored.Metadata[proxyErrorMetadataKey] == nil {
				t.Fatalf("wanted the stored pair marked with %s\ngot: %v", proxyErrorMetadataKey, stored.Metadata)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the proxy error response was not captured")
		}
	})
}

func TestH1Engine_ClientLeaves(t *testing.T) {
	t.Run("should close the origin connection when the client leaves while the origin has not answered", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listening for the origin: %v", err)
		}
		t.Cleanup(func() { l.Close() })
		originClosed := make(chan struct{})
		go func() {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			// Read the request and never answer; a read that ends means
			// the proxy closed the connection.
			io.Copy(io.Discard, conn)
			close(originClosed)
		}()
		addr, _ := serveH1EngineProxy(t)
		conn, _ := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/hung HTTP/1.1\r\nHost: %s\r\n\r\n", l.Addr(), l.Addr())
		time.Sleep(100 * time.Millisecond)
		conn.Close()

		select {
		case <-originClosed:
		case <-time.After(5 * time.Second):
			t.Fatal("wanted the origin connection closed once the client left")
		}
	})
}
