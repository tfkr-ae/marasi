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
	"strconv"
	"strings"

	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// A request on an HTTP/2 connection that a user sent to another origin, by a
// waypoint, a modifier that changed the URL's host, or a Checkpoint edit that
// named a target, gets an origin connection of its own, chosen origin first
// (docs/adr/2026-11-19-marasi-gives-the-client-the-origins-protocol.md). When
// that origin speaks HTTP/2 the request goes as it is; when it speaks only
// HTTP/1.1 the request/response pair is translated, and header case is lost.
// Translated, a streamed request is carried a stream message at a time and
// an ordinary body passes the body threshold as on any leg; a streamed
// response is read as an ordinary body, so up to the threshold or a stall.
// The connection carries that one request and is closed after its response.

// redirectedALPN is what a redirected request's origin is offered: the
// client's own ALPN list, as the handshake's origin was, or, for a client
// that sent none (h2c), h2 first as an HTTP/2 client going direct would.
func (h *h2Conn) redirectedALPN() []string {
	if h.originDial != nil && len(h.originDial.offered) > 0 {
		return h.originDial.offered
	}
	return []string{domain.ProtocolHTTP2, domain.ProtocolHTTP1}
}

// errH2Switched is the cause when an HTTP/1.1 origin answers a translated
// request with 101, which an HTTP/2 stream cannot carry.
var errH2Switched = errors.New("the HTTP/1.1 origin switched protocols, which an HTTP/2 stream cannot carry")

// sendRedirected sends the pair's request to addr, an origin other than the
// one the handshake dialled, on a connection of its own, and waits for the
// response. Streams behind it do not wait for it.
func (h *h2Conn) sendRedirected(pair *Pair, st *h2Stream, addr string) (*http.Response, *wireResponse, *http2.ErrCode, error) {
	h.passTurn(st)
	req := pair.Request
	out, err := h2OutboundRequest(pair.requestWire.h2, req)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := h.engine.proxy.dialOrigin(withOriginALPN(st.ctx, h.redirectedALPN()), req.URL.Scheme, addr)
	if err != nil {
		return nil, nil, nil, err
	}
	// A stream that ends while in flight closes the connection, as a
	// direct client resetting its only stream would.
	stop := context.AfterFunc(st.ctx, func() { conn.Close() })
	var origin *h2Origin
	// keep is set when a direction is streamed: the connection stays
	// open until it is carried (h2Conn.dispatch).
	keep := false
	release := func() {
		stop()
		if origin != nil {
			origin.close()
		}
		conn.Close()
	}
	defer func() {
		if keep || pair.stream != nil {
			st.release = append(st.release, release)
			return
		}
		release()
	}()
	if pair.Route != nil {
		pair.Route.Connected(conn.route.Snapshot())
	}
	if conn.protocol() == domain.ProtocolHTTP2 {
		pair.originProtocol = domain.ProtocolHTTP2
		origin, err = startH2Origin(conn, addr, nil, nil, h.engine.proxy.bodyThreshold())
		if err != nil {
			return nil, nil, nil, err
		}
		sent, err := h.sendOn(origin, st, pair, out, nil)
		if err != nil {
			return nil, nil, nil, err
		}
		res, wire, cut, err := h.awaitResponse(st, req, sent)
		keep = wire != nil && wire.h2.streamed != nil
		return res, wire, cut, err
	}
	pair.originProtocol = domain.ProtocolHTTP1
	res, wire, cut, err := h.sendTranslated(st, pair, req, conn, out)
	if wire != nil && wire.h2.rest != nil {
		// The rest of the response goes on after this returns; the
		// connection stays open until it went, and closing it stops
		// the rest's read.
		keep = true
		rest := wire.h2.rest
		stopRest := release
		release = func() {
			rest.cut(nil, errPassStopped)
			stopRest()
		}
	}
	keep = keep || out.rest != nil
	return res, wire, cut, err
}

// sendTranslated sends out to an HTTP/1.1 origin on conn and reads its
// response back as an HTTP/2 one. A response body is read up to the body
// threshold: past it, the response goes on with what arrived and the rest
// follows as it arrives. An origin that ends the body early has what
// arrived forwarded and the client's stream reset with INTERNAL_ERROR.
func (h *h2Conn) sendTranslated(st *h2Stream, pair *Pair, req *http.Request, conn net.Conn, out *h2Request) (*http.Response, *wireResponse, *http2.ErrCode, error) {
	if err := h.sendTranslatedRequest(st, pair, conn, out); err != nil {
		return nil, nil, nil, fmt.Errorf("writing the request to the origin: %w", err)
	}
	br := bufio.NewReader(conn)
	head, err := h1.ReadResponseHead(br)
	for err == nil && head.Interim() {
		h.relayInterim(st, h2FieldsFromH1(head.StatusCode, head.Header))
		head, err = h1.ReadResponseHead(br)
	}
	switch {
	case h.gone(st):
		return nil, nil, nil, errStreamGone
	case err != nil:
		return nil, nil, nil, fmt.Errorf("reading the origin's response: %w", err)
	case head.StatusCode == http.StatusSwitchingProtocols:
		return nil, nil, nil, errH2Switched
	}
	framing, err := h1.ResponseFraming(head, req.Method)
	var framingErr *h1.FramingError
	if err != nil && !(errors.As(err, &framingErr) && framingErr.Cause == h1.CauseLengthAndTransferEncoding) {
		return nil, nil, nil, err
	}
	threshold := h.engine.proxy.bodyThreshold()
	body, passing, bodyErr := readBodyOrPass(br, framing, threshold)
	if h.gone(st) {
		if passing != nil {
			passing.rest.close()
		}
		return nil, nil, nil, errStreamGone
	}
	wire := &h2Response{fields: h2FieldsFromH1(head.StatusCode, head.Header), body: body.Payload()}
	if passing != nil {
		// The payload is read again from the start, the wire bytes that
		// arrived and then the rest, as the client takes it.
		wire.body, _ = cutAtThreshold(wire.body, threshold)
		payload := h1.NewBodyReader(bufio.NewReader(io.MultiReader(bytes.NewReader(passing.arrived), passing.rest)), framing)
		wire.rest = newH2Passing(h2OriginStreamWindow, func(uint32) {}, nil)
		go feedPassing(wire.rest, payload, len(wire.body))
		res := wire.response()
		res.Request = req
		res.Body = newPassedBody(wire.body)
		return res, &wireResponse{h2: wire}, nil, nil
	}
	if len(body.Trailer.Fields) > 0 {
		wire.trailers = h2FieldsFromH1(0, body.Trailer)
	}
	res := wire.response()
	res.Request = req
	var cut *http2.ErrCode
	switch {
	case bodyErr != nil:
		code := http2.ErrCodeInternal
		cut = &code
		res.Body = cutShortBody(wire.body, bodyErr)
	case len(wire.body) > 0:
		res.Body = io.NopCloser(bytes.NewReader(wire.body))
	}
	return res, &wireResponse{h2: wire}, cut, nil
}

// sendTranslatedRequest writes out to the HTTP/1.1 origin on conn: whole
// when it was read whole, and otherwise its head and what arrived, with
// the rest after it on a goroutine of its own, so the response can come
// while it is sent. A streamed request is carried a stream message at a
// time; the rest of a passed one as it arrives. A body that goes on so is
// chunked, unless its head declares a length.
func (h *h2Conn) sendTranslatedRequest(st *h2Stream, pair *Pair, conn net.Conn, out *h2Request) error {
	streamed := st.request.streamed
	if streamed == nil && out.rest == nil {
		_, err := conn.Write(h1RequestFromH2(out))
		return err
	}
	_, declared := out.fields.get("content-length")
	var head bytes.Buffer
	writeH1RequestHead(&head, out.fields, !declared, "")
	if _, err := conn.Write(head.Bytes()); err != nil {
		return err
	}
	sink := &h1Sink{bw: bufio.NewWriter(conn), conn: conn, chunked: !declared, streams: &h1Streams{gone: make(chan struct{})}}
	if streamed != nil {
		sp := h.engine.proxy.streamed(pair)
		d := sp.direction(domain.StreamDirectionRequest, streamed.framing)
		sp.start(domain.StreamDirectionRequest, d, streamedHead{fields: out.fields, declared: declared}, streamed, sink, &st.carrying)
		return nil
	}
	rest := out.rest
	rest.take()
	go func() {
		if err := sink.forward(out.body); err != nil {
			rest.cut(nil, errPassStopped)
			close(rest.forwarded)
			return
		}
		rest.forward(sink)
	}()
	return nil
}

// feedPassing puts the payload r reads into p as p's window opens, so the
// origin is read only as fast as the client takes the body, then ends p as
// the body ended. The first skip bytes went with the head already.
func feedPassing(p *h2Passing, r *h1.BodyReader, skip int) {
	for {
		data, err := r.Peek()
		if n := min(skip, len(data)); n > 0 {
			r.Discard(n)
			skip -= n
			continue
		}
		if len(data) > 0 {
			n := p.in.awaitWindow(len(data))
			if n == 0 {
				return
			}
			p.in.put(data[:n], uint32(n))
			r.Discard(n)
			continue
		}
		if errors.Is(err, io.EOF) {
			var trailers h2Fields
			if trailer := r.Trailer(); len(trailer.Fields) > 0 {
				trailers = h2FieldsFromH1(0, trailer)
			}
			p.end(trailers)
			return
		}
		code := http2.ErrCodeInternal
		p.cut(&code, nil)
		return
	}
}

// h1RequestFromH2 is an HTTP/2 request as HTTP/1.1 (RFC 9113 §8.3.1): the
// request line from :method and :path, a host field from :authority when
// there is none, the cookie fields joined into one (§8.2.3), then the body
// framed by content-length, or chunked with the trailers when there are any.
// Names go as they are, in lowercase.
func h1RequestFromH2(out *h2Request) []byte {
	var b bytes.Buffer
	chunked := len(out.trailers) > 0
	length := ""
	if _, hasLength := out.fields.get("content-length"); !chunked && !hasLength && (len(out.body) > 0 || out.endsOnData) {
		length = strconv.Itoa(len(out.body))
	}
	writeH1RequestHead(&b, out.fields, chunked, length)
	if !chunked {
		b.Write(out.body)
		return b.Bytes()
	}
	if len(out.body) > 0 {
		b.WriteString(strconv.FormatInt(int64(len(out.body)), 16) + "\r\n")
		b.Write(out.body)
		b.WriteString("\r\n")
	}
	b.WriteString("0\r\n")
	for _, f := range out.trailers {
		b.WriteString(f.Name + ": " + f.Value + "\r\n")
	}
	b.WriteString("\r\n")
	return b.Bytes()
}

// writeH1RequestHead writes the HTTP/1.1 head of an HTTP/2 request with
// fields: chunked replaces any content-length with the chunked coding, and
// length, when set, is a content-length to add.
func writeH1RequestHead(b *bytes.Buffer, fields h2Fields, chunked bool, length string) {
	method, _ := fields.get(":method")
	target, _ := fields.get(":path")
	authority, hasAuthority := fields.get(":authority")
	if target == "" {
		target = authority
	}
	b.WriteString(method + " " + target + " HTTP/1.1\r\n")
	if _, hasHost := fields.get("host"); !hasHost && hasAuthority {
		b.WriteString("host: " + authority + "\r\n")
	}
	var cookies []string
	for _, f := range fields {
		if f.Name == "cookie" {
			cookies = append(cookies, f.Value)
		}
	}
	cookieWritten := false
	for _, f := range fields {
		switch {
		case f.pseudo(), f.Name == "transfer-encoding", chunked && f.Name == "content-length":
		case f.Name == "cookie":
			if !cookieWritten {
				cookieWritten = true
				b.WriteString("cookie: " + strings.Join(cookies, "; ") + "\r\n")
			}
		default:
			b.WriteString(f.Name + ": " + f.Value + "\r\n")
		}
	}
	switch {
	case chunked:
		b.WriteString("transfer-encoding: chunked\r\n")
	case length != "":
		b.WriteString("content-length: " + length + "\r\n")
	}
	b.WriteString("\r\n")
}

// h2FieldsFromH1 is an HTTP/1.1 header section as an HTTP/2 field list:
// :status first when status is set, then each field in wire order with its
// name in lowercase, less Connection, the fields it names and the other
// connection-specific fields HTTP/2 does not carry (RFC 9113 §8.2.2).
func h2FieldsFromH1(status int, header h1.Header) h2Fields {
	header.Fields = append([]h1.Field(nil), header.Fields...)
	header.RemoveConnectionOptions(false)
	var fields h2Fields
	if status != 0 {
		fields = append(fields, h2Field{Name: ":status", Value: strconv.Itoa(status)})
	}
	for _, f := range header.Fields {
		name := strings.ToLower(f.Name())
		switch name {
		case "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade":
			continue
		}
		fields = append(fields, h2Field{Name: name, Value: f.Value()})
	}
	return fields
}
