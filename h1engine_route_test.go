package marasi

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/upstreamtest"
)

// routedH1Engine is a proxy on the engine whose config directory lists
// upstream proxies, with what it stored and logged.
type routedH1Engine struct {
	proxy    *Proxy
	addr     string
	captured *capturedPair
	logs     chan domain.Log
}

// serveRoutedH1Engine serves the engine with the base pipeline and the
// setup, filter and write modifiers, with entries listed as upstream proxies.
func serveRoutedH1Engine(t *testing.T, entries ...UpstreamProxyConfig) *routedH1Engine {
	t.Helper()
	configDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(database)
	h := &routedH1Engine{
		captured: &capturedPair{request: make(chan domain.ProxyRequest, 16), response: make(chan domain.ProxyResponse, 16)},
		logs:     make(chan domain.Log, 16),
	}
	h.proxy, err = New(
		WithLogger(logger),
		WithConfigDir(configDir),
		WithDefaultRepositories(repository),
		WithLogHandler(func(log domain.Log) error { h.logs <- log; return nil }),
		WithRequestHandler(func(req domain.ProxyRequest) error { h.captured.request <- req; return nil }),
		WithResponseHandler(func(res domain.ProxyResponse) error { h.captured.response <- res; return nil }),
		WithBasePipeline(),
	)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	cfg, err := ReadConfig(configDir)
	if err != nil {
		h.proxy.Close()
		t.Fatalf("reading the config: %v", err)
	}
	for _, entry := range entries {
		if err := cfg.AddUpstreamProxy(entry); err != nil {
			h.proxy.Close()
			t.Fatalf("adding upstream proxy %s: %v", entry.Name, err)
		}
	}
	h.proxy.AddRequestPairModifier(SetupRequestModifier)
	h.proxy.AddRequestPairModifier(WriteRequestModifier)
	h.proxy.AddResponsePairModifier(ResponseFilterModifier)
	h.proxy.AddResponsePairModifier(WriteResponseModifier)
	listener, err := h.proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		h.proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- h.proxy.Serve(listener) }()
	t.Cleanup(func() {
		h.proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})
	h.addr = net.JoinHostPort(h.proxy.Addr, h.proxy.Port)
	return h
}

// storedRoute waits for the next stored response and returns its route.
func (h *routedH1Engine) storedRoute(t *testing.T) *domain.RouteSnapshot {
	t.Helper()
	select {
	case stored := <-h.captured.response:
		return stored.Route
	case <-time.After(5 * time.Second):
		t.Fatal("no response was stored")
		return nil
	}
}

// closingOrigin answers every request head with a 200 and reports each
// connection the far side closed.
type closingOrigin struct {
	addr   string
	closed chan struct{}
}

func newClosingOrigin(t *testing.T) *closingOrigin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	origin := &closingOrigin{addr: l.Addr().String(), closed: make(chan struct{}, 16)}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						origin.closed <- struct{}{}
						return
					}
					if line == "\r\n" {
						io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
					}
				}
			}()
		}
	}()
	return origin
}

const routedOK = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"

func TestH1Engine_Routes(t *testing.T) {
	t.Run("should carry a later request on a client connection over the new route after a swap, closing the old route's idle connection", func(t *testing.T) {
		origin := newClosingOrigin(t)
		first := upstreamtest.NewHTTP(t)
		second := upstreamtest.NewHTTP(t)
		h := serveRoutedH1Engine(t,
			UpstreamProxyConfig{Name: "first", URL: first.URL("http", "", "")},
			UpstreamProxyConfig{Name: "second", URL: second.URL("http", "", "")},
		)
		h.proxy.SelectUpstreamProxy("first")
		conn, br := dialProxy(t, h.addr)
		request := fmt.Sprintf("GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		io.WriteString(conn, request)
		readExactly(t, br, routedOK)
		if route := h.storedRoute(t); route == nil || route.Name != "first" {
			t.Fatalf("wanted the first pair stored with route first\ngot: %+v", route)
		}

		h.proxy.SelectUpstreamProxy("second")
		select {
		case <-origin.closed:
		case <-time.After(5 * time.Second):
			t.Fatal("wanted the swap to close the old route's idle origin connection")
		}

		io.WriteString(conn, request)
		readExactly(t, br, routedOK)
		if route := h.storedRoute(t); route == nil || route.Name != "second" || route.Address != second.URL("http", "", "") {
			t.Fatalf("wanted the second pair stored with route second at %s\ngot: %+v", second.URL("http", "", ""), route)
		}
		if got := first.Targets(); len(got) != 1 {
			t.Fatalf("wanted one tunnel through the first upstream proxy\ngot: %v", got)
		}
		if got := second.Targets(); len(got) != 1 || got[0] != origin.addr {
			t.Fatalf("wanted one tunnel to %s through the second upstream proxy\ngot: %v", origin.addr, got)
		}
		if sent := string(first.Tunnels()[0].Sent()); strings.Count(sent, "GET ") != 1 {
			t.Fatalf("wanted one request through the first upstream proxy\ngot:\n%s", sent)
		}
	})

	t.Run("should take a route selected between two requests on the same client connection", func(t *testing.T) {
		origin := newRawOrigin(t, routedOK)
		first := upstreamtest.NewHTTP(t)
		h := serveRoutedH1Engine(t, UpstreamProxyConfig{Name: "first", URL: first.URL("http", "", "")})
		conn, br := dialProxy(t, h.addr)
		request := fmt.Sprintf("GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		io.WriteString(conn, request)
		readExactly(t, br, routedOK)
		if route := h.storedRoute(t); route == nil || route.Name != "" {
			t.Fatalf("wanted the direct pair stored with an empty route\ngot: %+v", route)
		}
		h.proxy.SelectUpstreamProxy("first")
		io.WriteString(conn, request)
		readExactly(t, br, routedOK)
		if route := h.storedRoute(t); route == nil || route.Name != "first" {
			t.Fatalf("wanted the pair after the selection stored with route first\ngot: %+v", route)
		}
		if _, connections := origin.received(); connections != 2 {
			t.Fatalf("wanted a new origin connection on the new route\ngot: %d connections", connections)
		}
	})

	t.Run("should answer a refusing upstream proxy with a 502 and one proxy log naming the pair, the upstream proxy and the cause", func(t *testing.T) {
		origin := newRawOrigin(t, routedOK)
		refusing := upstreamtest.NewHTTP(t, upstreamtest.WithBehaviour(upstreamtest.Refuse))
		h := serveRoutedH1Engine(t, UpstreamProxyConfig{Name: "refusing", URL: refusing.URL("http", "", "")})
		h.proxy.SelectUpstreamProxy("refusing")
		conn, br := dialProxy(t, h.addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "refusing") {
			t.Fatalf("wanted a 502 naming the upstream proxy\ngot: %d %q", res.StatusCode, body)
		}
		route := h.storedRoute(t)
		if route == nil || route.Name != "refusing" || route.Address != refusing.URL("http", "", "") {
			t.Fatalf("wanted the pair stored with the route it tried\ngot: %+v", route)
		}
		var errors []domain.Log
		deadline := time.After(time.Second)
	collect:
		for {
			select {
			case log := <-h.logs:
				if log.Level == "ERROR" {
					errors = append(errors, log)
				}
			case <-deadline:
				break collect
			}
		}
		if len(errors) != 1 {
			t.Fatalf("wanted one ERROR proxy log\ngot: %+v", errors)
		}
		context := errors[0].Context
		if errors[0].RequestID == nil || context["upstream_proxy"] != "refusing" || context["cause"] == nil || context["cause"] == "" {
			t.Fatalf("wanted the log to name the pair, the upstream proxy and the cause\ngot: %+v", errors[0])
		}
		if _, connections := origin.received(); connections != 0 {
			t.Fatalf("wanted nothing to reach the origin\ngot: %d connections", connections)
		}
	})
}
