package marasi

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// newRawTLSOrigin is a rawOrigin served over TLS with a certificate no
// client trusts, so only a proxy that skips origin verification reaches it.
func newRawTLSOrigin(t *testing.T, response string) *rawOrigin {
	t.Helper()
	issuer, _ := newTestLeafIssuer(t, 24*time.Hour)
	l, err := tls.Listen("tcp", "127.0.0.1:0", issuer.config("127.0.0.1:443"))
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

// serveH1EngineMITMProxy serves the new engine as serveH1EngineProxy does,
// with the proxy's CA loaded. trustOrigin makes the origin leg trust any
// certificate.
// It returns the listener's address, the captures and the CA.
func serveH1EngineMITMProxy(t *testing.T, trustOrigin bool) (string, *capturedPair, *x509.Certificate) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(database)
	captured := &capturedPair{request: make(chan domain.ProxyRequest, 16), response: make(chan domain.ProxyResponse, 16)}
	proxy, err := New(
		withH1Engine(),
		WithLogger(logger),
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithTLS(),
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
	// As Serve does, with the origin leg the test asks for.
	proxy.setRoundTripper(newDirectTransport(proxy.dialTransport, trustOrigin))
	proxy.dbWriterStarted.Store(true)
	go proxy.WriteToDB()
	serveResult := make(chan error, 1)
	go func() { serveResult <- proxy.engine.serve(listener) }()
	t.Cleanup(func() {
		listener.Close()
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})
	return net.JoinHostPort(proxy.Addr, proxy.Port), captured, proxy.Cert
}

// openTunnel sends CONNECT for authority and reads the 200.
func openTunnel(t *testing.T, conn net.Conn, br *bufio.Reader, authority string) {
	t.Helper()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("wanted the tunnel opened with a 200\ngot: %v, %v", res, err)
	}
}

// clientTLS runs a TLS handshake over conn, whose unread bytes are in br,
// trusting only ca.
func clientTLS(t *testing.T, conn net.Conn, br *bufio.Reader, ca *x509.Certificate, serverName string, alpn ...string) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	tlsConn := tls.Client(&bufferedConn{Conn: conn, reader: br}, &tls.Config{ServerName: serverName, RootCAs: roots, NextProtos: alpn})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("wanted the client to verify the proxy's leaf against the CA\ngot: %v", err)
	}
	return tlsConn, bufio.NewReader(tlsConn)
}

// expectCaptured waits for the stored pair and compares its raw text.
func expectCaptured(t *testing.T, captured *capturedPair, scheme, request, response string) {
	t.Helper()
	select {
	case req := <-captured.request:
		if req.Scheme != scheme || string(req.Raw) != request {
			t.Fatalf("wanted the stored request:\n%s %q\ngot:\n%s %q", scheme, request, req.Scheme, req.Raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not captured")
	}
	select {
	case res := <-captured.response:
		if string(res.Raw) != response {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", response, res.Raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
	}
}

func TestH1Engine_MITM(t *testing.T) {
	t.Run("should intercept HTTPS inside CONNECT with a leaf the client verifies against the CA, and capture it", func(t *testing.T) {
		origin := newRawTLSOrigin(t, originResponse)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		_, port, _ := net.SplitHostPort(origin.addr)
		authority := net.JoinHostPort("localhost", port)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, authority)

		tlsConn, tbr := clientTLS(t, conn, br, ca, "localhost", "h2", "http/1.1")
		state := tlsConn.ConnectionState()
		if state.NegotiatedProtocol != "http/1.1" {
			t.Errorf("ALPN\nwanted:\nhttp/1.1\ngot:\n%q", state.NegotiatedProtocol)
		}
		leaf := state.PeerCertificates[0]
		if leaf.Subject.CommonName != "localhost" || !slices.Equal(leaf.Subject.Organization, []string{"Marasi"}) {
			t.Errorf("leaf\nwanted:\nCN localhost, O Marasi\ngot:\n%s", leaf.Subject)
		}

		request := "GET /secure HTTP/1.1\r\nhost: " + authority + "\r\nX-Kept-CASE: 1\r\n\r\n"
		io.WriteString(tlsConn, request)
		readExactly(t, tbr, originResponse)
		requests, _ := origin.received()
		if len(requests) != 1 || requests[0] != request {
			t.Fatalf("wanted the origin to receive over TLS:\n%q\ngot:\n%q", request, requests)
		}
		expectCaptured(t, captured, "https", request, originResponse)

		// The connection carries the next request to the same origin
		// connection.
		io.WriteString(tlsConn, request)
		readExactly(t, tbr, originResponse)
		if _, connections := origin.received(); connections != 1 {
			t.Fatalf("wanted one origin connection, got %d", connections)
		}
	})

	t.Run("should name an IP CONNECT host in an IP SAN when the client sends no SNI", func(t *testing.T) {
		origin := newRawTLSOrigin(t, originResponse)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, origin.addr)

		// Go sends no SNI for an IP, and verifies the IP SAN.
		tlsConn, tbr := clientTLS(t, conn, br, ca, "127.0.0.1")
		leaf := tlsConn.ConnectionState().PeerCertificates[0]
		if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
			t.Errorf("IP SAN\nwanted:\n[127.0.0.1]\ngot:\n%v", leaf.IPAddresses)
		}
		request := "GET / HTTP/1.1\r\nHost: " + origin.addr + "\r\n\r\n"
		io.WriteString(tlsConn, request)
		readExactly(t, tbr, originResponse)
		expectCaptured(t, captured, "https", request, originResponse)
	})

	t.Run("should intercept TLS sent straight to the proxy listener", func(t *testing.T) {
		origin := newRawTLSOrigin(t, originResponse)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		conn, br := dialProxy(t, addr)

		tlsConn, tbr := clientTLS(t, conn, br, ca, "direct.example")
		request := "GET /direct HTTP/1.1\r\nHost: " + origin.addr + "\r\n\r\n"
		io.WriteString(tlsConn, request)
		readExactly(t, tbr, originResponse)
		requests, _ := origin.received()
		if len(requests) != 1 || requests[0] != request {
			t.Fatalf("wanted the origin to receive over TLS:\n%q\ngot:\n%q", request, requests)
		}
		expectCaptured(t, captured, "https", request, originResponse)
	})

	t.Run("should answer a TLS origin whose certificate does not verify with a 502", func(t *testing.T) {
		origin := newRawTLSOrigin(t, originResponse)
		addr, captured, ca := serveH1EngineMITMProxy(t, false)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, origin.addr)
		tlsConn, tbr := clientTLS(t, conn, br, ca, "127.0.0.1")

		io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n")
		res, err := http.ReadResponse(tbr, nil)
		if err != nil || res.StatusCode != http.StatusBadGateway {
			t.Fatalf("wanted a 502\ngot: %v, %v", res, err)
		}
		body, _ := io.ReadAll(res.Body)
		if !strings.Contains(string(body), "certificate") {
			t.Fatalf("wanted the cause to name the certificate\ngot: %q", body)
		}
		select {
		case stored := <-captured.response:
			if stored.Metadata["proxy_error"] == nil {
				t.Fatalf("wanted the 502 stored with metadata.proxy_error\ngot: %v", stored.Metadata)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the 502 was not captured")
		}
	})
}
