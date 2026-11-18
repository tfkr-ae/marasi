package marasi

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// scriptedOrigin accepts one connection, reads one whole request with the
// codec and hands the connection to its script. Each request it read, head
// and body exactly as they arrived, goes to requests.
type scriptedOrigin struct {
	addr     string
	requests chan string
}

func newScriptedOrigin(t *testing.T, script func(conn net.Conn)) *scriptedOrigin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	origin := &scriptedOrigin{addr: l.Addr().String(), requests: make(chan string, 4)}
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
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
		origin.requests <- raw.String()
		script(conn)
	}()
	return origin
}

// received waits for the request the origin read.
func (origin *scriptedOrigin) received(t *testing.T) string {
	t.Helper()
	select {
	case request := <-origin.requests:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("the origin received no request")
		return ""
	}
}

// serveHoldingH1EngineProxy serves a proxy on the engine like
// serveH1EngineProxy, with Checkpoint holding every request.
func serveHoldingH1EngineProxy(t *testing.T) (string, *Proxy) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(database)
	proxy, err := New(
		WithLogger(logger),
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithRequestHandler(func(domain.ProxyRequest) error { return nil }),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithBasePipeline(),
	)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	checkpoint := testExtensions["checkpoint"]
	if err := proxy.WithOptions(WithExtension(&domain.Extension{
		ID: checkpoint.ID, Name: checkpoint.Name, LuaContent: checkpoint.LuaContent, Enabled: true,
	})); err != nil {
		t.Fatalf("loading Checkpoint: %v", err)
	}
	proxy.SetIntercept(true)
	proxy.AddRequestPairModifier(SetupRequestModifier)
	proxy.AddRequestPairModifier(CheckpointRequestModifier)
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
	return net.JoinHostPort(proxy.Addr, proxy.Port), proxy
}

// expectCapturedResponse waits for the stored response and compares it.
func expectCapturedResponse(t *testing.T, captured *capturedPair, want string) {
	t.Helper()
	select {
	case res := <-captured.response:
		if string(res.Raw) != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, res.Raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
	}
}

const (
	earlyHints = "HTTP/1.1 103 Early Hints\r\nlink: </style.css>; rel=preload\r\n\r\n"
	finalOK    = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
)

func TestH1Engine_InterimResponses(t *testing.T) {
	t.Run("should relay a 103 to the client before the origin sends the final response", func(t *testing.T) {
		release := make(chan struct{})
		origin := newScriptedOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, earlyHints)
			<-release
			io.WriteString(conn, finalOK)
		})
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/hints HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		readExactly(t, br, earlyHints)
		close(release)
		readExactly(t, br, finalOK)
		expectCapturedResponse(t, captured, finalOK)
	})

	t.Run("should remove the hop-by-hop fields of a relayed 103", func(t *testing.T) {
		const hints = "HTTP/1.1 103 Early Hints\r\nConnection: X-Hint\r\nX-Hint: 1\r\nlink: </style.css>; rel=preload\r\n\r\n"
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, hints+finalOK) })
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/hints HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		readExactly(t, br, earlyHints+finalOK)
	})

	t.Run("should relay a 102 and a 103 in the order the origin sent them", func(t *testing.T) {
		processing := "HTTP/1.1 102 Processing\r\n\r\n"
		origin := newScriptedOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, processing+earlyHints+finalOK)
		})
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/slow HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		readExactly(t, br, processing+earlyHints+finalOK)
	})

	t.Run("should not relay a 1xx to an HTTP/1.0 client", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, earlyHints+finalOK)
		})
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/old HTTP/1.0\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		readExactly(t, br, finalOK)
	})
}

func TestH1Engine_Trailers(t *testing.T) {
	t.Run("should pass a chunked response's Trailer field and trailers to the client unchanged", func(t *testing.T) {
		response := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\ntrailer: X-Checksum\r\n\r\n" +
			"5\r\nhello\r\n0\r\nx-CHECKSUM:  abc \r\n\r\n"
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, response) })
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/sum HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		readExactly(t, br, response)
		select {
		case res := <-captured.response:
			if !strings.Contains(string(res.Raw), "trailer: X-Checksum\r\n") {
				t.Fatalf("wanted the stored response to keep the Trailer field\ngot:\n%q", res.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
	})

	t.Run("should pass a chunked request's Trailer field and trailers to the origin unchanged", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, finalOK) })
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		rest := " HTTP/1.1\r\nHost: " + origin.addr + "\r\nTransfer-Encoding: chunked\r\nTrailer: x-digest\r\n\r\n" +
			"3;ext=1\r\nabc\r\n0\r\nX-Digest: sha-256=x\r\n\r\n"
		fmt.Fprintf(conn, "POST http://%s/upload%s", origin.addr, rest)
		readExactly(t, br, finalOK)
		if got, want := origin.received(t), "POST /upload"+rest; got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
	})
}

// continueHead is the request head of an Expect: 100-continue upload, whose
// client waits for a 100 before it sends the body.
func continueHead(origin string) string {
	return "POST http://" + origin + "/upload HTTP/1.1\r\nHost: " + origin +
		"\r\nExpect: 100-continue\r\nContent-Length: 5\r\n\r\n"
}

const marasiContinue = "HTTP/1.1 100 Continue\r\n\r\n"

func TestH1Engine_ExpectContinue(t *testing.T) {
	t.Run("should answer 100 itself so Checkpoint holds the full body before anything reaches the origin", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, finalOK) })
		addr, proxy := serveHoldingH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, continueHead(origin.addr))
		readExactly(t, br, marasiContinue)
		io.WriteString(conn, "hello")

		items := waitForCheckpoint(t, proxy, 1)
		if raw := string(items[0].Raw); !strings.HasSuffix(raw, "\r\n\r\nhello") || !strings.Contains(raw, "Expect: 100-continue") {
			t.Fatalf("wanted Checkpoint to hold the request with Expect and its full body\ngot:\n%q", raw)
		}
		select {
		case request := <-origin.requests:
			t.Fatalf("wanted nothing at the origin while Checkpoint holds the request\ngot:\n%q", request)
		case <-time.After(100 * time.Millisecond):
		}
		if err := proxy.ForwardCheckpoint(items[0].ID, CheckpointForward{}); err != nil {
			t.Fatalf("forwarding the held request: %v", err)
		}
		readExactly(t, br, finalOK)
		want := "POST /upload HTTP/1.1\r\nHost: " + origin.addr + "\r\nExpect: 100-continue\r\nContent-Length: 5\r\n\r\nhello"
		if got := origin.received(t); got != want {
			t.Fatalf("wanted the origin to receive the request with Expect kept:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should not relay the origin's 100 after Marasi's own", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, marasiContinue+finalOK) })
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, continueHead(origin.addr))
		readExactly(t, br, marasiContinue)
		io.WriteString(conn, "hello")
		readExactly(t, br, finalOK)
		expectCapturedResponse(t, captured, finalOK)
	})

	t.Run("should relay a 417 the origin sends instead of continuing", func(t *testing.T) {
		refused := "HTTP/1.1 417 Expectation Failed\r\nContent-Length: 0\r\n\r\n"
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, refused) })
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, continueHead(origin.addr))
		readExactly(t, br, marasiContinue)
		io.WriteString(conn, "hello")
		readExactly(t, br, refused)
	})

	t.Run("should not answer 100 to an HTTP/1.0 request", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, finalOK) })
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "POST http://%s/old HTTP/1.0\r\nHost: %s\r\nExpect: 100-continue\r\nContent-Length: 5\r\n\r\nhello", origin.addr, origin.addr)
		readExactly(t, br, finalOK)
	})
}
