package marasi

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// originSwitch is an origin's 101, with its field names in odd case and order.
const originSwitch = "HTTP/1.1 101 Switching Protocols\r\nupgrade: websocket\r\nX-odd-CASE:  kept\r\nconnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"

// webSocketOrigin answers an upgrade request with originSwitch, then echoes
// every frame until a close frame. It reports each connection that ended.
type webSocketOrigin struct {
	addr  string
	ended chan struct{}
}

func newWebSocketOrigin(t *testing.T) *webSocketOrigin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	origin := &webSocketOrigin{addr: l.Addr().String(), ended: make(chan struct{}, 4)}
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

func (origin *webSocketOrigin) serve(conn net.Conn) {
	defer func() { origin.ended <- struct{}{} }()
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(conn, originSwitch); err != nil {
		return
	}
	for {
		frame, err := marasiws.ReadFrame(br)
		if err != nil || frame.Opcode == marasiws.OpClose {
			// Wait for the far side to close, so ended reports
			// the proxy closing the connection.
			io.Copy(io.Discard, br)
			return
		}
		if err := marasiws.WriteFrame(conn, frame, false); err != nil {
			return
		}
	}
}

// serveH1EngineWebSocketProxy serves a proxy on the new engine with the base
// pipeline, capture, WebSocketPrepareModifier and the relay, and any response
// modifiers in after, which run after Prepare.
func serveH1EngineWebSocketProxy(t *testing.T, after ...PairModifier) (*Proxy, string, *capturedPair) {
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
	proxy.AddResponsePairModifier(WebSocketPrepareModifier)
	for _, modifier := range after {
		proxy.AddResponsePairModifier(modifier)
	}
	proxy.AddResponsePairModifier(WriteResponseModifier)
	proxy.SetUpgradeHandler(WebSocketUpgradeHandler)
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

// upgradeRequest is a WebSocket upgrade for origin, in absolute form.
func upgradeRequest(origin string) string {
	return fmt.Sprintf("GET http://%s/socket HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", origin, origin)
}

// waitEnded waits for the origin to report a connection that ended.
func waitEnded(t *testing.T, origin *webSocketOrigin, why string) {
	t.Helper()
	select {
	case <-origin.ended:
	case <-time.After(5 * time.Second):
		t.Fatalf("the origin connection stayed open: %s", why)
	}
}

func TestH1Engine_WebSocket(t *testing.T) {
	t.Run("should send the client the origin's 101 as read, store it so, and relay frames", func(t *testing.T) {
		origin := newWebSocketOrigin(t)
		_, addr, captured := serveH1EngineWebSocketProxy(t)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, upgradeRequest(origin.addr))
		readExactly(t, br, originSwitch)
		select {
		case res := <-captured.response:
			if string(res.Raw) != originSwitch {
				t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", originSwitch, res.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}

		sent := marasiws.Frame{Fin: true, Opcode: marasiws.OpText, Payload: []byte("hello")}
		if err := marasiws.WriteFrame(conn, sent, true); err != nil {
			t.Fatalf("writing a frame: %v", err)
		}
		got, err := marasiws.ReadFrame(br)
		if err != nil {
			t.Fatalf("reading the echoed frame: %v", err)
		}
		if got.Opcode != marasiws.OpText || string(got.Payload) != "hello" {
			t.Fatalf("wanted the echoed text frame %q\ngot: opcode %d %q", "hello", got.Opcode, got.Payload)
		}
	})

	t.Run("should close an active WebSocket connection when the proxy closes", func(t *testing.T) {
		origin := newWebSocketOrigin(t)
		proxy, addr, _ := serveH1EngineWebSocketProxy(t)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, upgradeRequest(origin.addr))
		readExactly(t, br, originSwitch)
		// A frame through the relay, so the connection is registered.
		if err := marasiws.WriteFrame(conn, marasiws.Frame{Fin: true, Opcode: marasiws.OpText, Payload: []byte("x")}, true); err != nil {
			t.Fatalf("writing a frame: %v", err)
		}
		if _, err := marasiws.ReadFrame(br); err != nil {
			t.Fatalf("reading the echoed frame: %v", err)
		}

		closed := make(chan error, 1)
		go func() { closed <- proxy.Close() }()
		for {
			frame, err := marasiws.ReadFrame(br)
			if err != nil {
				break
			}
			if frame.Opcode == marasiws.OpClose {
				continue
			}
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("closing the proxy waited on the WebSocket connection")
		}
		waitEnded(t, origin, "after the proxy closed")
	})

	t.Run("should close the origin connection of a 101 the response chain drops", func(t *testing.T) {
		origin := newWebSocketOrigin(t)
		drop := func(_ *Proxy, pair *Pair) error { pair.Drop(); return nil }
		_, addr, _ := serveH1EngineWebSocketProxy(t, drop)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, upgradeRequest(origin.addr))
		if _, err := br.ReadByte(); err != io.EOF {
			t.Fatalf("wanted the client connection closed, got: %v", err)
		}
		waitEnded(t, origin, "after a dropped 101")
	})
}
