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

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// carryPair carries one request/response pair: it reads the request's body,
// runs the request step, the round trip and the response step, and writes the
// response. It reports whether the connection carries another request.
func (c *h1Conn) carryPair(head *h1.RequestHead) (keep bool) {
	c.streams = nil
	framing, refused := h1.RequestFraming(head)
	threshold := c.engine.proxy.bodyThreshold()
	if refused == nil && expectsContinue(head, framing) && !declaresOver(framing, threshold) {
		// Marasi answers 100 itself, so the chains see the whole request
		// (docs/adr/2026-11-17-marasi-answers-100-continue-itself.md).
		// A body declared over the threshold goes as it arrives, so
		// the origin answers Expect.
		io.WriteString(c.bw, "HTTP/1.1 100 Continue\r\n\r\n")
		c.bw.Flush()
	}
	var body *h1.Body
	var passing *h1Passing
	var bodyErr error
	if refused == nil && c.streamedRequest(head, framing) {
		// The request chain sees the head with an empty body; its
		// stream messages follow once it goes upstream.
		body = &h1.Body{Framing: framing}
	} else {
		body, passing, bodyErr = readWireBody(c.br, framing, &refused, threshold)
	}
	if refused != nil {
		defer c.drain()
	}
	req, err := head.Request()
	if err != nil {
		c.refuse(http.StatusBadRequest, err)
		return false
	}
	req = c.prepare(req)
	switch {
	case refused != nil:
	case bodyErr != nil:
		req.Body = cutShortBody(body.Payload(), bodyErr)
	case passing != nil:
		req.Body = newPassedBody(body.Payload(), threshold)
	case framing.Kind != h1.NoBody:
		req.Body = io.NopCloser(bytes.NewReader(body.Payload()))
	}
	// The rest of a passing body is read only as it goes to the origin.
	// An origin that answered before it ended and keeps its connection
	// reads the rest, so it goes on before the next request; otherwise a
	// connection whose request body was not all read carries no other.
	defer func() {
		if passing != nil && passing.sent != nil && keep {
			<-passing.sent
			keep = passing.sentAll
		}
		if c.lingering != nil {
			passing.linger(c.lingering)
			c.lingering = nil
		}
		if passing.unfinished() {
			keep = false
		}
	}()
	if err := c.engine.request(req, c, &wireRequest{head: head, body: body, refused: refused, passing: passing}); err != nil || c.hijacked {
		return false
	}
	pair := requestPair(req)
	if pair == nil {
		return false
	}
	// A streamed pair goes on once both its directions ended
	// (h1engine_streamed.go).
	defer func() { keep = c.endStreams(pair, keep) }()

	res, wire := c.roundTrip(pair)
	if wire == nil && res.Close {
		// Marasi's own answer ends the connection, so the origin
		// connection, which a handshake may have dialled before any
		// request, is closed before it: the origin sees its end no
		// later than the client.
		c.closeOrigin()
	}
	stream, _ := res.Body.(*originStream)
	// A streamed response's head runs the response chain with an empty
	// body; its stream messages follow.
	var streamed *streamedDirection
	if c.streams != nil && c.streams.response != nil && wire != nil {
		streamed = c.engine.proxy.streamed(pair).direction(domain.StreamDirectionResponse, c.streams.response.framing)
	}
	err = c.engine.response(res, wire)
	if stream != nil {
		// The upgrade handler has returned, having closed it, or the
		// 101 never reached it (a drop, a skip, a failed handshake, or
		// a stale project's 502, which is still written below).
		stream.Close()
	}
	if c.hijacked || (stream != nil && res.StatusCode == http.StatusSwitchingProtocols) {
		return false
	}
	if wire == nil && c.streams != nil && c.streams.cancelled.Load() {
		// Marasi closed both connections of a streamed pair it
		// cancelled: its answer is stored, not written.
		return false
	}
	if streamed != nil {
		return c.carryStreamedResponse(pair, res, wire, streamed, keepAlive(head.Proto, &head.Header))
	}
	if wire != nil && wire.passing != nil && (res != pair.Response || pair.FromMarasi() || err != nil) {
		// A passing response the client does not get: its rest is never
		// read, so its origin connection carries no other.
		wire.passing.rest.close()
		c.closeOrigin()
	}
	if err != nil && pair.ResponseBodyEnding == BodyCutShort {
		// No response modifier ran: what arrived goes on as it arrived,
		// less the hop-by-hop fields, and the connection ends as the
		// origin's did (ticket 19).
		head := wire.head.Clone()
		head.Header.RemoveConnectionOptions(false)
		head.WriteTo(c.bw)
		wire.body.WriteTo(c.bw)
		c.bw.Flush()
		return false
	}
	keep = keepAlive(head.Proto, &head.Header)
	if wire != nil && !pair.FromMarasi() {
		client, err := encodedForClient(pair, res)
		if err != nil {
			return c.failClientLeg(pair, err)
		}
		out, err := outboundResponse(wire, client)
		if err != nil {
			return c.failClientLeg(pair, err)
		}
		out.head.WriteTo(c.bw)
		c.bw.Write(out.body)
		if wire.passing != nil {
			// The rest goes as it arrives, then the origin connection
			// ends as a response read whole would have ended it.
			if c.bw.Flush() != nil || !wire.passing.forward(c.bw) {
				c.closeOrigin()
				return false
			}
			if !keepAlive(wire.head.Proto, &wire.head.Header) || wire.body.Framing.Kind == h1.UntilClose {
				c.endOriginAfterResponse(passing)
			}
		}
		keep = keep && c.origin != nil && wire.body.Framing.Kind != h1.UntilClose
	} else if pair.storedWire != nil && res == pair.Response {
		// A browser view: the stored response as stored.
		out, err := outboundResponse(pair.storedWire, res)
		if err != nil {
			return c.failClientLeg(pair, err)
		}
		out.head.WriteTo(c.bw)
		c.bw.Write(out.body)
	} else {
		if err := res.Write(c.bw); err != nil {
			return false
		}
		keep = keep && !res.Close
	}
	return c.bw.Flush() == nil && keep
}

// failClientLeg answers a pair whose response could not be made into bytes
// for the client, before any of it was written, with a 502 proxy error
// response naming the cause, and logs the cause. The connection ends.
func (c *h1Conn) failClientLeg(pair *Pair, err error) bool {
	cause := fmt.Errorf("writing the response for the client: %w", err)
	proxy := c.engine.proxy
	proxy.respondWithProxyError(pair, http.StatusBadGateway, cause)
	proxy.logProxyError(pair, cause)
	pair.Response.Write(c.bw)
	c.bw.Flush()
	return false
}

// roundTrip gets the pair's response: the one a request modifier gave
// Respond, the empty 200 of a dropped request, or the origin's. A request the
// origin could not be asked is answered with a 502 proxy error response. The
// wire form is nil for any response but the origin's.
func (c *h1Conn) roundTrip(pair *Pair) (*http.Response, *wireResponse) {
	switch {
	case pair.control == controlRespond:
		return pair.Response, nil
	case pair.requestWire != nil && pair.requestWire.refused != nil:
		// Without the base pipeline nothing answered the refusal yet.
		c.engine.proxy.respondWithProxyError(pair, http.StatusBadRequest, pair.requestWire.refused)
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
	cancelled := c.streams != nil && c.streams.cancelled.Load()
	if cancelled {
		// Marasi cancelled the streamed pair while it waited for the
		// response, and logged why: the proxy error response is
		// stored with how each direction ended, and never written.
		err = errStreamedPairCancelled
	}
	c.engine.proxy.roundTripFailed(pair.Request, err)
	c.engine.proxy.respondWithProxyError(pair, http.StatusBadGateway, err)
	if !cancelled {
		c.engine.proxy.logProxyError(pair, err)
	}
	return pair.Response, nil
}

// errStreamedPairCancelled is the cause of the proxy error response stored
// for a streamed pair Marasi cancelled before its response came, closing
// both its connections: a message over the limit, say.
var errStreamedPairCancelled = errors.New("the streamed pair was cancelled before its response")

// errRouteNotCarried is the cause of the proxy error response to a request
// the engine cannot carry: a scheme other than http or https.
var errRouteNotCarried = errors.New("the h1 engine carries only HTTP and HTTPS")

// sendUpstream writes the pair's request to the connection's origin
// connection and reads the origin's response. The route is resolved for
// every request, and a request never goes out on a connection of another
// route.
func (c *h1Conn) sendUpstream(pair *Pair) (*http.Response, *wireResponse, error) {
	req := pair.Request
	if pair.RequestBodyEnding == BodyCutShort {
		_, err := io.ReadAll(req.Body)
		return nil, nil, fmt.Errorf("reading the request body: %w", err)
	}
	route := c.engine.proxy.resolveRoute()
	port, ok := carried(req.URL.Scheme)
	if !ok {
		return nil, nil, errRouteNotCarried
	}
	if c.streams != nil && c.streams.request != nil {
		// The stream messages follow the head: a body a modifier gave
		// the view is not sent.
		req.Body = http.NoBody
	}
	out, err := outboundRequest(pair.requestWire, req)
	if err != nil {
		return nil, nil, err
	}
	addr := hostPort(req.URL.Host, port)
	if req.Method == http.MethodConnect && c.tunnel != "" {
		// A CONNECT inside a tunnel names another server, but it is the
		// tunnel's origin that answers it, as without Marasi.
		addr = c.tunnel
	}
	origin, err := c.originFor(pair, route, addr)
	if err != nil {
		return nil, nil, err
	}
	// A client that leaves while its pair is in flight closes the origin connection, as a
	// direct client leaving would (docs/adr/2026-11-17-one-origin-connection-per-client-connection.md).
	stop := context.AfterFunc(pair.Context(), func() { origin.conn.Close() })
	streaming := false
	defer func() {
		if !streaming {
			stop()
		}
	}()
	// stream keeps the watch until the streamed pair ends, and makes the
	// client's leaving a cancel of the pair.
	stream := func() {
		if streaming {
			return
		}
		streaming = true
		streams := c.streams
		if !stop() {
			streams.cancel()
		}
		stop = context.AfterFunc(pair.Context(), func() {
			streams.cancel()
			origin.conn.Close()
		})
		streams.release = append(streams.release, func() { stop() })
	}
	out.head.WriteTo(origin.bw)
	origin.bw.Write(out.body)
	if err := origin.bw.Flush(); err != nil {
		return nil, nil, fmt.Errorf("writing the request to the origin: %w", err)
	}
	if c.streams != nil && c.streams.request != nil {
		stream()
		c.carryStreamedRequest(pair, origin, out.head)
	} else if passing := pair.requestWire.passing; passing != nil {
		// The rest of the body goes as it arrives, while the origin's
		// answer, which may come first, is read.
		passing.sendRest(origin)
	}

	head, err := h1.ReadResponseHead(origin.br)
	for err == nil && head.Interim() {
		c.relayInterim(req, head, pair.requestWire.passing)
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
	if c.streamedResponse(origin, head, framing, res) {
		// The body is carried after the response chain, and the
		// client's leaving closes the origin connection until then.
		stream()
		return res, wire, nil
	}
	body, passing, bodyErr := readBodyOrPass(origin.br, framing, c.engine.proxy.bodyThreshold())
	wire.body = body
	if passing != nil {
		// The rest goes to the client as it arrives (carryPair).
		wire.passing = passing
		res.Body = newPassedBody(body.Payload(), c.engine.proxy.bodyThreshold())
		return res, wire, nil
	}
	switch {
	case bodyErr != nil:
		res.Body = cutShortBody(body.Payload(), bodyErr)
		c.closeOrigin()
	case framing.Kind != h1.NoBody:
		res.Body = io.NopCloser(bytes.NewReader(body.Payload()))
	}
	if framing.Kind == h1.UntilClose || !keepAlive(head.Proto, &head.Header) {
		c.endOriginAfterResponse(pair.requestWire.passing)
	}
	return res, wire, nil
}

// expectsContinue reports whether the client waits for a 100 before it sends
// the request's body (RFC 9110 §10.1.1). An HTTP/1.0 request's expectation is
// ignored, and a request without a body needs no 100.
func expectsContinue(head *h1.RequestHead, framing h1.Framing) bool {
	if head.Proto == "HTTP/1.0" || framing.Kind == h1.NoBody ||
		(framing.Kind == h1.LengthFramed && framing.Length == 0) {
		return false
	}
	for _, value := range head.Header.Values("Expect") {
		if strings.EqualFold(strings.TrimSpace(value), "100-continue") {
			return true
		}
	}
	return false
}

// relayInterim writes an origin's interim response to the client as read,
// less its hop-by-hop fields, as for every message on each leg. A 100 is not
// relayed: Marasi answers Expect itself, except for a request that declared
// a length over the body threshold. Nor is any 1xx sent to an HTTP/1.0
// client, which did not ask for one (RFC 9110 §15.2).
func (c *h1Conn) relayInterim(req *http.Request, head *h1.ResponseHead, passing *h1Passing) {
	answered := passing == nil || !passing.declared
	if (head.StatusCode == http.StatusContinue && answered) || !req.ProtoAtLeast(1, 1) {
		return
	}
	head = head.Clone()
	head.Header.RemoveConnectionOptions(false)
	head.WriteTo(c.bw)
	c.bw.Flush()
}

// originConn is the origin connection of one client connection.
type originConn struct {
	addr string
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer

	// route is the route that opened conn.
	route *Route
	// scheme is http, or https when conn runs TLS with the origin.
	scheme string

	// While the client connection is idle (h1engine_endings.go): watch is
	// closed when the watch's read returns, stopping is set while the
	// client connection ends it, ended once the origin closed or sent
	// unasked bytes, and retired once a route swap closed conn. All but
	// ended are guarded by engine.mu.
	watch    chan struct{}
	stopping bool
	ended    bool
	retired  bool
}

// originFor returns the client connection's origin connection to addr on
// route, over TLS for an https request, and records its route on the pair.
// The first request dials it; a request for another origin, scheme or route
// closes the old one and dials anew through the route (h1engine_route.go).
func (c *h1Conn) originFor(pair *Pair, route *Route, addr string) (*originConn, error) {
	scheme := pair.Request.URL.Scheme
	if c.origin == nil || c.origin.addr != addr || c.origin.route != route || c.origin.scheme != scheme {
		c.closeOrigin()
		if h := c.handshake; h != nil && h.err != nil && h.addr == addr && scheme == "https" {
			// The origin the handshake could not reach: each request
			// is answered with the cause (h1engine_mitm.go).
			recordTried(pair.Request.Context(), h.route)
			return nil, h.err
		}
		conn, err := c.engine.proxy.dialOrigin(pair.Request.Context(), scheme, addr)
		if err != nil {
			return nil, err
		}
		c.origin = &originConn{addr: addr, scheme: scheme, route: conn.route, conn: conn, br: bufio.NewReader(conn), bw: bufio.NewWriter(conn)}
	}
	recordRoute(pair, c.origin)
	pair.originProtocol = domain.ProtocolHTTP1
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
