package marasi

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// h1Engine is Marasi's own HTTP/1.1 engine (docs/adr/2026-11-17-marasi-owns-its-http1-wire-format.md).
// It reads each client connection with the codec in internal/h1, keeps every
// message's raw lines, and hands the base pipeline views of them. Each client
// connection gets its own origin connection
// (docs/adr/2026-11-17-one-origin-connection-per-client-connection.md).
//
// It is chosen by the h1engine build tag or the withH1Engine option until the
// switch. See .scratch/proxy-library/notes/engine-api.md for where each later
// piece of work plugs in.
//
// Files:
//   - h1engine.go: the engine, the accept loop, shutdown, and one client
//     connection's loop, CONNECT and hijack (h1Conn);
//   - h1engine_exchange.go: one request/response exchange through the base
//     pipeline, and the round trip over the connection's origin connection;
//   - h1engine_wire.go: views back to wire bytes, for the engine and capture.
type h1Engine struct {
	proxy    *Proxy
	request  requestStep
	response responseStep

	closing atomic.Bool
	// handlers counts the client connections being served.
	handlers sync.WaitGroup

	mu sync.Mutex
	// conns are the client connections being served, true while one waits
	// for its next request.
	conns map[*h1Conn]bool
}

func newH1Engine(proxy *Proxy) *h1Engine {
	engine := &h1Engine{proxy: proxy, conns: make(map[*h1Conn]bool)}
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
// its next request is closed now, and a busy one after its exchange.
func (engine *h1Engine) startClose() {
	engine.closing.Store(true)
	engine.mu.Lock()
	defer engine.mu.Unlock()
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

// setMITM records the CA. Ticket 14 gives the engine its own leaf issuer;
// until then it intercepts nothing, and a TLS handshake to the listener or
// inside a tunnel fails.
func (engine *h1Engine) setMITM(*x509.Certificate, any) (*tls.Config, error) {
	return &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, errNoLeafIssuer
	}}, nil
}

// errNoLeafIssuer is the TLS handshake failure until ticket 14.
var errNoLeafIssuer = errors.New("the h1 engine issues no leaf certificates yet")

// setPipeline sets the steps run for each request and response. Without a
// pipeline every request goes to the origin untouched, as under Martian: the
// request gets a bare pair, and the response is written as read.
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

// setRoundTripper does nothing: the engine carries requests itself.
func (engine *h1Engine) setRoundTripper(http.RoundTripper) {}

// idle marks conn as waiting for its next request. It reports false when the
// engine is closing, and the connection should end.
func (engine *h1Engine) idle(conn *h1Conn) bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closing.Load() {
		return false
	}
	engine.conns[conn] = true
	conn.retireOrigin()
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
	// hijacked is set once the base pipeline took the connection over.
	hijacked bool
	// origin is this client connection's origin connection, nil until its
	// first request or after the origin closed it.
	origin *originConn
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

// serve reads requests until the connection ends, one exchange at a time, so
// pipelined requests are answered in order.
func (c *h1Conn) serve() {
	defer c.end()
	for c.engine.idle(c) {
		// Ticket 19 adds the 60-second header and 5-minute idle
		// timeouts here.
		head, err := h1.ReadRequestHead(c.br)
		c.engine.busy(c)
		if err != nil {
			// A clean close, a timeout or a head the codec cannot
			// parse ends the connection, as under Martian.
			return
		}
		var keep bool
		if head.Method == http.MethodConnect && c.tunnel == "" {
			keep = c.connect(head)
		} else {
			keep = c.exchange(head)
		}
		if !keep || c.hijacked {
			return
		}
	}
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
// refused tunnel closes the connection. Plain HTTP inside the tunnel is read
// as more requests on this connection; TLS inside it is ticket 14's.
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
	first, err := c.br.Peek(1)
	if err != nil {
		return false
	}
	if first[0] == 0x16 {
		// A TLS handshake: ticket 14 intercepts it here.
		return false
	}
	c.tunnel = req.URL.Host
	return true
}

// prepare gives a request view what Martian gave every request: the client's
// address, a scheme, the URL's host and the connection's context.
func (c *h1Conn) prepare(req *http.Request) *http.Request {
	req.RemoteAddr = c.conn.RemoteAddr().String()
	if req.Method != http.MethodConnect {
		req.URL.Scheme = "http"
		if tlsConn, ok := c.conn.(*tls.Conn); ok {
			state := tlsConn.ConnectionState()
			req.TLS = &state
			req.URL.Scheme = "https"
		}
		if req.URL.Host == "" {
			req.URL.Host = req.Host
		}
	}
	return req.WithContext(c.ctx)
}
