package marasi

import (
	"bytes"
	"cmp"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/domain"

	"golang.org/x/net/http2"
)

// One client stream's request/response pair through the base pipeline, as
// h1Conn.carryPair carries one on HTTP/1.1: the request step, the round trip
// on the stream's own origin stream, the response step, and the response
// written back on the stream.

// carry carries st's request, which is whole, and writes its response.
func (h *h2Conn) carry(st *h2Stream) {
	h.mu.Lock()
	request := st.request
	h.mu.Unlock()
	req := request.request()
	switch {
	case request.rest != nil:
		req.Body = newPassedBody(request.body, int64(len(request.body)))
	case len(request.body) > 0:
		req.Body = io.NopCloser(bytes.NewReader(request.body))
	}
	req = h.prepare(req, st)
	if err := h.engine.request(req, h2NoHijack{}, &wireRequest{h2: &request}); err != nil {
		// A refused request: its stream is refused, not the connection.
		h.resetStream(st.id, http2.ErrCodeRefusedStream)
		return
	}
	pair := requestPair(req)
	if pair == nil {
		h.resetStream(st.id, http2.ErrCodeInternal)
		return
	}
	st.pair = pair
	requestDropped := pair.Dropped()
	res, wire, cut := h.roundTrip(pair, st)
	if res == nil {
		return
	}
	// A streamed response's head runs the response chain with an empty
	// body; its stream messages follow (h2engine_streamed.go).
	var streamed *h2StreamedBody
	var d *streamedDirection
	if wire != nil && wire.h2 != nil && wire.h2.streamed != nil {
		streamed = wire.h2.streamed
		d = h.engine.proxy.streamed(pair).direction(domain.StreamDirectionResponse, streamed.framing)
	}
	h.engine.response(res, wire)
	switch {
	case h.gone(st):
		if streamed != nil {
			streamed.reset(http2.ErrCodeCancel)
		}
		return
	case !requestDropped && pair.Dropped():
		// A dropped response: the client's stream is cancelled, as an
		// HTTP/1.1 client's connection is closed.
		if streamed != nil {
			streamed.reset(http2.ErrCodeCancel)
		}
		h.resetStream(st.id, http2.ErrCodeCancel)
		return
	case streamed != nil && pair.FromMarasi():
		// The chain answered with a response of Marasi's own.
		streamed.reset(http2.ErrCodeCancel)
	case streamed != nil:
		if pair.responseWire != nil {
			wire = pair.responseWire
		}
		h.carryStreamedResponse(st, pair, res, wire, streamed, d)
		return
	}
	if cut != nil {
		// No response modifier ran: what arrived goes on as it arrived,
		// then the stream is reset with the origin's code.
		h.writeMessage(st, wire.h2.fields, wire.h2.body, nil, false, false)
		h.resetStream(st.id, *cut)
		return
	}
	// The response step may have replaced the wire message: a Checkpoint
	// edit is a new field list.
	read := wire
	if pair.responseWire != nil {
		wire = pair.responseWire
	}
	h.writeResponse(st, pair, res, wire, read)
	if read != nil && read.h2 != nil && read.h2.rest != nil && !read.h2.rest.taken() {
		// A passed response the client did not get: the rest is not
		// wanted, so the origin stream is cancelled.
		h.mu.Lock()
		sent, origin := st.origin, h.origin
		h.mu.Unlock()
		if sent != nil && origin != nil {
			origin.cancel(sent, http2.ErrCodeCancel)
		}
	}
}

// prepare gives a request view what every request needs, as h1Conn.prepare
// does: the client's address, the https scheme, the URL's host and the
// stream's context. Inside a tunnel the URL's host is the tunnel's origin.
func (h *h2Conn) prepare(req *http.Request, st *h2Stream) *http.Request {
	req.RemoteAddr = h.conn.RemoteAddr().String()
	if tlsConn, ok := h.conn.(*tls.Conn); ok {
		state := tlsConn.ConnectionState()
		req.TLS = &state
	}
	req.URL.Scheme, _ = h.scheme()
	req.URL.Host = cmp.Or(withoutDefaultPort(h.c.tunnel, req.URL.Scheme), req.Host)
	// The streams behind a held stream do not wait for it while it is held.
	return req.WithContext(withHoldHooks(st.ctx, holdHooks{started: func() { h.held(st, true) }, ended: func() { h.held(st, false) }}))
}

// roundTrip gets the pair's response, as h1Conn.roundTrip does. A nil
// response means the stream ended without one: the client reset it, the
// origin reset it before its response, or an origin GOAWAY left it
// unanswered. cut is the origin's reset code when it reset the stream
// partway through the response body.
func (h *h2Conn) roundTrip(pair *Pair, st *h2Stream) (res *http.Response, wire *wireResponse, cut *http2.ErrCode) {
	switch {
	case pair.control == controlRespond:
		return pair.Response, nil, nil
	case pair.Dropped():
		req := pair.Request
		return &http.Response{
			Status: "200 OK", StatusCode: http.StatusOK,
			Proto: req.Proto, ProtoMajor: req.ProtoMajor, ProtoMinor: req.ProtoMinor,
			Header: http.Header{}, Body: http.NoBody, Request: req,
		}, nil, nil
	}
	res, wire, cut, err := h.sendUpstream(pair, st)
	switch {
	case errors.Is(err, errStreamGone):
		return nil, nil, nil
	case err != nil:
		proxy := h.engine.proxy
		proxy.roundTripFailed(pair.Request, err)
		proxy.respondWithProxyError(pair, http.StatusBadGateway, err)
		proxy.logProxyError(pair, err)
		return pair.Response, nil, nil
	}
	return res, wire, cut
}

// h2AfterHeadSent, when set, runs once a stream's request head went out on
// the origin leg the handshake dialled. Only tests set it, to land an origin
// frame right after a head.
var h2AfterHeadSent func(clientID uint32)

// sendUpstream sends the pair's request on an origin stream of its own and
// waits for the response.
func (h *h2Conn) sendUpstream(pair *Pair, st *h2Stream) (*http.Response, *wireResponse, *http2.ErrCode, error) {
	req := pair.Request
	// A route swap sends the client GOAWAY; this stream, already open,
	// finishes on the route its origin connection is on.
	h.engine.proxy.resolveRoute()
	// The origin the handshake dialled, named as the waypoint modifier
	// leaves it or as the handshake did. Any other, or another scheme, was
	// redirected to.
	scheme, defaultPort := h.scheme()
	if req.URL.Scheme != scheme {
		port, ok := carried(req.URL.Scheme)
		if !ok {
			return nil, nil, nil, errRouteNotCarried
		}
		return h.sendRedirected(pair, st, hostPort(req.URL.Host, port))
	}
	addr := hostPort(req.URL.Host, defaultPort)
	h.openOriginFor(req.Context(), addr)
	if addr != h.originAddr && (h.originDial == nil || addr != h.originDial.target) {
		return h.sendRedirected(pair, st, addr)
	}
	origin := h.originLeg()
	if origin == nil {
		if h.originDial != nil {
			recordTried(req.Context(), h.originDial.route)
		}
		return nil, nil, nil, h.originErr
	}
	if pair.Route != nil {
		pair.Route.Connected(origin.conn.route.Snapshot())
	}
	pair.originProtocol = domain.ProtocolHTTP2
	out, err := h2OutboundRequest(pair.requestWire.h2, req)
	if err != nil {
		return nil, nil, nil, err
	}
	h.waitTurn(st)
	sent, err := h.sendOn(origin, st, pair, out, func(originID uint32) {
		h.sentMu.Lock()
		h.sent[st.id] = originID
		h.sentMu.Unlock()
	})
	if h2AfterHeadSent != nil {
		h2AfterHeadSent(st.id)
	}
	h.passTurn(st)
	if errors.Is(err, errOriginGoneAway) {
		// The origin's GOAWAY came before this stream went out. Once it
		// is relayed, a stream it left unanswered is gone.
		<-origin.relayed
		if h.gone(st) {
			return nil, nil, nil, errStreamGone
		}
	}
	if err != nil {
		return nil, nil, nil, err
	}
	h.mu.Lock()
	if st.gone {
		h.mu.Unlock()
		origin.cancel(sent, http2.ErrCodeCancel)
		return nil, nil, nil, errStreamGone
	}
	st.origin = sent
	h.mu.Unlock()
	return h.awaitResponse(st, req, sent)
}

// awaitResponse waits for the response on origin stream s, relaying its
// interim responses to st.
func (h *h2Conn) awaitResponse(st *h2Stream, req *http.Request, s *h2OriginStream) (*http.Response, *wireResponse, *http2.ErrCode, error) {
	for done := false; !done; {
		select {
		case <-s.moreInterim:
		case <-s.done:
			done = true
		case <-s.headed:
			// A streamed response: its head is all there is so far.
			done = true
		case <-s.passed:
			done = true
		case <-st.ctx.Done():
			return nil, nil, nil, errStreamGone
		}
		for _, fields := range s.takeInterim() {
			h.relayInterim(st, fields)
		}
	}
	if s.streamed.Load() != nil {
		// The head came before headed closed; the body goes on in its
		// inbound, however the stream ends.
		if h.gone(st) {
			return nil, nil, nil, errStreamGone
		}
		res := s.response.response()
		res.Request = req
		return res, &wireResponse{h2: s.response}, nil, nil
	}
	if s.passing() != nil {
		// The response goes with what arrived; the rest follows
		// (writeResponse).
		for _, fields := range s.takeInterim() {
			h.relayInterim(st, fields)
		}
		wire := s.response
		res := wire.response()
		res.Request = req
		res.Body = newPassedBody(wire.body, int64(len(wire.body)))
		return res, &wireResponse{h2: wire}, nil, nil
	}
	switch {
	case errors.Is(s.err, errStreamGone), h.gone(st):
		return nil, nil, nil, errStreamGone
	case s.err != nil:
		return nil, nil, nil, s.err
	case !s.final && s.reset != nil:
		h.resetStream(st.id, *s.reset)
		return nil, nil, nil, errStreamGone
	case !s.final:
		return nil, nil, nil, errors.New("the origin ended the stream without a response")
	}
	wire := s.response
	res := wire.response()
	res.Request = req
	switch {
	case s.reset != nil:
		res.Body = cutShortBody(wire.body, fmt.Errorf("the origin reset the stream (%s)", *s.reset))
	case len(wire.body) > 0:
		res.Body = io.NopCloser(bytes.NewReader(wire.body))
	}
	return res, &wireResponse{h2: wire}, s.reset, nil
}

// relayInterim writes an origin's interim response to the client, except a
// 100: an HTTP/2 request's body is read whole before it goes upstream,
// unless it passed the body threshold.
func (h *h2Conn) relayInterim(st *h2Stream, fields h2Fields) {
	h.mu.Lock()
	passing := st.pass != nil
	h.mu.Unlock()
	if status, _ := fields.get(":status"); (status == "100" && !passing) || h.gone(st) {
		return
	}
	h.w.headers(fixedID(st.id), fields, false, h.flow.frameSize())
}

// writeResponse writes the pair's response on st: the origin's as its field
// list with the chain's edits, or one Marasi made. read is the response as
// the origin sent it, which wire replaces after a Checkpoint edit.
func (h *h2Conn) writeResponse(st *h2Stream, pair *Pair, res *http.Response, wire, read *wireResponse) {
	if wire != nil && wire.h2 != nil && !pair.FromMarasi() {
		client, err := encodedForClient(pair, res)
		if err == nil {
			base := wire.h2
			if client != res && read != nil && read.h2 != nil {
				base = withCodingOf(base, read.h2)
			}
			var out *h2Response
			if out, err = h2OutboundResponse(base, client); err == nil {
				if out.rest != nil {
					h.writeMessage(st, out.fields, out.body, nil, false, false)
					h.forwardResponsePass(st, out.rest)
					return
				}
				h.writeMessage(st, out.fields, out.body, out.trailers, true, out.endsOnData)
				return
			}
		}
		cause := fmt.Errorf("writing the response for the client: %w", err)
		h.engine.proxy.respondWithProxyError(pair, http.StatusBadGateway, cause)
		h.engine.proxy.logProxyError(pair, cause)
		res = pair.Response
	}
	payload, _ := readViewBody(&res.Body)
	if pair.Request != nil && pair.Request.Method == http.MethodHead {
		payload = nil
	}
	h.writeMessage(st, h2FieldsFromHeader(res.StatusCode, res.Header, len(payload)), payload, nil, true, false)
}

// withCodingOf is m with origin's content-encoding fields back where origin
// had them, when m has none. Checkpoint shows a decoded body without its
// content-encoding, so an edit's field list lacks it; when the client gets
// the coded body after all (encodedForClient), the field goes back to its
// place rather than last.
func withCodingOf(m, origin *h2Response) *h2Response {
	coding := func(f h2Field) bool { return strings.EqualFold(f.Name, "content-encoding") }
	if slices.ContainsFunc(m.fields, coding) {
		return m
	}
	fields := slices.Clone(m.fields)
	for i, f := range origin.fields {
		if coding(f) {
			fields = slices.Insert(fields, min(i, len(fields)), f)
		}
	}
	out := *m
	out.fields = fields
	return &out
}

// writeMessage writes a header block, a body and trailers on st. With end
// set END_STREAM falls on the last of them, on an empty DATA frame when
// endsOnData asks; without it the stream stays open.
func (h *h2Conn) writeMessage(st *h2Stream, fields h2Fields, body []byte, trailers h2Fields, end, endsOnData bool) {
	if h.gone(st) {
		return
	}
	hasBody := len(body) > 0 || (end && endsOnData)
	hasTrailers := end && len(trailers) > 0
	if _, err := h.w.headers(fixedID(st.id), fields, end && !hasBody && !hasTrailers, h.flow.frameSize()); err != nil {
		return
	}
	if hasBody {
		if err := sendData(h.w, h.flow, &st.window, st.id, body, end && !hasTrailers); err != nil {
			return
		}
	}
	if hasTrailers && !h.gone(st) {
		h.w.headers(fixedID(st.id), trailers, true, h.flow.frameSize())
	}
}

// h2OutboundRequest is the request as it goes to the origin: the client's
// field list with the view's edits merged, and the view's body (withPayloadLength).
func h2OutboundRequest(wire *h2Request, req *http.Request) (*h2Request, error) {
	payload, err := readViewBody(&req.Body)
	if err != nil {
		return nil, err
	}
	fields := withPayloadLength(wire.merge(req), wire.body, payload)
	return &h2Request{fields: fields, body: payload, trailers: wire.trailers, endsOnData: wire.endsOnData || len(payload) > 0, rest: wire.rest}, nil
}

// h2OutboundResponse is the response as it goes to the client, as
// h2OutboundRequest is the request.
func h2OutboundResponse(wire *h2Response, res *http.Response) (*h2Response, error) {
	payload, err := readViewBody(&res.Body)
	if err != nil {
		return nil, err
	}
	fields := withPayloadLength(wire.merge(res), wire.body, payload)
	return &h2Response{fields: fields, body: payload, trailers: wire.trailers, endsOnData: wire.endsOnData || len(payload) > 0, rest: wire.rest}, nil
}

// withPayloadLength is a merged field list for payload, which replaces the wire's
// body: a body the chain changed gets its content-length, when it has one,
// set to its new length; an unchanged message goes as it came.
func withPayloadLength(fields h2Fields, body, payload []byte) h2Fields {
	if bytes.Equal(payload, body) {
		return fields
	}
	if _, ok := fields.get("content-length"); ok {
		return fields.set("content-length", strconv.Itoa(len(payload)))
	}
	return fields
}
