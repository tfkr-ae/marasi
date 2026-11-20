package marasi

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// h2TestOrigin is an HTTP/2 origin over TLS whose behaviour each test
// scripts frame by frame, so a test sees exactly what the proxy sent it.
type h2TestOrigin struct {
	addr string
	// conns carries each accepted connection, once its preface and first
	// SETTINGS are read.
	conns chan *h2TestPeer
	// offered carries the ALPN list of each handshake.
	offered chan []string
}

// newH2TestOrigin serves TLS with ALPN alpn (h2 by default) and SETTINGS
// settings, and hands each connection to the test.
func newH2TestOrigin(t *testing.T, settings ...http2.Setting) *h2TestOrigin {
	t.Helper()
	issuer, _ := newTestLeafIssuer(t, 24*time.Hour)
	config := issuer.config("127.0.0.1:443")
	config.NextProtos = []string{"h2"}
	origin := &h2TestOrigin{conns: make(chan *h2TestPeer, 4), offered: make(chan []string, 4)}
	config.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		select {
		case origin.offered <- hello.SupportedProtos:
		default:
		}
		return nil, nil
	}
	l, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	origin.addr = l.Addr().String()
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				preface := make([]byte, len(http2.ClientPreface))
				if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != http2.ClientPreface {
					conn.Close()
					return
				}
				peer := newH2TestPeer(conn)
				peer.write(func(f *http2.Framer) error { return f.WriteSettings(settings...) })
				first, err := peer.framer.ReadFrame()
				if err != nil {
					conn.Close()
					return
				}
				first.(*http2.SettingsFrame).ForeachSetting(func(s http2.Setting) error {
					peer.settings = append(peer.settings, s)
					return nil
				})
				peer.write(func(f *http2.Framer) error { return f.WriteSettingsAck() })
				go peer.read()
				origin.conns <- peer
			}()
		}
	}()
	return origin
}

// accept waits for the proxy's connection to the origin.
func (origin *h2TestOrigin) accept(t *testing.T) *h2TestPeer {
	t.Helper()
	select {
	case peer := <-origin.conns:
		t.Cleanup(func() { peer.conn.Close() })
		return peer
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy opened no connection to the origin")
		return nil
	}
}

// h2TestPeer is one end of an HTTP/2 connection in a test, client or
// origin: it writes frames and records what arrives, stream by stream.
type h2TestPeer struct {
	conn     net.Conn
	framer   *http2.Framer
	wmu      sync.Mutex
	encoder  *hpack.Encoder
	block    bytes.Buffer
	settings []http2.Setting

	mu      sync.Mutex
	changed chan struct{}
	streams map[uint32]*h2TestStream
	order   []uint32
	goAway  *http2.GoAwayFrame
	err     error
}

// h2TestStream is what arrived on one stream.
type h2TestStream struct {
	headers  []h2Fields
	data     []byte
	trailers h2Fields
	ended    bool
	reset    string
	final    bool
}

func newH2TestPeer(conn net.Conn) *h2TestPeer {
	peer := &h2TestPeer{conn: conn, framer: http2.NewFramer(conn, conn), changed: make(chan struct{}), streams: make(map[uint32]*h2TestStream)}
	peer.framer.AllowIllegalWrites = true
	peer.encoder = hpack.NewEncoder(&peer.block)
	return peer
}

func (p *h2TestPeer) write(fn func(*http2.Framer) error) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return fn(p.framer)
}

// headers sends a header block of name, value pairs on stream id.
func (p *h2TestPeer) headers(id uint32, end bool, pairs ...string) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.block.Reset()
	for i := 0; i+1 < len(pairs); i += 2 {
		p.encoder.WriteField(hpack.HeaderField{Name: pairs[i], Value: pairs[i+1]})
	}
	p.framer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: p.block.Bytes(), EndStream: end, EndHeaders: true})
}

func (p *h2TestPeer) data(id uint32, end bool, data string) {
	p.write(func(f *http2.Framer) error { return f.WriteData(id, end, []byte(data)) })
}

// read records frames until the connection ends.
func (p *h2TestPeer) read() {
	decoder := hpack.NewDecoder(4096, nil)
	for {
		frame, err := p.framer.ReadFrame()
		p.mu.Lock()
		if err != nil {
			p.err = err
			close(p.changed)
			p.mu.Unlock()
			return
		}
		stream := func(id uint32) *h2TestStream {
			s, ok := p.streams[id]
			if !ok {
				s = &h2TestStream{}
				p.streams[id] = s
				p.order = append(p.order, id)
			}
			return s
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				go p.write(func(fr *http2.Framer) error { return fr.WriteSettingsAck() })
				if p.settings == nil {
					f.ForeachSetting(func(s http2.Setting) error { p.settings = append(p.settings, s); return nil })
				}
			}
		case *http2.PingFrame:
			if !f.IsAck() {
				go p.write(func(fr *http2.Framer) error { return fr.WritePing(true, f.Data) })
			}
		case *http2.HeadersFrame:
			decoded, _ := decoder.DecodeFull(f.HeaderBlockFragment())
			fields := make(h2Fields, len(decoded))
			for i, d := range decoded {
				fields[i] = h2Field{Name: d.Name, Value: d.Value}
			}
			s := stream(f.StreamID)
			status, _ := fields.get(":status")
			switch {
			case s.final:
				s.trailers = fields
			default:
				s.headers = append(s.headers, fields)
				s.final = !strings.HasPrefix(status, "1")
			}
			s.ended = s.ended || f.StreamEnded()
		case *http2.DataFrame:
			s := stream(f.StreamID)
			s.data = append(s.data, f.Data()...)
			s.ended = s.ended || f.StreamEnded()
			if n := f.Header().Length; n > 0 {
				go p.write(func(fr *http2.Framer) error {
					fr.WriteWindowUpdate(0, n)
					return fr.WriteWindowUpdate(f.StreamID, n)
				})
			}
		case *http2.RSTStreamFrame:
			stream(f.StreamID).reset = f.ErrCode.String()
		case *http2.GoAwayFrame:
			p.goAway = f
		}
		close(p.changed)
		p.changed = make(chan struct{})
		p.mu.Unlock()
	}
}

// await waits until done holds of what arrived.
func (p *h2TestPeer) await(t *testing.T, what string, done func(p *h2TestPeer) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		p.mu.Lock()
		ok, changed, err := done(p), p.changed, p.err
		p.mu.Unlock()
		if ok {
			return
		}
		if err != nil {
			t.Fatalf("waiting for %s, the connection ended: %v", what, err)
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("waiting for %s: timed out", what)
		}
	}
}

// stream waits for stream id to end, by END_STREAM or RST_STREAM, and
// returns what arrived on it.
func (p *h2TestPeer) stream(t *testing.T, id uint32) h2TestStream {
	t.Helper()
	p.await(t, "the stream to end", func(p *h2TestPeer) bool {
		s := p.streams[id]
		return s != nil && (s.ended || s.reset != "")
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return *p.streams[id]
}

// nthStream waits for the n-th stream the peer opened and returns its ID.
func (p *h2TestPeer) nthStream(t *testing.T, n int) uint32 {
	t.Helper()
	p.await(t, "a request", func(p *h2TestPeer) bool { return len(p.order) > n })
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.order[n]
}

// dialH2 opens a tunnel to authority through the proxy at addr and runs TLS
// in it offering alpn, trusting only ca. When the proxy chose h2 it sends
// the connection preface and SETTINGS and reads what comes back.
func dialH2(t *testing.T, addr, authority, serverName string, ca *x509.Certificate, alpn ...string) (*h2TestPeer, string) {
	t.Helper()
	conn, br := dialProxy(t, addr)
	openTunnel(t, conn, br, authority)
	tlsConn, _ := clientTLS(t, conn, br, ca, serverName, alpn...)
	return startH2Client(t, tlsConn), tlsConn.ConnectionState().NegotiatedProtocol
}

func startH2Client(t *testing.T, tlsConn *tls.Conn) *h2TestPeer {
	t.Helper()
	t.Cleanup(func() { tlsConn.Close() })
	if tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
		return nil
	}
	io.WriteString(tlsConn, http2.ClientPreface)
	client := newH2TestPeer(tlsConn)
	client.write(func(f *http2.Framer) error { return f.WriteSettings() })
	go client.read()
	return client
}

func TestH2Engine(t *testing.T) {
	t.Run("should give a client that offers h2 the origin's h2, carry its request on an origin stream and store the pair in the text form", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		_, port, _ := net.SplitHostPort(origin.addr)
		authority := net.JoinHostPort("localhost", port)
		client, protocol := dialH2(t, addr, authority, "localhost", ca, "h2", "http/1.1")
		if protocol != "h2" {
			t.Fatalf("ALPN\nwanted:\nh2\ngot:\n%q", protocol)
		}
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", authority, ":path", "/a?b=1", "x-test", "One", "accept", "*/*")
		id := upstream.nthStream(t, 0)
		request := upstream.stream(t, id)
		want := h2Fields{{":method", "GET", false}, {":scheme", "https", false}, {":authority", authority, false}, {":path", "/a?b=1", false}, {"x-test", "One", false}, {"accept", "*/*", false}}
		if len(request.headers) != 1 || !equalFields(request.headers[0], want) {
			t.Fatalf("the origin's request\nwanted:\n%v\ngot:\n%v", want, request.headers)
		}
		upstream.headers(id, false, ":status", "200", "content-type", "text/plain", "x-origin", "1")
		upstream.data(id, true, "hello")

		response := client.stream(t, 1)
		wantResponse := h2Fields{{":status", "200", false}, {"content-type", "text/plain", false}, {"x-origin", "1", false}}
		if len(response.headers) != 1 || !equalFields(response.headers[0], wantResponse) || string(response.data) != "hello" {
			t.Fatalf("the client's response\nwanted:\n%v %q\ngot:\n%v %q", wantResponse, "hello", response.headers, response.data)
		}
		expectCaptured(t, captured, "https",
			"GET /a?b=1 HTTP/2\r\n:scheme: https\r\n:authority: "+authority+"\r\nx-test: One\r\naccept: */*\r\n\r\n",
			"HTTP/2 200\r\ncontent-type: text/plain\r\nx-origin: 1\r\n\r\nhello")
	})

	t.Run("should complete the handshake when the origin cannot be reached, with http/1.1 when offered and h2 otherwise, and answer each request with a 502 naming the cause", func(t *testing.T) {
		closed, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		unreachable := closed.Addr().String()
		closed.Close()
		addr, _, ca := serveH1EngineMITMProxy(t, true)

		if _, protocol := dialH2(t, addr, unreachable, "127.0.0.1", ca, "h2", "http/1.1"); protocol != "http/1.1" {
			t.Fatalf("ALPN offering h2 and http/1.1\nwanted:\nhttp/1.1\ngot:\n%q", protocol)
		}
		client, protocol := dialH2(t, addr, unreachable, "127.0.0.1", ca, "h2")
		if protocol != "h2" {
			t.Fatalf("ALPN offering h2\nwanted:\nh2\ngot:\n%q", protocol)
		}
		for _, id := range []uint32{1, 3} {
			client.headers(id, true, ":method", "GET", ":scheme", "https", ":authority", unreachable, ":path", "/")
			response := client.stream(t, id)
			if status, _ := response.headers[0].get(":status"); status != "502" || !strings.Contains(string(response.data), "refused") {
				t.Fatalf("stream %d\nwanted:\na 502 naming the refused connection\ngot:\n%v %q", id, response.headers, response.data)
			}
		}
	})

	t.Run("should send the origin ENABLE_PUSH=0, tell the client the origin's MAX_CONCURRENT_STREAMS and no ENABLE_CONNECT_PROTOCOL, and carry streams side by side", func(t *testing.T) {
		origin := newH2TestOrigin(t, http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 7}, http2.Setting{ID: http2.SettingEnableConnectProtocol, Val: 1})
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		if !hasSetting(upstream.settings, http2.SettingEnablePush, 0) {
			t.Errorf("the origin's SETTINGS\nwanted:\nENABLE_PUSH=0\ngot:\n%v", upstream.settings)
		}
		client.await(t, "the proxy's SETTINGS", func(p *h2TestPeer) bool { return p.settings != nil })
		if !hasSetting(client.settings, http2.SettingMaxConcurrentStreams, 7) {
			t.Errorf("the client's SETTINGS\nwanted:\nMAX_CONCURRENT_STREAMS=7\ngot:\n%v", client.settings)
		}
		for _, s := range client.settings {
			if s.ID == http2.SettingEnableConnectProtocol {
				t.Errorf("the client's SETTINGS\nwanted:\nno ENABLE_CONNECT_PROTOCOL\ngot:\n%v", client.settings)
			}
		}

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/slow")
		slow := upstream.nthStream(t, 0)
		client.headers(3, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/fast")
		fast := upstream.nthStream(t, 1)
		upstream.headers(fast, true, ":status", "204")
		if status, _ := client.stream(t, 3).headers[0].get(":status"); status != "204" {
			t.Fatalf("the second stream, answered first\nwanted:\n204\ngot:\n%v", status)
		}
		upstream.headers(slow, true, ":status", "200")
		if status, _ := client.stream(t, 1).headers[0].get(":status"); status != "200" {
			t.Fatalf("the first stream\nwanted:\n200\ngot:\n%v", status)
		}
	})

	t.Run("should forward what arrived of a body the origin resets, reset the client's stream with the same code, and store the pair as cut short", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/reset")
		id := upstream.nthStream(t, 0)
		upstream.headers(id, false, ":status", "200", "content-type", "text/plain")
		upstream.data(id, false, "partial")
		upstream.write(func(f *http2.Framer) error { return f.WriteRSTStream(id, http2.ErrCodeInternal) })

		response := client.stream(t, 1)
		client.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[1].reset != "" })
		response = client.stream(t, 1)
		if string(response.data) != "partial" || response.reset != "INTERNAL_ERROR" || response.ended {
			t.Fatalf("the client's stream\nwanted:\n%q then RST_STREAM INTERNAL_ERROR\ngot:\n%q, reset %q, ended %v", "partial", response.data, response.reset, response.ended)
		}
		<-captured.request
		select {
		case res := <-captured.response:
			if string(res.Raw) != "HTTP/2 200\r\ncontent-type: text/plain\r\n\r\npartial" || res.Metadata["response_body_ending"] != "cut short" {
				t.Fatalf("the stored response\nwanted:\nwhat arrived, cut short\ngot:\n%q %v", res.Raw, res.Metadata)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
	})

	t.Run("should relay the origin's GOAWAY in the client's stream IDs and leave the streams it did not process unanswered", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/first")
		first := upstream.nthStream(t, 0)
		client.headers(3, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/second")
		upstream.nthStream(t, 1)
		upstream.headers(first, false, ":status", "200")
		upstream.data(first, true, "first")
		upstream.write(func(f *http2.Framer) error { return f.WriteGoAway(first, http2.ErrCodeNo, nil) })

		if response := client.stream(t, 1); string(response.data) != "first" {
			t.Fatalf("the first stream\nwanted:\n%q\ngot:\n%q", "first", response.data)
		}
		client.await(t, "GOAWAY", func(p *h2TestPeer) bool { return p.goAway != nil })
		client.mu.Lock()
		goAway, second := *client.goAway, client.streams[3]
		client.mu.Unlock()
		if goAway.LastStreamID != 1 || goAway.ErrCode != http2.ErrCodeNo {
			t.Fatalf("GOAWAY\nwanted:\nlast stream 1, NO_ERROR\ngot:\nlast stream %d, %s", goAway.LastStreamID, goAway.ErrCode)
		}
		if second != nil {
			t.Fatalf("the second stream\nwanted:\nnothing\ngot:\n%+v", *second)
		}
	})

	t.Run("should send a graceful GOAWAY on a route swap, finish the stream in flight, then close both legs", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		id := upstream.nthStream(t, 0)

		proxyOf(t, addr).engine.routeSwapped(&Route{})
		client.await(t, "GOAWAY", func(p *h2TestPeer) bool { return p.goAway != nil })
		if client.goAway.LastStreamID != 1 || client.goAway.ErrCode != http2.ErrCodeNo {
			t.Fatalf("GOAWAY\nwanted:\nlast stream 1, NO_ERROR\ngot:\nlast stream %d, %s", client.goAway.LastStreamID, client.goAway.ErrCode)
		}
		upstream.headers(id, true, ":status", "200")
		if status, _ := client.stream(t, 1).headers[0].get(":status"); status != "200" {
			t.Fatalf("the stream in flight\nwanted:\n200\ngot:\n%v", status)
		}
		client.conn.Close()
		upstream.await(t, "the origin connection to close", func(p *h2TestPeer) bool { return p.err != nil })
	})

	t.Run("should intercept h2 sent straight to the listener, dialling the SNI's origin through its waypoint", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		proxy := proxyOf(t, addr)
		proxy.waypointMu.Lock()
		proxy.Waypoints = map[string]string{"origin.example:443": origin.addr}
		proxy.waypointMu.Unlock()
		conn, br := dialProxy(t, addr)
		tlsConn, _ := clientTLS(t, conn, br, ca, "origin.example", "h2")
		client := startH2Client(t, tlsConn)
		if client == nil {
			t.Fatalf("ALPN\nwanted:\nh2\ngot:\n%q", tlsConn.ConnectionState().NegotiatedProtocol)
		}
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", "origin.example", ":path", "/")
		id := upstream.nthStream(t, 0)
		if authority, _ := upstream.stream(t, id).headers[0].get(":authority"); authority != "origin.example" {
			t.Errorf(":authority\nwanted:\norigin.example\ngot:\n%q", authority)
		}
		upstream.headers(id, true, ":status", "204")
		if status, _ := client.stream(t, 1).headers[0].get(":status"); status != "204" {
			t.Fatalf("wanted the origin's 204, got %v", status)
		}
	})
}

func equalFields(a, b h2Fields) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func hasSetting(settings []http2.Setting, id http2.SettingID, val uint32) bool {
	for _, s := range settings {
		if s.ID == id && s.Val == val {
			return true
		}
	}
	return false
}
