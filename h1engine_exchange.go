package marasi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// exchange carries one request and its response: it reads the body, runs
// the request step, the round trip and the response step, and writes the
// response. It reports whether the connection carries another request.
func (c *h1Conn) exchange(head *h1.RequestHead) bool {
	framing, err := h1.RequestFraming(head)
	if err != nil {
		// Ticket 20 answers this with a stored 400 proxy error response.
		c.refuse(http.StatusBadRequest, err)
		return false
	}
	body, bodyErr := h1.ReadBody(c.br, framing)
	req, err := head.Request()
	if err != nil {
		c.refuse(http.StatusBadRequest, err)
		return false
	}
	req = c.prepare(req)
	switch {
	case bodyErr != nil:
		req.Body = cutShortBody(body.Payload(), bodyErr)
	case framing.Kind != h1.NoBody:
		req.Body = io.NopCloser(bytes.NewReader(body.Payload()))
	}
	if err := c.engine.request(req, c, &wireRequest{head: head, body: body}); err != nil || c.hijacked {
		return false
	}
	pair := requestPair(req)
	if pair == nil {
		return false
	}

	res, wire := c.roundTrip(pair)
	err = c.engine.response(res, wire)
	if c.hijacked {
		return false
	}
	if wire != nil && res.StatusCode == http.StatusSwitchingProtocols {
		// No upgrade handler took the connection: ticket 16 relays it.
		if stream, ok := res.Body.(*originStream); ok {
			stream.Close()
		}
		return false
	}
	if err != nil && pair.ResponseBodyEnding == BodyCutShort {
		// No response modifier ran: what arrived goes on as it arrived,
		// and the connection ends as the origin's did (ticket 19).
		wire.head.WriteTo(c.bw)
		wire.body.WriteTo(c.bw)
		c.bw.Flush()
		return false
	}
	keep := keepAlive(head.Proto, &head.Header)
	if wire != nil && !pair.FromMarasi() {
		out, err := outboundResponse(wire, res)
		if err != nil {
			return false
		}
		out.head.WriteTo(c.bw)
		c.bw.Write(out.body)
		keep = keep && c.origin != nil && wire.body.Framing.Kind != h1.UntilClose
	} else {
		if err := res.Write(c.bw); err != nil {
			return false
		}
		keep = keep && !res.Close
	}
	return c.bw.Flush() == nil && keep
}

// roundTrip gets the pair's response: the one a request modifier gave
// Respond, the empty 200 of a dropped request, or the origin's. A request the
// origin could not be asked is answered with a 502 proxy error response. The
// wire form is nil for any response but the origin's.
func (c *h1Conn) roundTrip(pair *Pair) (*http.Response, *wireResponse) {
	switch {
	case pair.control == controlRespond:
		return pair.Response, nil
	case pair.Dropped():
		// The same empty 200 Martian answered a dropped request with.
		req := pair.Request
		return &http.Response{
			Status: "200 OK", StatusCode: http.StatusOK,
			Proto: req.Proto, ProtoMajor: req.ProtoMajor, ProtoMinor: req.ProtoMinor,
			Header: http.Header{}, Body: http.NoBody, Request: req, Close: req.Close,
		}, nil
	}
	res, wire, err := c.sendUpstream(pair)
	if err == nil {
		return res, wire
	}
	c.closeOrigin()
	c.engine.proxy.roundTripFailed(pair.Request, err)
	c.engine.proxy.respondWithProxyError(pair, http.StatusBadGateway, err)
	c.engine.proxy.logProxyError(pair, err)
	return pair.Response, nil
}

// errRouteNotCarried is the cause of the proxy error response to a request
// whose route the skeleton cannot carry yet: ticket 15 carries upstream
// proxies, ticket 14 TLS to the origin.
var errRouteNotCarried = errors.New("the h1 engine carries only plain HTTP over a direct route")

// sendUpstream writes the pair's request to the connection's origin
// connection and reads the origin's response. The route is resolved for
// every request; anything but a direct route fails closed.
func (c *h1Conn) sendUpstream(pair *Pair) (*http.Response, *wireResponse, error) {
	req := pair.Request
	if pair.RequestBodyEnding == BodyCutShort {
		_, err := io.ReadAll(req.Body)
		return nil, nil, fmt.Errorf("reading the request body: %w", err)
	}
	if !c.engine.proxy.resolveRoute().Direct() || req.URL.Scheme != "http" {
		return nil, nil, errRouteNotCarried
	}
	out, err := outboundRequest(pair.requestWire, req)
	if err != nil {
		return nil, nil, err
	}
	origin, err := c.originFor(pair, hostPort(req.URL.Host, "80"))
	if err != nil {
		return nil, nil, err
	}
	// A client that leaves mid-exchange closes the origin connection, as a
	// direct client leaving would (docs/adr/2026-11-17-one-origin-connection-per-client-connection.md).
	stop := context.AfterFunc(pair.Context(), func() { origin.conn.Close() })
	defer stop()
	out.head.WriteTo(origin.bw)
	origin.bw.Write(out.body)
	if err := origin.bw.Flush(); err != nil {
		return nil, nil, fmt.Errorf("writing the request to the origin: %w", err)
	}

	head, err := h1.ReadResponseHead(origin.br)
	for err == nil && head.Interim() {
		// Ticket 18 relays interim responses; until then they are
		// read and dropped, as Go's transport did.
		head, err = h1.ReadResponseHead(origin.br)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the origin's response: %w", err)
	}
	framing, err := h1.ResponseFraming(head, req.Method)
	var framingErr *h1.FramingError
	if err != nil && !(errors.As(err, &framingErr) && framingErr.Cause == h1.CauseLengthAndTransferEncoding) {
		return nil, nil, err
	}
	res := head.Response()
	res.Request = req
	wire := &wireResponse{head: head, body: &h1.Body{Framing: framing}}
	if head.StatusCode == http.StatusSwitchingProtocols {
		// The connection is the upgrade's body now. The upgrade handler
		// owns it once WebSocketPrepareModifier detaches it (ticket 16).
		res.Body = &originStream{Reader: origin.br, Conn: origin.conn}
		c.origin = nil
		return res, wire, nil
	}
	body, bodyErr := h1.ReadBody(origin.br, framing)
	wire.body = body
	switch {
	case bodyErr != nil:
		res.Body = cutShortBody(body.Payload(), bodyErr)
		c.closeOrigin()
	case framing.Kind != h1.NoBody:
		res.Body = io.NopCloser(bytes.NewReader(body.Payload()))
	}
	if framing.Kind == h1.UntilClose || !keepAlive(head.Proto, &head.Header) {
		c.closeOrigin()
	}
	return res, wire, nil
}

// originConn is the origin connection of one client connection.
type originConn struct {
	addr string
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
}

// originFor returns the client connection's origin connection to addr. The
// first request dials it; a request for another origin closes the old one
// and dials anew.
func (c *h1Conn) originFor(pair *Pair, addr string) (*originConn, error) {
	if c.origin != nil && c.origin.addr == addr {
		return c.origin, nil
	}
	c.closeOrigin()
	// Ticket 15 dials through the route here.
	conn, err := c.engine.proxy.dialTransport(pair.Context(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	c.origin = &originConn{addr: addr, conn: conn, br: bufio.NewReader(conn), bw: bufio.NewWriter(conn)}
	return c.origin, nil
}

// closeOrigin closes the origin connection the engine owns, if any.
func (c *h1Conn) closeOrigin() {
	if c.origin != nil {
		c.origin.conn.Close()
		c.origin = nil
	}
}

// refuse answers a request the engine cannot read with status and the cause,
// and the connection ends.
func (c *h1Conn) refuse(status int, cause error) {
	res := plainTextResponse(nil, status, cause.Error())
	res.Close = true
	res.Write(c.bw)
	c.bw.Flush()
}

// originStream is an upgraded origin connection, read through the buffer the
// engine read the 101 with.
type originStream struct {
	io.Reader
	net.Conn
}

func (s *originStream) Read(p []byte) (int, error) { return s.Reader.Read(p) }

// keepAlive reports whether a message's sender keeps the connection open
// after it (RFC 9112 §9.3).
func keepAlive(proto string, header *h1.Header) bool {
	var closeToken, keepToken bool
	for _, value := range header.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			switch strings.ToLower(strings.TrimSpace(token)) {
			case "close":
				closeToken = true
			case "keep-alive":
				keepToken = true
			}
		}
	}
	if closeToken {
		return false
	}
	return proto != "HTTP/1.0" || keepToken
}

// hostPort adds port to host when it has none.
func hostPort(host, port string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), port)
}
