package marasi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// A launch and an Armory request are sent raw: their text is written as bytes
// to Marasi's own proxy listener, so the origin receives the header case,
// order and whitespace that were typed, and the request still runs the whole
// pipeline and is captured. Nothing parses the text into an http.Request to
// send it, and no Go transport adds headers to it
// (docs/adr/2026-10-13-launches-are-tracked-and-cancellable.md, amended 2026-11-17).

// rawRequest is the raw text of a launch or Armory request, read with the
// codec and with its Content-Length recalculated.
type rawRequest struct {
	head   *h1.RequestHead // nil for HTTP/2 text
	h2     *h2Request      // the HTTP/2 text read as a field list, else nil
	body   []byte          // the body exactly as typed
	view   *http.Request   // a view of the request, for the launch record and checks
	scheme string          // http or https
}

// readRawRequest reads raw as a request head followed by its body, sets the
// Content-Length to the body's length (see recalculateContentLength), and
// checks that it names a host.
//
// Text whose request line names HTTP/2 is read in the HTTP/2 text form
// instead (readH2RawRequest), and its Content-Length is left as typed.
func readRawRequest(raw string, useHTTPS bool) (*rawRequest, error) {
	scheme := "http"
	if useHTTPS {
		scheme = "https"
	}
	if isH2Text(raw) {
		return readH2RawRequest(raw, scheme)
	}
	br := bufio.NewReader(strings.NewReader(raw))
	head, err := h1.ReadRequestHead(br)
	if err != nil {
		return nil, fmt.Errorf("reading http request: %w", err)
	}
	if head.Method == http.MethodConnect {
		return nil, errors.New("reading http request: a CONNECT cannot be sent raw")
	}
	body, _ := io.ReadAll(br)
	recalculateContentLength(&head.Header, len(body))
	view, err := head.Request()
	if err != nil {
		return nil, fmt.Errorf("reading http request: %w", err)
	}
	if view.Host == "" {
		return nil, errors.New("host header not found or is empty")
	}
	view.URL.Scheme, view.URL.Host = scheme, view.Host
	if payload := wireBody(head, body).Payload(); len(payload) > 0 {
		view.Body = io.NopCloser(bytes.NewReader(payload))
	}
	return &rawRequest{head: head, body: body, view: view, scheme: scheme}, nil
}

// wire is the request as the engine would have read it, so the launch record
// is stored in its wire form.
func (r *rawRequest) wire() *wireRequest {
	if r.h2 != nil {
		return &wireRequest{h2: r.h2}
	}
	return &wireRequest{head: r.head, body: wireBody(r.head, r.body)}
}

// wireBody reads body with the framing head gives it. A body the framing does
// not read whole is kept as it was typed.
func wireBody(head *h1.RequestHead, body []byte) *h1.Body {
	verbatim := &h1.Body{Framing: h1.Framing{Kind: h1.LengthFramed, Length: int64(len(body))}, Data: body}
	framing, err := h1.RequestFraming(head)
	if err != nil {
		return verbatim
	}
	br := bufio.NewReader(bytes.NewReader(body))
	read, err := h1.ReadBody(br, framing)
	if err != nil || br.Buffered() > 0 {
		return verbatim
	}
	return read
}

// recalculateContentLength sets every Content-Length line's value to n,
// keeping the line's position, name case and the whitespace around the value.
// It adds a Content-Length line at the end of the section when there is none
// and the body is not empty, and does nothing when the text uses
// Transfer-Encoding, whose framing is the text's own.
func recalculateContentLength(header *h1.Header, n int) {
	if header.Has("Transfer-Encoding") {
		return
	}
	value := strconv.Itoa(n)
	found := false
	for i, field := range header.Fields {
		if !strings.EqualFold(field.Name(), "Content-Length") {
			continue
		}
		found = true
		header.Fields[i].Raw = withValue(field.Raw, value)
	}
	if !found && n > 0 {
		header.Fields = append(header.Fields, h1.Field{Raw: []byte("Content-Length: " + value + lineEnd(header))})
	}
}

// withValue gives a field line with its value replaced. A value folded over
// several lines becomes one line.
func withValue(raw []byte, value string) []byte {
	colon := bytes.IndexByte(raw, ':')
	line, _, folded := bytes.Cut(raw, []byte("\n"))
	eol := "\n"
	if bytes.HasSuffix(line, []byte("\r")) {
		line, eol = line[:len(line)-1], "\r\n"
	}
	if folded && len(raw) > len(line)+len(eol) {
		return []byte(string(raw[:colon]) + ": " + value + eol)
	}
	start := colon + 1
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	end := len(line)
	for end > start && (line[end-1] == ' ' || line[end-1] == '\t') {
		end--
	}
	out := make([]byte, 0, len(raw)+len(value))
	out = append(out, line[:start]...)
	out = append(out, value...)
	out = append(out, line[end:]...)
	return append(out, eol...)
}

// lineEnd is the line end new lines of header use: bare LF when the section
// ends with one, CRLF otherwise.
func lineEnd(header *h1.Header) string {
	if bytes.Equal(header.End, []byte("\n")) {
		return "\n"
	}
	return "\r\n"
}

// rawResponse is what came back for a raw request. An upgrade's stream is
// the connection itself, which the caller then owns.
type rawResponse struct {
	statusCode int
	upgrade    io.ReadCloser
}

// sendRaw writes r to the proxy's own listener on a new connection, with the
// internal fields appended to its head, and reads the response to its end.
// The connection is closed when sendRaw returns, except for a 101, whose
// connection is returned as the upgrade stream. Cancelling ctx closes the
// connection, so the proxy sees the client leave; an HTTP/2 launch first
// resets its stream with CANCEL, which the proxy passes on to the origin.
//
// A plain-HTTP request goes in absolute form with the proxy's credentials,
// as a proxy client sends it. An HTTPS request goes through a CONNECT that
// carries the credentials, then TLS, with its text unchanged.
func (proxy *Proxy) sendRaw(ctx context.Context, r *rawRequest, internal [][2]string) (res *rawResponse, err error) {
	if proxy.Port == "" {
		return nil, errors.New("the proxy listener is not running")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(proxy.Addr, proxy.Port))
	if err != nil {
		return nil, fmt.Errorf("dialing the proxy listener: %w", err)
	}
	var cancelStream atomic.Pointer[func()]
	stop := context.AfterFunc(ctx, func() {
		if reset := cancelStream.Load(); reset != nil {
			(*reset)()
		}
		conn.Close()
	})
	defer func() {
		stop()
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		if err != nil || res.upgrade == nil {
			conn.Close()
		}
	}()

	credentials := "Basic " + base64.StdEncoding.EncodeToString([]byte("marasi:"+proxy.clientWorkToken))
	if r.h2 != nil {
		stream, err := proxy.connectRaw(ctx, conn, r.view.Host, credentials, r.scheme)
		if err != nil {
			return nil, err
		}
		return sendH2Raw(stream, r, internal, func(reset func()) { cancelStream.Store(&reset) })
	}
	var stream net.Conn = conn
	line := r.head.Line
	if r.scheme == "https" {
		if stream, err = proxy.connectRaw(ctx, conn, r.view.Host, credentials, ""); err != nil {
			return nil, err
		}
	} else {
		line = absoluteForm(r.head, r.view.Host)
		internal = append([][2]string{{"Proxy-Authorization", credentials}}, internal...)
	}

	var out bytes.Buffer
	out.Write(r.head.Lead)
	out.Write(line)
	for _, field := range r.head.Header.Fields {
		out.Write(field.Raw)
	}
	eol := lineEnd(&r.head.Header)
	for _, field := range internal {
		out.WriteString(field[0] + ": " + field[1] + eol)
	}
	out.Write(r.head.Header.End)
	out.Write(r.body)
	if _, err := stream.Write(out.Bytes()); err != nil {
		return nil, fmt.Errorf("writing the request: %w", err)
	}

	br := bufio.NewReader(stream)
	for {
		head, err := h1.ReadResponseHead(br)
		if err != nil {
			return nil, fmt.Errorf("reading the response: %w", err)
		}
		if head.Interim() {
			continue
		}
		if head.StatusCode == http.StatusSwitchingProtocols {
			return &rawResponse{statusCode: head.StatusCode, upgrade: &upgradeStream{Reader: br, Closer: stream}}, nil
		}
		framing, err := h1.ResponseFraming(head, r.head.Method)
		var framingErr *h1.FramingError
		if err != nil && !(errors.As(err, &framingErr) && framingErr.Cause == h1.CauseLengthAndTransferEncoding) {
			// Both lengths: the framing RFC 9112 §6.3 picks is read.
			return nil, fmt.Errorf("reading the response: %w", err)
		}
		if _, err := h1.ReadBody(br, framing); err != nil {
			return nil, fmt.Errorf("reading the response body: %w", err)
		}
		return &rawResponse{statusCode: head.StatusCode}, nil
	}
}

// tunnelHeadBuffer is the read buffer for the 200 that opens a launch's
// tunnel: the smallest bufio allows. The 200 has no body and the client
// speaks first inside the tunnel, so nothing past its head is buffered and
// lost.
const tunnelHeadBuffer = 16

// connectRaw opens a tunnel to host through the proxy's listener and starts
// TLS inside it, as a proxy client does for an HTTPS request. h2Scheme is
// the scheme of an HTTP/2 request, or "" for HTTP/1.1: an https one offers h2
// alone by ALPN and fails unless the proxy chose it, and an http one gets
// the plain tunnel, for prior knowledge.
func (proxy *Proxy) connectRaw(ctx context.Context, conn net.Conn, host, credentials, h2Scheme string) (net.Conn, error) {
	port := "443"
	if h2Scheme == "http" {
		port = "80"
	}
	authority := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		authority = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	connect := "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\nProxy-Authorization: " + credentials + "\r\n\r\n"
	if _, err := io.WriteString(conn, connect); err != nil {
		return nil, fmt.Errorf("opening the tunnel: %w", err)
	}
	head, err := h1.ReadResponseHead(bufio.NewReaderSize(conn, tunnelHeadBuffer))
	if err != nil {
		return nil, fmt.Errorf("opening the tunnel: %w", err)
	}
	if head.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opening the tunnel: the proxy answered %d", head.StatusCode)
	}
	if h2Scheme == "http" {
		return conn, nil
	}
	config := &tls.Config{}
	if proxy.MarasiClientTLSConfig != nil {
		config = proxy.MarasiClientTLSConfig.Clone()
	}
	if h2Scheme != "" {
		config.NextProtos = []string{domain.ProtocolHTTP2}
	}
	if config.ServerName == "" {
		name, _, err := net.SplitHostPort(authority)
		if err != nil {
			name = authority
		}
		config.ServerName = name
	}
	stream := tls.Client(conn, config)
	if err := stream.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("starting TLS in the tunnel: %w", err)
	}
	if h2Scheme != "" && stream.ConnectionState().NegotiatedProtocol != domain.ProtocolHTTP2 {
		return nil, errNoH2
	}
	return stream, nil
}

// absoluteForm gives the request line with an origin-form target turned into
// the absolute form a proxy reads. Any other target is sent as typed.
func absoluteForm(head *h1.RequestHead, host string) []byte {
	if !strings.HasPrefix(head.Target, "/") {
		return head.Line
	}
	absolute := head.Clone()
	absolute.SetTarget("http://" + host + head.Target)
	return absolute.Line
}

// upgradeStream reads a connection through the reader that already holds
// what arrived after a head.
type upgradeStream struct {
	*bufio.Reader
	io.Closer
}
