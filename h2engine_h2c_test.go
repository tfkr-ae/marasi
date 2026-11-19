package marasi

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// newH2COrigin serves cleartext HTTP/2 by prior knowledge alone, as a gRPC
// server without TLS does. It answers with the protocol and the request's
// method, path, x-test field and body.
func newH2COrigin(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Protocols: protocols,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("x-origin", "h2c")
			fmt.Fprintf(w, "%s %s %s x-test=%s body=%s", r.Proto, r.Method, r.URL.RequestURI(), r.Header.Get("X-Test"), body)
		}),
	}
	go server.Serve(l)
	t.Cleanup(func() { server.Close() })
	return l.Addr().String()
}

// priorKnowledgeThroughConnect is a client that speaks HTTP/2 by prior
// knowledge inside a plain CONNECT tunnel through the proxy at proxyAddr, as
// grpc-go does with a proxy for a target without TLS.
func priorKnowledgeThroughConnect(t *testing.T, proxyAddr string) *http.Client {
	t.Helper()
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, authority string, _ *tls.Config) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddr)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
			br := bufio.NewReader(conn)
			res, err := http.ReadResponse(br, nil)
			if err != nil || res.StatusCode != http.StatusOK {
				conn.Close()
				return nil, fmt.Errorf("the tunnel was not opened: %v, %v", res, err)
			}
			return &bufferedConn{Conn: conn, reader: br}, nil
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func TestH2Engine_PriorKnowledge(t *testing.T) {
	t.Run("should carry a prior-knowledge client inside a plain CONNECT to an h2c origin over HTTP/2 and store the pair in the text form", func(t *testing.T) {
		origin := newH2COrigin(t)
		addr, captured := serveH1EngineProxy(t)
		client := priorKnowledgeThroughConnect(t, addr)

		req, _ := http.NewRequest(http.MethodPost, "http://"+origin+"/svc.Echo/Say?x=1", strings.NewReader("ping"))
		req.Header.Set("X-Test", "one")
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("sending through the tunnel: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		want := "HTTP/2.0 POST /svc.Echo/Say?x=1 x-test=one body=ping"
		if res.StatusCode != http.StatusOK || res.ProtoMajor != 2 || string(body) != want || res.Header.Get("X-Origin") != "h2c" {
			t.Fatalf("the client's response\nwanted:\nHTTP/2 200 %q from the origin\ngot:\n%s %d %q %v", want, res.Proto, res.StatusCode, body, res.Header)
		}
		select {
		case stored := <-captured.request:
			raw := string(stored.Raw)
			if !strings.HasPrefix(raw, "POST /svc.Echo/Say?x=1 HTTP/2\r\n") || !strings.Contains(raw, "\r\n:scheme: http\r\n") || !strings.Contains(raw, "\r\n:authority: "+origin+"\r\n") || !strings.HasSuffix(raw, "\r\n\r\nping") || stored.Scheme != "http" {
				t.Fatalf("the stored request\nwanted:\nthe HTTP/2 text form, scheme http\ngot:\n%q, scheme %q", stored.Raw, stored.Scheme)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
	})

	t.Run("should carry prior knowledge sent straight to the listener to the origin its :authority names, over HTTP/2", func(t *testing.T) {
		origin := newH2COrigin(t)
		addr, _ := serveH1EngineProxy(t)
		// What curl --http2-prior-knowledge sends when its origin's name
		// resolves to the listener.
		conn, _ := dialProxy(t, addr)
		io.WriteString(conn, http2.ClientPreface)
		client := newH2TestPeer(conn)
		client.write(func(f *http2.Framer) error { return f.WriteSettings() })
		go client.read()

		for _, id := range []uint32{1, 3} {
			client.headers(id, true, ":method", "GET", ":scheme", "http", ":authority", origin, ":path", fmt.Sprintf("/%d", id), "x-test", "two")
			response := client.stream(t, id)
			want := fmt.Sprintf("HTTP/2.0 GET /%d x-test=two body=", id)
			if len(response.headers) != 1 || string(response.data) != want {
				t.Fatalf("stream %d\nwanted:\n%q from the origin\ngot:\n%v %q", id, want, response.headers, response.data)
			}
			if status, _ := response.headers[0].get(":status"); status != "200" {
				t.Fatalf("stream %d\nwanted:\n200\ngot:\n%v", id, response.headers)
			}
		}
	})

	t.Run("should still read an HTTP/1.1 request shorter than the preface on the listener", func(t *testing.T) {
		origin := newRawOrigin(t, originResponse)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)
		// "GET / HTTP/1.0" is shorter than the preface; with Host in the
		// absolute form only, it must not be waited on for more bytes.
		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.0\r\n\r\n", origin.addr)
		res, err := http.ReadResponse(br, nil)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("the HTTP/1.0 request\nwanted:\nthe origin's 200\ngot:\n%v, %v", res, err)
		}
	})
}
