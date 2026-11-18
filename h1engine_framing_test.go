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
	"github.com/tfkr-ae/marasi/internal/h1"
)

// bodyOrigin is an origin that reads whole requests, bodies included, with
// the codec, records each request's bytes as they arrived, and answers each
// with the same raw response.
type bodyOrigin struct {
	addr     string
	response string

	mu       sync.Mutex
	requests []string
}

func newBodyOrigin(t *testing.T, response string) *bodyOrigin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	origin := &bodyOrigin{addr: l.Addr().String(), response: response}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go origin.serve(conn)
		}
	}()
	return origin
}

func (origin *bodyOrigin) serve(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		head, err := h1.ReadRequestHead(br)
		if err != nil {
			return
		}
		framing, err := h1.RequestFraming(head)
		if err != nil {
			return
		}
		body, err := h1.ReadBody(br, framing)
		if err != nil {
			return
		}
		var raw bytes.Buffer
		head.WriteTo(&raw)
		body.WriteTo(&raw)
		origin.mu.Lock()
		origin.requests = append(origin.requests, raw.String())
		origin.mu.Unlock()
		if _, err := io.WriteString(conn, origin.response); err != nil {
			return
		}
	}
}

// request returns the only request the origin received.
func (origin *bodyOrigin) request(t *testing.T) string {
	t.Helper()
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if len(origin.requests) != 1 {
		t.Fatalf("wanted the origin to receive one request\ngot: %q", origin.requests)
	}
	return origin.requests[0]
}

// serveFramingProxy serves a proxy on the new engine with the base pipeline,
// capture, and the modifiers configure adds between setup and capture.
func serveFramingProxy(t *testing.T, configure func(proxy *Proxy)) (*Proxy, string, *capturedPair) {
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
	proxy.AddResponsePairModifier(ResponseFilterModifier)
	if configure != nil {
		configure(proxy)
	}
	proxy.AddRequestPairModifier(WriteRequestModifier)
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
	return proxy, net.JoinHostPort(proxy.Addr, proxy.Port), captured
}

// storedPair waits for the stored raw request and response.
func storedPair(t *testing.T, captured *capturedPair) (string, string) {
	t.Helper()
	var request, response string
	select {
	case req := <-captured.request:
		request = string(req.Raw)
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not captured")
	}
	select {
	case res := <-captured.response:
		response = string(res.Raw)
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
	}
	return request, response
}

// setRequestBody is a request modifier that replaces the body.
func setRequestBody(body string) PairModifier {
	return func(_ *Proxy, pair *Pair) error {
		pair.Request.Body = io.NopCloser(strings.NewReader(body))
		return nil
	}
}

// setResponseBody is a response modifier that replaces the body.
func setResponseBody(body string) PairModifier {
	return func(_ *Proxy, pair *Pair) error {
		pair.Response.Body = io.NopCloser(strings.NewReader(body))
		return nil
	}
}

// exchangeRaw sends raw to the proxy and reads len(want) bytes back.
func exchangeRaw(t *testing.T, addr, raw, want string) {
	t.Helper()
	conn, br := dialProxy(t, addr)
	io.WriteString(conn, raw)
	readExactly(t, br, want)
}

func TestH1Engine_HopByHopFields(t *testing.T) {
	t.Run("should never let the fields Connection names reach the other leg in either direction", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nConnection: X-Back, close\r\nX-Back: 1\r\nContent-Length: 2\r\nx-End: e\r\n\r\nok")
		_, addr, captured := serveFramingProxy(t, nil)

		host := "Host: " + origin.addr + "\r\n"
		exchangeRaw(t, addr,
			"GET http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"connection: X-Hop, keep-alive,TE\r\nX-Hop: 1\r\nKeep-Alive: timeout=5\r\nTE: trailers\r\nX-Kept: y\r\n\r\n",
			"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nx-End: e\r\n\r\nok")

		want := "GET / HTTP/1.1\r\n" + host + "X-Kept: y\r\n\r\n"
		if got := origin.request(t); got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
		request, response := storedPair(t, captured)
		if request != want || response != "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nx-End: e\r\n\r\nok" {
			t.Fatalf("wanted the stored pair to be what was sent\ngot:\n%q\n%q", request, response)
		}
	})

	t.Run("should keep the upgrade a request asks for", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, nil)

		host := "Host: " + origin.addr + "\r\n"
		exchangeRaw(t, addr,
			"GET http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"Connection: keep-alive, Upgrade\r\nUpgrade: websocket\r\n\r\n",
			"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")

		want := "GET / HTTP/1.1\r\n" + host + "Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n"
		if got := origin.request(t); got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
	})
}

func TestH1Engine_EditedBodyFraming(t *testing.T) {
	t.Run("should send an edited chunked request chunked with its trailers and never with Content-Length", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		_, addr, captured := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(setRequestBody("edited!"))
			// A modifier written for Martian also sets the length.
			proxy.AddRequestPairModifier(func(_ *Proxy, pair *Pair) error {
				pair.Request.Header.Set("Content-Length", "7")
				return nil
			})
		})

		head := "POST http://" + origin.addr + "/ HTTP/1.1\r\nHost: " + origin.addr + "\r\ntransfer-encoding: chunked\r\n\r\n"
		exchangeRaw(t, addr, head+"3;x=1\r\nabc\r\n0\r\nX-Trail: t\r\n\r\n", "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")

		want := "POST / HTTP/1.1\r\nHost: " + origin.addr + "\r\ntransfer-encoding: chunked\r\n\r\n7\r\nedited!\r\n0\r\nX-Trail: t\r\n\r\n"
		if got := origin.request(t); got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
		request, _ := storedPair(t, captured)
		if want := "POST / HTTP/1.1\r\nHost: " + origin.addr + "\r\ntransfer-encoding: chunked\r\n\r\nedited!"; request != want {
			t.Fatalf("wanted the stored request:\n%q\ngot:\n%q", want, request)
		}
	})

	t.Run("should send an edited chunked response chunked with its trailers", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\nX-Sum: 1\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddResponsePairModifier(setResponseBody("changed"))
		})

		exchangeRaw(t, addr, "GET http://"+origin.addr+"/ HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n",
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n7\r\nchanged\r\n0\r\nX-Sum: 1\r\n\r\n")
	})

	t.Run("should rewrite an edited Content-Length body's length on its own line with its name case", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(setRequestBody("hello"))
		})

		host := "Host: " + origin.addr + "\r\n"
		exchangeRaw(t, addr, "POST http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"content-LENGTH:3\r\nX-After: a\r\n\r\nabc",
			"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")

		want := "POST / HTTP/1.1\r\n" + host + "content-LENGTH: 5\r\nX-After: a\r\n\r\nhello"
		if got := origin.request(t); got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should append Content-Length when an edit adds a body to a message with neither", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(setRequestBody("hi"))
		})

		host := "Host: " + origin.addr + "\r\n"
		exchangeRaw(t, addr, "GET http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"X-Last: l\r\n\r\n",
			"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")

		want := "GET / HTTP/1.1\r\n" + host + "X-Last: l\r\nContent-Length: 2\r\n\r\nhi"
		if got := origin.request(t); got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should never re-frame a body that was set to itself", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(setRequestBody("abc"))
		})

		request := "POST / HTTP/1.1\r\nHost: " + origin.addr + "\r\nTransfer-Encoding: chunked\r\n\r\n2;e\r\nab\r\n1\r\nc\r\n0\r\n\r\n"
		exchangeRaw(t, addr, strings.Replace(request, "POST /", "POST http://"+origin.addr+"/", 1), "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")

		if got := origin.request(t); got != request {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, got)
		}
	})

	t.Run("should drop the Content-Length of a response the origin framed with both", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 99\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, nil)

		exchangeRaw(t, addr, "GET http://"+origin.addr+"/ HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n",
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\n\r\n")
	})
}

func TestH1Engine_LuaSetBody(t *testing.T) {
	t.Run("should frame a body Lua set_body changed from its final bytes, keeping chunked messages chunked", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			ext := &domain.Extension{Name: "workshop", ID: testExtensions["workshop"].ID, Enabled: true, LuaContent: `
				function processRequest(request) request:set_body("lua!") end
				function processResponse(response) response:set_body("from lua") end
			`}
			if err := proxy.WithOptions(WithExtension(ext)); err != nil {
				t.Fatalf("adding the extension: %v", err)
			}
			proxy.AddRequestPairModifier(ExtensionsRequestModifier)
			proxy.AddResponsePairModifier(ExtensionsResponseModifier)
		})

		host := "Host: " + origin.addr + "\r\n"
		exchangeRaw(t, addr, "POST http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"Transfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n",
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n8\r\nfrom lua\r\n0\r\n\r\n")

		want := "POST / HTTP/1.1\r\n" + host + "Transfer-Encoding: chunked\r\n\r\n4\r\nlua!\r\n0\r\n\r\n"
		if got := origin.request(t); got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
	})
}

func TestH1Engine_LuaSetBodyUnchanged(t *testing.T) {
	t.Run("should change no bytes when Lua set_body sets a body to itself", func(t *testing.T) {
		const response = "HTTP/1.1 200 OK\r\ncontent-length: 2\r\n\r\nok"
		origin := newBodyOrigin(t, response)
		_, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			ext := &domain.Extension{Name: "workshop", ID: testExtensions["workshop"].ID, Enabled: true, LuaContent: `
				function processRequest(request) request:set_body(request:body()) end
				function processResponse(response) response:set_body(response:body()) end
			`}
			if err := proxy.WithOptions(WithExtension(ext)); err != nil {
				t.Fatalf("adding the extension: %v", err)
			}
			proxy.AddRequestPairModifier(ExtensionsRequestModifier)
			proxy.AddResponsePairModifier(ExtensionsResponseModifier)
		})

		request := "GET / HTTP/1.1\r\nHost: " + origin.addr + "\r\n\r\n"
		exchangeRaw(t, addr, strings.Replace(request, "GET /", "GET http://"+origin.addr+"/", 1), response)

		if got := origin.request(t); got != request {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, got)
		}
	})
}

// serveCheckpointProxy serves a framing proxy that holds every request and
// response in Checkpoint, configured further by each of extra.
func serveCheckpointProxy(t *testing.T, extra ...func(proxy *Proxy)) (*Proxy, string, *capturedPair) {
	t.Helper()
	return serveFramingProxy(t, func(proxy *Proxy) {
		for _, configure := range extra {
			configure(proxy)
		}
		ext := *testExtensions["checkpoint"]
		ext.Enabled = true
		if err := proxy.WithOptions(WithExtension(&ext)); err != nil {
			t.Fatalf("adding Checkpoint: %v", err)
		}
		proxy.SetIntercept(true)
		proxy.AddRequestPairModifier(CheckpointRequestModifier)
		proxy.AddResponsePairModifier(CheckpointResponseModifier)
	})
}

// forwardHeld waits for one held item, checks that it shows want, and
// forwards it with raw as an edit.
func forwardHeld(t *testing.T, proxy *Proxy, want, raw string) {
	t.Helper()
	item := waitForCheckpoint(t, proxy, 1)[0]
	if string(item.Raw) != want {
		t.Fatalf("wanted Checkpoint to show:\n%q\ngot:\n%q", want, item.Raw)
	}
	edit := []byte(raw)
	if err := proxy.ForwardCheckpoint(item.ID, CheckpointForward{Raw: &edit}); err != nil {
		t.Fatalf("forwarding: %v", err)
	}
}

func TestH1Engine_CheckpointWireForm(t *testing.T) {
	const response = "HTTP/1.1 200 OK\r\ntransfer-encoding: chunked\r\nx-Case: k\r\n\r\n2\r\nok\r\n0\r\nX-Sum: 1\r\n\r\n"

	t.Run("should show the wire form and change no bytes when an edit keeps it", func(t *testing.T) {
		origin := newBodyOrigin(t, response)
		proxy, addr, captured := serveCheckpointProxy(t)
		conn, br := dialProxy(t, addr)

		request := "POST / HTTP/1.1\r\nhost: " + origin.addr + "\r\nTransfer-Encoding: chunked\r\nx-Case: k\r\n\r\n2;e\r\nab\r\n1\r\nc\r\n0\r\nX-T: 1\r\n\r\n"
		io.WriteString(conn, strings.Replace(request, "POST /", "POST http://"+origin.addr+"/", 1))

		shownRequest := "POST / HTTP/1.1\r\nhost: " + origin.addr + "\r\nTransfer-Encoding: chunked\r\nx-Case: k\r\n\r\nabc"
		forwardHeld(t, proxy, shownRequest, shownRequest)
		shownResponse := "HTTP/1.1 200 OK\r\ntransfer-encoding: chunked\r\nx-Case: k\r\n\r\nok"
		forwardHeld(t, proxy, shownResponse, shownResponse)

		readExactly(t, br, response)
		if got := origin.request(t); got != request {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, got)
		}
		stored, storedResponse := storedPair(t, captured)
		if stored != shownRequest || storedResponse != shownResponse {
			t.Fatalf("wanted the stored pair to be what Checkpoint showed\ngot:\n%q\n%q", stored, storedResponse)
		}
	})

	t.Run("should forward a CONNECT held inside a tunnel to the tunnel's origin", func(t *testing.T) {
		const refusal = "HTTP/1.1 405 Not Allowed\r\nContent-Length: 0\r\n\r\n"
		origin := newBodyOrigin(t, refusal)
		proxy, addr, _ := serveCheckpointProxy(t)
		conn, br := dialProxy(t, addr)
		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		if res, err := http.ReadResponse(br, nil); err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("wanted the tunnel opened with a 200\ngot: %v, %v", res, err)
		}

		request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
		io.WriteString(conn, request)
		forwardHeld(t, proxy, request, request)
		forwardHeld(t, proxy, refusal, refusal)

		readExactly(t, br, refusal)
		if got := origin.request(t); got != request {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, got)
		}
	})

	t.Run("should re-chunk an edited chunked body and keep Transfer-Encoding and trailers", func(t *testing.T) {
		origin := newBodyOrigin(t, response)
		proxy, addr, _ := serveCheckpointProxy(t)
		conn, br := dialProxy(t, addr)

		head := "POST / HTTP/1.1\r\nHost: " + origin.addr + "\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(conn, strings.Replace(head, "POST /", "POST http://"+origin.addr+"/", 1)+"3\r\nabc\r\n0\r\nX-T: 1\r\n\r\n")

		forwardHeld(t, proxy, head+"abc", head+"edited")
		shownResponse := "HTTP/1.1 200 OK\r\ntransfer-encoding: chunked\r\nx-Case: k\r\n\r\nok"
		forwardHeld(t, proxy, shownResponse, "HTTP/1.1 200 OK\r\ntransfer-encoding: chunked\r\nx-Case: changed\r\n\r\nnew body")

		readExactly(t, br, "HTTP/1.1 200 OK\r\ntransfer-encoding: chunked\r\nx-Case: changed\r\n\r\n8\r\nnew body\r\n0\r\nX-Sum: 1\r\n\r\n")
		if want := head + "6\r\nedited\r\n0\r\nX-T: 1\r\n\r\n"; origin.request(t) != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, origin.request(t))
		}
	})
}
