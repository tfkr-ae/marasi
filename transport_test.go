package marasi

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newDirectDialer builds the origin dialer as Serve does, on a route that is
// always direct. insecure trusts a self-signed origin.
func newDirectDialer(dial func(context.Context, string, string) (net.Conn, error), insecure bool) *originDialer {
	direct := DirectRoute()
	always := func() *Route { return direct }
	dialer := newOriginDialer(always, always, dial)
	dialer.insecureSkipVerify = insecure
	return dialer
}

func TestH1Engine_OriginTLS(t *testing.T) {
	t.Run("should speak HTTP/1.1 to a TLS origin that also offers h2", func(t *testing.T) {
		protos := make(chan string, 1)
		origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			protos <- r.Proto
			io.WriteString(w, "marasi tls")
		}))
		origin.EnableHTTP2 = true
		origin.StartTLS()
		t.Cleanup(origin.Close)
		authority := strings.TrimPrefix(origin.URL, "https://")
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, authority)
		tlsConn, tlsReader := clientTLS(t, conn, br, ca, "127.0.0.1")

		fmt.Fprintf(tlsConn, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", authority)
		res, err := http.ReadResponse(tlsReader, nil)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		proto := <-protos

		if res.StatusCode != http.StatusOK || string(body) != "marasi tls" || proto != "HTTP/1.1" {
			t.Fatalf("\nwanted:\n200 %q over HTTP/1.1\ngot:\n%d %q over %q", "marasi tls", res.StatusCode, body, proto)
		}
	})

	t.Run("should answer a TLS origin on a closed port with a 502 naming the refusal", func(t *testing.T) {
		authority := closedAddress(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, authority)
		tlsConn, tlsReader := clientTLS(t, conn, br, ca, "127.0.0.1")

		fmt.Fprintf(tlsConn, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", authority)
		res, err := http.ReadResponse(tlsReader, nil)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		body, _ := io.ReadAll(res.Body)

		if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "connection refused") {
			t.Fatalf("\nwanted:\n502 naming %q\ngot:\n%d %q", "connection refused", res.StatusCode, body)
		}
	})
}
