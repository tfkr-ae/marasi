package marasi

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// newScriptedTLSOrigin is a scriptedOrigin over TLS that offers only
// http/1.1 by ALPN.
func newScriptedTLSOrigin(t *testing.T, script func(conn net.Conn)) *scriptedOrigin {
	t.Helper()
	issuer, _ := newTestLeafIssuer(t, 24*time.Hour)
	config := issuer.config("127.0.0.1:443")
	config.NextProtos = []string{"http/1.1"}
	l, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	return serveScriptedOrigin(t, l, script)
}

// redirectPaths sends each request whose path starts with prefix to addr,
// as a modifier that changes the URL's host does.
func redirectPaths(prefix, addr string) PairModifier {
	return func(_ *Proxy, pair *Pair) error {
		if strings.HasPrefix(pair.Request.URL.Path, prefix) {
			pair.Request.URL.Host = addr
		}
		return nil
	}
}

// pairProtocols records each pair's path with its client and origin protocols.
type pairProtocols chan string

func (p pairProtocols) modifier(_ *Proxy, pair *Pair) error {
	p <- pair.Request.URL.Path + " " + pair.clientProtocol() + " " + pair.originProtocol
	return nil
}

func (p pairProtocols) next(t *testing.T) string {
	t.Helper()
	select {
	case got := <-p:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("no response reached the response chain")
		return ""
	}
}

func TestH2Engine_RedirectedRequest(t *testing.T) {
	t.Run("should translate a request sent to an origin that speaks only HTTP/1.1, and its response back, recording both protocols", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h1Origin := newScriptedTLSOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, "HTTP/1.1 201 Created\r\nContent-Type: text/plain\r\nConnection: keep-alive, X-Hop\r\nX-Hop: 1\r\nX-Origin: h1\r\nTransfer-Encoding: chunked\r\n\r\n4\r\npong\r\n0\r\nX-Sum: 1\r\n\r\n")
		})
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		proxy := proxyOf(t, addr)
		proxy.AddRequestPairModifier(redirectPaths("/elsewhere", h1Origin.addr))
		protocols := make(pairProtocols, 4)
		proxy.AddResponsePairModifier(protocols.modifier)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2", "http/1.1")
		origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/elsewhere?x=1",
			"content-type", "text/plain", "cookie", "a=1", "x-test", "One", "cookie", "b=2")
		client.data(1, true, "ping")

		want := "POST /elsewhere?x=1 HTTP/1.1\r\nhost: " + origin.addr + "\r\ncontent-type: text/plain\r\ncookie: a=1; b=2\r\nx-test: One\r\ncontent-length: 4\r\n\r\nping"
		if got := h1Origin.received(t); got != want {
			t.Fatalf("the HTTP/1.1 origin's request\nwanted:\n%q\ngot:\n%q", want, got)
		}
		response := client.stream(t, 1)
		wantFields := h2Fields{{":status", "201", false}, {"content-type", "text/plain", false}, {"x-origin", "h1", false}}
		wantTrailers := h2Fields{{"x-sum", "1", false}}
		if len(response.headers) != 1 || !equalFields(response.headers[0], wantFields) || string(response.data) != "pong" || !equalFields(response.trailers, wantTrailers) {
			t.Fatalf("the client's response\nwanted:\n%v %q %v\ngot:\n%v %q %v", wantFields, "pong", wantTrailers, response.headers, response.data, response.trailers)
		}
		if got := protocols.next(t); got != "/elsewhere h2 http/1.1" {
			t.Errorf("the pair's protocols\nwanted:\n/elsewhere h2 http/1.1\ngot:\n%s", got)
		}
		expectCaptured(t, captured, "https",
			"POST /elsewhere?x=1 HTTP/2\r\n:scheme: https\r\n:authority: "+origin.addr+"\r\ncontent-type: text/plain\r\ncookie: a=1\r\nx-test: One\r\ncookie: b=2\r\n\r\nping",
			"HTTP/2 201\r\ncontent-type: text/plain\r\nx-origin: h1\r\n\r\npong")
	})

	t.Run("should send a Checkpoint edit whose request line names another origin to that origin, translated for HTTP/1.1", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h1Origin := newScriptedTLSOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		})
		addr, _, ca := serveH1EngineMITMProxy(t, true, holdEverything(t))
		proxy := proxyOf(t, addr)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2", "http/1.1")
		origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/a")

		shown := "GET /a HTTP/2\r\n:scheme: https\r\n:authority: " + origin.addr + "\r\n\r\n"
		edit := "GET https://" + h1Origin.addr + "/moved HTTP/2\r\n:scheme: https\r\n:authority: " + origin.addr + "\r\nx-edit: 1\r\n\r\n"
		forwardHeld(t, proxy, shown, edit)

		want := "GET /moved HTTP/1.1\r\nhost: " + h1Origin.addr + "\r\nx-edit: 1\r\n\r\n"
		if got := h1Origin.received(t); got != want {
			t.Fatalf("the HTTP/1.1 origin's request\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should leave the connection's other streams to its origin while a redirected request is in flight", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		release := make(chan struct{})
		h1Origin := newScriptedTLSOrigin(t, func(conn net.Conn) {
			<-release
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nlate")
		})
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		proxyOf(t, addr).AddRequestPairModifier(redirectPaths("/elsewhere", h1Origin.addr))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2", "http/1.1")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/elsewhere")
		h1Origin.received(t)
		for i, id := range []uint32{3, 5} {
			client.headers(id, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/here")
			sent := upstream.nthStream(t, i)
			if path, _ := upstream.stream(t, sent).headers[0].get(":path"); path != "/here" {
				t.Fatalf("the origin's stream %d\nwanted:\n/here\ngot:\n%q", sent, path)
			}
			upstream.headers(sent, false, ":status", "200")
			upstream.data(sent, true, "here")
			if response := client.stream(t, id); string(response.data) != "here" {
				t.Fatalf("stream %d, while stream 1 waits\nwanted:\n%q\ngot:\n%v %q", id, "here", response.headers, response.data)
			}
		}
		close(release)
		response := client.stream(t, 1)
		want := h2Fields{{":status", "200", false}, {"content-length", "4", false}}
		if len(response.headers) != 1 || !equalFields(response.headers[0], want) || string(response.data) != "late" {
			t.Fatalf("the redirected stream\nwanted:\n%v %q\ngot:\n%v %q", want, "late", response.headers, response.data)
		}
	})

	t.Run("should offer a redirected request's origin the client's ALPN list", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		other := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		proxyOf(t, addr).AddRequestPairModifier(redirectPaths("/elsewhere", other.addr))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/elsewhere")
		redirected := other.accept(t)
		if offered := <-other.offered; !slices.Equal(offered, []string{"h2"}) {
			t.Fatalf("the redirected origin's ALPN offer\nwanted:\n[h2]\ngot:\n%v", offered)
		}
		redirected.headers(redirected.nthStream(t, 0), true, ":status", "204")
		client.stream(t, 1)
	})

	t.Run("should send a redirected request as it is to an origin that chooses h2, on a connection of its own", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		other := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		protocols := make(pairProtocols, 4)
		proxy := proxyOf(t, addr)
		proxy.AddRequestPairModifier(redirectPaths("/elsewhere", other.addr))
		proxy.AddResponsePairModifier(protocols.modifier)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/elsewhere", "X-Case", "Kept")
		redirected := other.accept(t)
		id := redirected.nthStream(t, 0)
		want := h2Fields{{":method", "GET", false}, {":scheme", "https", false}, {":authority", origin.addr, false}, {":path", "/elsewhere", false}, {"X-Case", "Kept", false}}
		if got := redirected.stream(t, id).headers; len(got) != 1 || !equalFields(got[0], want) {
			t.Fatalf("the other origin's request\nwanted:\n%v\ngot:\n%v", want, got)
		}
		redirected.headers(id, true, ":status", "204")
		if status, _ := client.stream(t, 1).headers[0].get(":status"); status != "204" {
			t.Fatalf("the redirected stream\nwanted:\n204\ngot:\n%v", status)
		}
		if got := protocols.next(t); got != "/elsewhere h2 h2" {
			t.Errorf("the pair's protocols\nwanted:\n/elsewhere h2 h2\ngot:\n%s", got)
		}
		redirected.await(t, "the redirected connection to close", func(p *h2TestPeer) bool { return p.err != nil })
	})
}

// h1Payload is the payload of the HTTP/1.1 request raw, its framing undone.
func h1Payload(t *testing.T, raw string) string {
	t.Helper()
	br := bufio.NewReader(strings.NewReader(raw))
	head, err := h1.ReadRequestHead(br)
	if err != nil {
		t.Fatalf("reading the request head: %v", err)
	}
	framing, err := h1.RequestFraming(head)
	if err != nil {
		t.Fatalf("reading the request framing: %v", err)
	}
	body, err := h1.ReadBody(br, framing)
	if err != nil {
		t.Fatalf("reading the request body: %v", err)
	}
	return string(body.Payload())
}

func TestH2Engine_TranslatedBodyThreshold(t *testing.T) {
	// redirected serves a proxy with threshold that sends /elsewhere to
	// h1Origin, and a client connected to it.
	redirected := func(t *testing.T, h1Origin string, threshold int64) *h2TestPeer {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, withBodyThreshold(threshold))
		proxyOf(t, addr).AddRequestPairModifier(redirectPaths("/elsewhere", h1Origin))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2", "http/1.1")
		origin.accept(t)
		return client
	}

	t.Run("should forward an HTTP/1.1 origin's response body past the threshold to the client as it arrives", func(t *testing.T) {
		release := make(chan struct{})
		h1Origin := newScriptedTLSOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n"+chunk("0123456789")+chunk("abc"))
			<-release
			io.WriteString(conn, chunk("def")+"0\r\nX-Sum: 1\r\n\r\n")
		})
		client := redirected(t, h1Origin.addr, 8)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", "example.test", ":path", "/elsewhere")
		client.awaitData(t, 1, 13, 2*time.Second)
		close(release)
		response := client.stream(t, 1)
		if string(response.data) != "0123456789abcdef" || !equalFields(response.trailers, h2Fields{{"x-sum", "1", false}}) {
			t.Fatalf("the client's response\nwanted:\n%q with x-sum\ngot:\n%q %v", "0123456789abcdef", response.data, response.trailers)
		}
	})

	t.Run("should send all of a request body past the threshold to an HTTP/1.1 origin, chunked, as it arrives", func(t *testing.T) {
		h1Origin := newScriptedTLSOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, "HTTP/1.1 204 No Content\r\n\r\n")
		})
		client := redirected(t, h1Origin.addr, 8)
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", "example.test", ":path", "/elsewhere")
		client.data(1, false, "0123456789")
		client.data(1, true, "abc")
		got := h1Origin.received(t)
		if !strings.Contains(got, "transfer-encoding: chunked\r\n") || h1Payload(t, got) != "0123456789abc" {
			t.Fatalf("the HTTP/1.1 origin's request\nwanted:\nchunked, the whole body\ngot:\n%q", got)
		}
		client.stream(t, 1)
	})

	t.Run("should carry a streamed request to an HTTP/1.1 origin a message at a time, before the client ends it", func(t *testing.T) {
		issuer, _ := newTestLeafIssuer(t, 24*time.Hour)
		config := issuer.config("127.0.0.1:443")
		config.NextProtos = []string{"http/1.1"}
		l, err := tls.Listen("tcp", "127.0.0.1:0", config)
		if err != nil {
			t.Fatalf("listening for the origin: %v", err)
		}
		t.Cleanup(func() { l.Close() })
		first := make(chan string, 1)
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
			framing, _ := h1.RequestFraming(head)
			body := h1.NewBodyReader(br, framing)
			var got []byte
			for len(got) < 10 {
				data, err := body.Peek()
				if err != nil {
					break
				}
				got = append(got, data...)
				body.Discard(len(data))
			}
			first <- string(got)
			for {
				data, err := body.Peek()
				if err != nil {
					break
				}
				body.Discard(len(data))
			}
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\nok")
		}()
		client := redirected(t, l.Addr().String(), DefaultBodyThreshold)
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", "example.test", ":path", "/elsewhere", "content-type", "application/grpc")
		client.data(1, false, grpcEnvelope(0, "hello"))
		select {
		case got := <-first:
			if got != grpcEnvelope(0, "hello") {
				t.Fatalf("the HTTP/1.1 origin's first message\nwanted:\n%q\ngot:\n%q", grpcEnvelope(0, "hello"), got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the HTTP/1.1 origin got no message before the client ended the request")
		}
		client.data(1, true, "")
		if response := client.stream(t, 1); string(response.data) != "ok" {
			t.Fatalf("the client's response\nwanted:\n%q\ngot:\n%q", "ok", response.data)
		}
	})
}
