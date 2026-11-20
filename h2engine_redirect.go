package marasi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	defer conn.Close()
	// A stream that ends while in flight closes the connection, as a
	// direct client resetting its only stream would.
	stop := context.AfterFunc(st.ctx, func() { conn.Close() })
	defer stop()
	if pair.Route != nil {
		pair.Route.Connected(conn.route.Snapshot())
	}
	if conn.protocol() == domain.ProtocolHTTP2 {
		pair.originProtocol = domain.ProtocolHTTP2
		origin, err := startH2Origin(conn, addr, nil, nil, h.engine.proxy.bodyThreshold())
		if err != nil {
			return nil, nil, nil, err
		}
		defer origin.close()
		sent, err := origin.send(out)
		if err != nil {
			return nil, nil, nil, err
		}
		return h.awaitResponse(st, req, sent)
	}
	pair.originProtocol = domain.ProtocolHTTP1
	return h.sendTranslated(st, req, conn, out)
}

// sendTranslated sends out to an HTTP/1.1 origin on conn and reads its
// response back as an HTTP/2 one. An origin that ends the body early has
// what arrived forwarded and the client's stream reset with INTERNAL_ERROR.
func (h *h2Conn) sendTranslated(st *h2Stream, req *http.Request, conn io.ReadWriter, out *h2Request) (*http.Response, *wireResponse, *http2.ErrCode, error) {
	if _, err := conn.Write(h1RequestFromH2(out)); err != nil {
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
	body, bodyErr := h1.ReadBody(br, framing)
	if h.gone(st) {
		return nil, nil, nil, errStreamGone
	}
	wire := &h2Response{fields: h2FieldsFromH1(head.StatusCode, head.Header), body: body.Payload()}
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

// h1RequestFromH2 is an HTTP/2 request as HTTP/1.1 (RFC 9113 §8.3.1): the
// request line from :method and :path, a host field from :authority when
// there is none, the cookie fields joined into one (§8.2.3), then the body
// framed by content-length, or chunked with the trailers when there are any.
// Names go as they are, in lowercase.
func h1RequestFromH2(out *h2Request) []byte {
	method, _ := out.fields.get(":method")
	target, _ := out.fields.get(":path")
	authority, hasAuthority := out.fields.get(":authority")
	if target == "" {
		target = authority
	}
	var b bytes.Buffer
	b.WriteString(method + " " + target + " HTTP/1.1\r\n")
	if _, hasHost := out.fields.get("host"); !hasHost && hasAuthority {
		b.WriteString("host: " + authority + "\r\n")
	}
	var cookies []string
	for _, f := range out.fields {
		if f.Name == "cookie" {
			cookies = append(cookies, f.Value)
		}
	}
	chunked := len(out.trailers) > 0
	_, hasLength := out.fields.get("content-length")
	cookieWritten := false
	for _, f := range out.fields {
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
		b.WriteString("transfer-encoding: chunked\r\n\r\n")
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
	case !hasLength && (len(out.body) > 0 || out.endsOnData):
		b.WriteString("content-length: " + strconv.Itoa(len(out.body)) + "\r\n")
	}
	b.WriteString("\r\n")
	b.Write(out.body)
	return b.Bytes()
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
