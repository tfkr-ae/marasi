package marasi

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

// capturedProtocols waits for the next captured pair and returns its
// request's client and origin protocols and its response's origin protocol.
func capturedProtocols(t *testing.T, captured *capturedPair) string {
	t.Helper()
	var req domain.ProxyRequest
	var res domain.ProxyResponse
	select {
	case req = <-captured.request:
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not captured")
	}
	select {
	case res = <-captured.response:
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
	}
	return req.ClientProtocol + " " + req.OriginProtocol + " " + res.OriginProtocol
}

func TestPairProtocols(t *testing.T) {
	t.Run("should capture an HTTP/2 pair with h2 on both legs", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		_, port, _ := net.SplitHostPort(origin.addr)
		authority := net.JoinHostPort("localhost", port)
		client, _ := dialH2(t, addr, authority, "localhost", ca, "h2", "http/1.1")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", authority, ":path", "/")
		id := upstream.nthStream(t, 0)
		upstream.headers(id, true, ":status", "204")
		client.stream(t, 1)

		if got := capturedProtocols(t, captured); got != "h2 h2 h2" {
			t.Fatalf("\nwanted:\nh2 h2 h2\ngot:\n%s", got)
		}
	})

	t.Run("should capture a cleartext prior-knowledge (h2c) pair as h2 on both legs", func(t *testing.T) {
		origin := newH2COrigin(t)
		addr, captured := serveH1EngineProxy(t)
		client := priorKnowledgeThroughConnect(t, addr)

		res, err := client.Get("http://" + origin + "/")
		if err != nil {
			t.Fatalf("sending through the tunnel: %v", err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()

		if got := capturedProtocols(t, captured); got != "h2 h2 h2" {
			t.Fatalf("\nwanted:\nh2 h2 h2\ngot:\n%s", got)
		}
	})

	t.Run("should capture an HTTPS HTTP/1.1 pair with http/1.1 on both legs", func(t *testing.T) {
		origin := newRawTLSOrigin(t, originResponse)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		_, port, _ := net.SplitHostPort(origin.addr)
		authority := net.JoinHostPort("localhost", port)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, authority)
		tlsConn, tbr := clientTLS(t, conn, br, ca, "localhost", "h2", "http/1.1")

		io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
		readExactly(t, tbr, originResponse)

		if got := capturedProtocols(t, captured); got != "http/1.1 http/1.1 http/1.1" {
			t.Fatalf("\nwanted:\nhttp/1.1 http/1.1 http/1.1\ngot:\n%s", got)
		}
	})

	t.Run("should capture a plain HTTP/1.1 pair with http/1.1 on both legs", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, originResponse) })
		addr, captured := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		readExactly(t, br, originResponse)

		if got := capturedProtocols(t, captured); got != "http/1.1 http/1.1 http/1.1" {
			t.Fatalf("\nwanted:\nhttp/1.1 http/1.1 http/1.1\ngot:\n%s", got)
		}
	})
}
