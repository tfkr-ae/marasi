package marasi

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// h1Engine is Marasi's own HTTP/1.1 engine (docs/adr/2026-11-17-marasi-owns-its-http1-wire-format.md).
// It reads each client connection with the codec in internal/h1, keeps every
// message's raw lines, and hands the base pipeline views of them. Each client
// connection gets its own origin connection
// (docs/adr/2026-11-17-one-origin-connection-per-client-connection.md).
//
// It is the only engine; docs/agents/maps/request-path.md is its map.
//
// Files:
//   - h1engine.go: the engine, the accept loop, shutdown, and one client
//     connection's loop, CONNECT and hijack (h1Conn);
//   - h1engine_pair.go: one request/response pair through the base
//     pipeline, and the round trip over the connection's origin connection;
//   - h1engine_wire.go: views back to wire bytes, for the engine and capture;
//   - h1engine_mitm.go: TLS inside a CONNECT, with leaf_issuer.go;
//   - h1engine_route.go: the origin connection's dial and route swaps;
//   - h1engine_endings.go: timeouts and how connections end;
//   - h1engine_refuse.go: requests refused for ambiguous framing;
//   - h1engine_encoding.go: compressed responses sent as the origin coded them;
//   - h1engine_checkpoint.go: Checkpoint's raw text and edits.
type h1Engine struct {
	proxy    *Proxy
	request  requestStep
	response responseStep

	closing atomic.Bool
	// handlers counts the client connections being served.
	handlers sync.WaitGroup

	// headerTimeout bounds reading a request's head, and idleTimeout the wait
	// for the next request on a kept-alive connection (h1engine_endings.go).
	headerTimeout, idleTimeout time.Duration

	// leaves issues the leaf certificates of intercepted TLS, nil until
	// setMITM, which runs before serving (h1engine_mitm.go).
	leaves *leafIssuer

	mu sync.Mutex
	// conns are the client connections being served, true while one waits
	// for its next request. h2conns are those that speak HTTP/2
	// (h2engine.go).
	conns   map[*h1Conn]bool
	h2conns map[*h2Conn]bool
}

func newH1Engine(proxy *Proxy) *h1Engine {
	engine := &h1Engine{
		proxy: proxy, conns: make(map[*h1Conn]bool),
		headerTimeout: headerTimeout, idleTimeout: idleTimeout,
	}
	engine.setPipeline(nil, nil)
	return engine
}

// serve accepts client connections from l and serves each on its own
// goroutine, until l closes. It returns nil once the engine is closing.
func (engine *h1Engine) serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if engine.closing.Load() {
				return nil
			}
			return err
		}
		engine.handlers.Add(1)
		go func() {
			defer engine.handlers.Done()
			newH1Conn(engine, conn).serve()
		}()
	}
}

// startClose stops the engine taking new requests. A connection waiting for
// its next request is closed now, and a busy one after its request/response pair.
func (engine *h1Engine) startClose() {
	engine.closing.Store(true)
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.h2Close()
	for conn, idle := range engine.conns {
		if idle {
			conn.conn.SetReadDeadline(time.Now())
		}
	}
}

// waitClosed waits for every client connection being served to finish.
func (engine *h1Engine) waitClosed() {
	engine.handlers.Wait()
}

// setPipeline sets the steps run for each request and response. Without a
// pipeline every request goes to the origin untouched: the request gets a
// bare pair, and the response is written as read.
func (engine *h1Engine) setPipeline(request requestStep, response responseStep) {
	if request == nil {
		request = func(req *http.Request, client clientHijacker, wire *wireRequest) error {
			if req.Method != http.MethodConnect {
				pair := &Pair{Request: req, ctx: req.Context(), requestWire: wire, RequestBodyEnding: readRequestBody(req)}
				*req = *req.WithContext(context.WithValue(req.Context(), pairKey{}, pair))
			}
			return nil
		}
	}
	if response == nil {
		response = func(*http.Response, *wireResponse) error { return nil }
	}
	engine.request, engine.response = request, response
}

// idle marks conn as waiting for its next request, for up to wait, and
// watches its origin connection meanwhile (h1engine_endings.go). It reports
// false when the engine is closing, and the connection should end.
func (engine *h1Engine) idle(conn *h1Conn, wait time.Duration) bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closing.Load() {
		return false
	}
	engine.conns[conn] = true
	conn.retireOrigin()
	conn.conn.SetReadDeadline(time.Now().Add(wait))
	conn.watchOrigin()
	return true
}

// busy marks conn as carrying a request.
func (engine *h1Engine) busy(conn *h1Conn) {
	engine.mu.Lock()
	engine.conns[conn] = false
	engine.mu.Unlock()
}

// forget drops conn once it is no longer served.
func (engine *h1Engine) forget(conn *h1Conn) {
	engine.mu.Lock()
	delete(engine.conns, conn)
	engine.mu.Unlock()
}

// h1Conn is one client connection and the state that lasts as long as it
// does. It is served by one goroutine, which runs the pipeline steps itself,
// so nothing in it is shared.
type h1Conn struct {
	engine *h1Engine
	conn   net.Conn
	br     *bufio.Reader
	bw     *bufio.Writer
	// ctx is done when the connection ends.
	ctx    context.Context
	cancel context.CancelFunc

	// tunnel is the authority of the CONNECT this connection is inside,
	// "" before one.
	tunnel string
	// kept is set once the connection carried a request and stays open for
	// another; the wait for that one is the idle timeout.
	kept bool
	// hijacked is set once the base pipeline took the connection over.
	hijacked bool
	// origin is this client connection's origin connection, nil until its
	// first request or after the origin closed it.
	origin *originConn
	// protocol is what the client's TLS handshake negotiated by ALPN, and
	// handshake the origin dial it made (h1engine_mitm.go); "" and nil
	// before intercepted TLS.
	protocol  string
	handshake *handshakeDial
}

func newH1Conn(engine *h1Engine, conn net.Conn) *h1Conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &h1Conn{
		engine: engine,
		conn:   conn,
		br:     bufio.NewReader(conn),
		bw:     bufio.NewWriter(conn),
		ctx:    ctx,
		cancel: cancel,
	}
}

// serve reads requests until the connection ends, one request/response pair at a time, so
// pipelined requests are answered in order.
func (c *h1Conn) serve() {
	defer c.end()
	// TLS sent straight to the listener is intercepted here, as TLS inside
	// a CONNECT is, so its handshake is origin first too. The HTTP/2
	// preface is h2c by prior knowledge (h2engine_h2c.go).
	switch c.opening() {
	case openingTLS:
		if !c.intercept("") {
			return
		}
	case openingH2C:
		c.protocol = protocolH2C
	}
	for {
		if c.protocol == domain.ProtocolHTTP2 || c.protocol == protocolH2C {
			c.serveH2()
			return
		}
		head, ok := c.readHead()
		if !ok {
			return
		}
		var keep bool
		if head.Method == http.MethodConnect && c.tunnel == "" {
			keep = c.connect(head)
			c.kept = false
		} else {
			keep = c.carryPair(head)
			c.kept = true
		}
		if !keep || c.hijacked {
			return
		}
	}
}

// How a client opens its connection to the listener.
type opening int

const (
	openingHTTP1 opening = iota
	openingTLS
	openingH2C
)

// opening reports whether the client opens its connection with a TLS
// record, the HTTP/2 preface or anything else, within the time a request
// head has. Meanwhile the connection is idle, so shutdown does not wait on
// a client that sends nothing.
func (c *h1Conn) opening() opening {
	if _, ok := c.conn.(*tls.Conn); ok || !c.engine.idle(c, c.engine.headerTimeout) {
		return openingHTTP1
	}
	defer c.engine.busy(c)
	first, err := c.br.Peek(1)
	switch {
	case err != nil:
		return openingHTTP1
	case first[0] == 0x16:
		return openingTLS
	case c.startsPriorKnowledge():
		return openingH2C
	}
	return openingHTTP1
}

// end closes the client connection, unless the pipeline took it over, and
// the origin connection the engine still owns.
func (c *h1Conn) end() {
	c.engine.forget(c)
	c.cancel()
	c.closeOrigin()
	if !c.hijacked {
		c.conn.Close()
	}
}

// Hijack hands the client connection to the base pipeline, which closes it or
// gives it to the upgrade handler. The engine no longer reads or writes it.
func (c *h1Conn) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if c.hijacked {
		return nil, nil, errors.New("the client connection was already taken over")
	}
	c.hijacked = true
	return c.conn, bufio.NewReadWriter(c.br, c.bw), nil
}

// connect opens a tunnel for a CONNECT the admission hook lets through. A
// refused tunnel closes the connection. What the tunnel carries is read as
// more requests on this connection, after intercepting TLS.
func (c *h1Conn) connect(head *h1.RequestHead) bool {
	req, err := head.Request()
	if err != nil {
		return false
	}
	req = c.prepare(req)
	if err := c.engine.request(req, c, nil); err != nil || c.hijacked {
		return false
	}
	// The same 200 Martian sent, so the tunnel's opening does not differ.
	res := &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      req.Proto,
		ProtoMajor: req.ProtoMajor,
		ProtoMinor: req.ProtoMinor,
		Header:     http.Header{},
		Body:       http.NoBody,
		Request:    req,
	}
	if err := res.Write(c.bw); err != nil || c.bw.Flush() != nil {
		return false
	}
	c.engine.setReadDeadline(c, c.engine.headerTimeout)
	first, err := c.br.Peek(1)
	if err != nil {
		return false
	}
	switch {
	case first[0] == 0x16:
		if !c.intercept(req.URL.Host) {
			return false
		}
	case c.startsPriorKnowledge():
		// h2c by prior knowledge inside a plain tunnel: the origin is
		// dialled now, as a TLS handshake inside a tunnel dials it.
		c.protocol = protocolH2C
		c.handshake = c.dialPriorKnowledgeOrigin(c.ctx, req.URL.Host)
	}
	c.tunnel = req.URL.Host
	return true
}

// prepare gives a request view what Martian gave every request: the client's
// address, a scheme, the URL's host and the connection's context.
func (c *h1Conn) prepare(req *http.Request) *http.Request {
	req.RemoteAddr = c.conn.RemoteAddr().String()
	// A CONNECT outside a tunnel opens one and is not sent on; inside a
	// tunnel it is a request for the tunnel's origin like any other.
	if req.Method != http.MethodConnect || c.tunnel != "" {
		req.URL.Scheme = "http"
		if tlsConn, ok := c.conn.(*tls.Conn); ok {
			state := tlsConn.ConnectionState()
			req.TLS = &state
			req.URL.Scheme = "https"
		}
		// Inside a tunnel the request goes to the tunnel's origin, as it
		// would without Marasi, so its Host is only a field a tester can
		// change (docs/adr/2026-11-19-requests-inside-a-tunnel-go-to-the-tunnels-origin.md).
		// A modifier that changes the URL's host still sends it elsewhere.
		if req.URL.Host == "" {
			req.URL.Host = cmp.Or(withoutDefaultPort(c.tunnel, req.URL.Scheme), req.Host)
		}
	}
	return req.WithContext(c.ctx)
}

// withoutDefaultPort drops authority's port when it is the default for
// scheme, so a URL built from a CONNECT authority (always host:port) reads as
// one built from the Host a browser sends (host alone), and scope rules and
// extensions see the same URL either way. An empty authority stays empty.
func withoutDefaultPort(authority, scheme string) string {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return authority
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return authority
}
