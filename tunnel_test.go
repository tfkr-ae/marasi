package marasi

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// tunnelHarness serves a TLS-intercepting proxy whose request chain answers
// every request itself, and records what each chain saw.
type tunnelHarness struct {
	proxy *Proxy
	mu    sync.Mutex
	seen  []string // "request GET", "response CONNECT", ...
	self  []bool   // isSelfClient of each request the request chain saw
}

func startTunnelHarness(t *testing.T, configure func(*Proxy)) *tunnelHarness {
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
		WithTLS(),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithBasePipeline(),
	)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	h := &tunnelHarness{proxy: proxy}
	proxy.AddRequestPairModifier(func(_ *Proxy, pair *Pair) error {
		h.mu.Lock()
		h.seen = append(h.seen, "request "+pair.Request.Method)
		h.self = append(h.self, isSelfClient(pair))
		h.mu.Unlock()
		pair.Respond(newResponse(pair.Request, http.StatusOK, []byte("inside")))
		return nil
	})
	proxy.AddResponsePairModifier(func(_ *Proxy, pair *Pair) error {
		h.mu.Lock()
		h.seen = append(h.seen, "response "+pair.Request.Method)
		h.mu.Unlock()
		return nil
	})
	if configure != nil {
		configure(proxy)
	}
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
	return h
}

func (h *tunnelHarness) observed() ([]string, []bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.seen...), append([]bool(nil), h.self...)
}

// connect opens a client connection and sends a CONNECT for target, with the
// proxy's own credentials when internal. It returns the connection, its reader
// and the CONNECT response, or the error that reading it ended in.
func (h *tunnelHarness) connect(t *testing.T, target string, internal bool) (net.Conn, *bufio.Reader, *http.Response, error) {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort(h.proxy.Addr, h.proxy.Port))
	if err != nil {
		t.Fatalf("dialling proxy: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	auth := ""
	if internal {
		auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("marasi:"+h.proxy.clientWorkToken)) + "\r\n"
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", target, target, auth); err != nil {
		t.Fatalf("writing CONNECT: %v", err)
	}
	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	return conn, reader, res, err
}

// get sends a GET for host's root inside an open tunnel and returns the body.
func (h *tunnelHarness) get(t *testing.T, conn net.Conn, host string) string {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(h.proxy.Cert)
	tlsConn := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: host})
	if _, err := fmt.Fprintf(tlsConn, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", host); err != nil {
		t.Fatalf("writing request inside the tunnel: %v", err)
	}
	res, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
	if err != nil {
		t.Fatalf("reading response inside the tunnel: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body inside the tunnel: %v", err)
	}
	return string(body)
}

func TestProxy_TunnelAdmission(t *testing.T) {
	t.Run("should open an admitted tunnel without showing the CONNECT to either chain", func(t *testing.T) {
		h := startTunnelHarness(t, nil)
		conn, _, res, err := h.connect(t, "example.test:443", false)
		if err != nil {
			t.Fatalf("reading CONNECT response: %v", err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT status: wanted 200, got %d", res.StatusCode)
		}
		if body := h.get(t, conn, "example.test"); body != "inside" {
			t.Fatalf("body inside the tunnel: wanted %q, got %q", "inside", body)
		}
		seen, _ := h.observed()
		if len(seen) != 1 || seen[0] != "request GET" {
			t.Fatalf("chains saw %v, wanted only the GET inside the tunnel", seen)
		}
	})

	t.Run("should open a tunnel to a host out of scope, as before", func(t *testing.T) {
		h := startTunnelHarness(t, func(proxy *Proxy) { proxy.Scope.SetDefaultAllow(false) })
		_, _, res, err := h.connect(t, "example.test:443", false)
		if err != nil {
			t.Fatalf("reading CONNECT response: %v", err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT status: wanted 200, got %d", res.StatusCode)
		}
	})

	t.Run("should open a tunnel to the proxy's own listener, as before", func(t *testing.T) {
		h := startTunnelHarness(t, nil)
		_, _, res, err := h.connect(t, net.JoinHostPort(h.proxy.Addr, h.proxy.Port), false)
		if err != nil {
			t.Fatalf("reading CONNECT response: %v", err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT status: wanted 200, got %d", res.StatusCode)
		}
	})

	t.Run("should close the connection when project admission refuses the tunnel", func(t *testing.T) {
		h := startTunnelHarness(t, func(proxy *Proxy) {
			proxy.admitWork = func(context.Context, bool) (func(), uint64, error) {
				return nil, 0, errors.New("project closing")
			}
		})
		_, _, res, err := h.connect(t, "example.test:443", false)
		if err == nil {
			t.Fatalf("CONNECT answered with %d, wanted the connection closed", res.StatusCode)
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("reading CONNECT response: wanted EOF, got %v", err)
		}
		if seen, _ := h.observed(); len(seen) != 0 {
			t.Fatalf("chains saw %v, wanted nothing", seen)
		}
	})

	t.Run("should recognise the proxy's own client for every request in its tunnel", func(t *testing.T) {
		h := startTunnelHarness(t, nil)
		conn, _, res, err := h.connect(t, "example.test:443", true)
		if err != nil {
			t.Fatalf("reading CONNECT response: %v", err)
		}
		res.Body.Close()
		h.get(t, conn, "example.test")

		other, _, res, err := h.connect(t, "example.test:443", false)
		if err != nil {
			t.Fatalf("reading second CONNECT response: %v", err)
		}
		res.Body.Close()
		h.get(t, other, "example.test")

		seen, self := h.observed()
		if len(self) != 2 || !self[0] || self[1] {
			t.Fatalf("self-client per request: wanted [true false], got %v (chains saw %v)", self, seen)
		}
	})
}
