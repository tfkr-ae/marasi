package marasi

import (
	"bytes"
	"cmp"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"golang.org/x/net/http2"
)

// One client stream's request/response pair through the base pipeline, as
// h1Conn.carryPair carries one on HTTP/1.1: the request step, the round trip
// on the stream's own origin stream, the response step, and the response
// written back on the stream.

// errH2Redirect is the cause when a modifier sent an HTTP/2 request to an
// origin other than the one the client's handshake dialled.
var errH2Redirect = errors.New("an HTTP/2 request sent to another origin is not carried yet")

// carry carries st's request, which is whole, and writes its response.
func (h *h2Conn) carry(st *h2Stream) {
	h.mu.Lock()
	request := st.request
	h.mu.Unlock()
	req := request.request()
	if len(request.body) > 0 {
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
	requestDropped := pair.Dropped()
	res, wire, cut := h.roundTrip(pair, st)
	if res == nil {
		return
	}
	h.engine.response(res, wire)
	if h.gone(st) {
		return
	}
	if !requestDropped && pair.Dropped() {
		// A dropped response: the client's stream is cancelled, as an
		// HTTP/1.1 client's connection is closed.
		h.resetStream(st.id, http2.ErrCodeCancel)
		return
	}
	if cut != nil {
		// No response modifier ran: what arrived goes on as it arrived,
		// then the stream is reset with the origin's code.
		h.writeMessage(st, wire.h2.fields, wire.h2.body, nil, false, false)
		h.resetStream(st.id, *cut)
		return
	}
	h.writeResponse(st, pair, res, wire)
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
	req.URL.Scheme = "https"
	if h.cleartext() {
		req.URL.Scheme = "http"
	}
	req.URL.Host = cmp.Or(withoutDefaultPort(h.c.tunnel, req.URL.Scheme), req.Host)
	return req.WithContext(st.ctx)
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

// sendUpstream sends the pair's request on an origin stream of its own and
// waits for the response.
func (h *h2Conn) sendUpstream(pair *Pair, st *h2Stream) (*http.Response, *wireResponse, *http2.ErrCode, error) {
	req := pair.Request
	// A route swap sends the client GOAWAY; this stream, already open,
	// finishes on the route its origin connection is on.
	h.engine.proxy.resolveRoute()
	// The origin the handshake dialled, named as the waypoint modifier
	// leaves it or as the handshake did.
	addr := hostPort(req.URL.Host, h.defaultPort())
	h.openOriginFor(req.Context(), addr)
	if addr != h.originAddr && (h.originDial == nil || addr != h.originDial.target) {
		return nil, nil, nil, fmt.Errorf("%w: %s", errH2Redirect, addr)
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
	out, err := h2OutboundRequest(pair.requestWire.h2, req)
	if err != nil {
		return nil, nil, nil, err
	}
	h.waitTurn(st)
	os, err := origin.send(out)
	h.passTurn(st)
	if err != nil {
		return nil, nil, nil, err
	}
	h.mu.Lock()
	if st.gone {
		h.mu.Unlock()
		origin.cancel(os, http2.ErrCodeCancel)
		return nil, nil, nil, errStreamGone
	}
	st.origin = os
	h.sent[st.id] = os.id
	h.mu.Unlock()

	for done := false; !done; {
		select {
		case fields := <-os.interim:
			h.relayInterim(st, fields)
		case <-os.done:
			done = true
		case <-st.ctx.Done():
			return nil, nil, nil, errStreamGone
		}
	}
	for len(os.interim) > 0 {
		h.relayInterim(st, <-os.interim)
	}
	switch {
	case errors.Is(os.err, errStreamGone), h.gone(st):
		return nil, nil, nil, errStreamGone
	case os.err != nil:
		return nil, nil, nil, os.err
	case !os.final && os.reset != nil:
		h.resetStream(st.id, *os.reset)
		return nil, nil, nil, errStreamGone
	case !os.final:
		return nil, nil, nil, errors.New("the origin ended the stream without a response")
	}
	wire := os.response
	res := wire.response()
	res.Request = req
	switch {
	case os.reset != nil:
		res.Body = cutShortBody(wire.body, fmt.Errorf("the origin reset the stream (%s)", *os.reset))
	case len(wire.body) > 0:
		res.Body = io.NopCloser(bytes.NewReader(wire.body))
	}
	return res, &wireResponse{h2: wire}, os.reset, nil
}

// relayInterim writes an origin's interim response to the client, except a
// 100: an HTTP/2 request's body is read whole before it goes upstream.
func (h *h2Conn) relayInterim(st *h2Stream, fields h2Fields) {
	if status, _ := fields.get(":status"); status == "100" || h.gone(st) {
		return
	}
	h.w.headers(fixedID(st.id), fields, false, h.flow.frameSize())
}

// writeResponse writes the pair's response on st: the origin's as its field
// list with the chain's edits, or one Marasi made.
func (h *h2Conn) writeResponse(st *h2Stream, pair *Pair, res *http.Response, wire *wireResponse) {
	if wire != nil && wire.h2 != nil && !pair.FromMarasi() {
		client, err := encodedForClient(pair, res)
		if err == nil {
			var out *h2Response
			if out, err = h2OutboundResponse(wire.h2, client); err == nil {
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
// field list with the view's edits merged, and the view's body. A body the
// chain changed gets its content-length, when it has one, set to its new
// length; an unchanged message goes as it came.
func h2OutboundRequest(wire *h2Request, req *http.Request) (*h2Request, error) {
	payload, err := readViewBody(&req.Body)
	if err != nil {
		return nil, err
	}
	fields := wire.merge(req)
	if !bytes.Equal(payload, wire.body) {
		if _, ok := fields.get("content-length"); ok {
			fields = fields.set("content-length", strconv.Itoa(len(payload)))
		}
	}
	return &h2Request{fields: fields, body: payload, trailers: wire.trailers, endsOnData: wire.endsOnData || len(payload) > 0}, nil
}

// h2OutboundResponse is the response as it goes to the client, as
// h2OutboundRequest is the request.
func h2OutboundResponse(wire *h2Response, res *http.Response) (*h2Response, error) {
	payload, err := readViewBody(&res.Body)
	if err != nil {
		return nil, err
	}
	fields := wire.merge(res)
	if !bytes.Equal(payload, wire.body) {
		if _, ok := fields.get("content-length"); ok {
			fields = fields.set("content-length", strconv.Itoa(len(payload)))
		}
	}
	return &h2Response{fields: fields, body: payload, trailers: wire.trailers, endsOnData: wire.endsOnData || len(payload) > 0}, nil
}
