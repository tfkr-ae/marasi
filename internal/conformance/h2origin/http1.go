package h2origin

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// A redirected case (Case.Redirect) sends its requests to an origin that
// speaks only HTTP/1.1. Its recording holds each HTTP/1.1 message as a
// stream: the request as :method and :path then its fields as they came,
// names in their case; the response as :status then its fields. Directly,
// the harness client speaks HTTP/1.1 to that origin, as a client sent
// there would; through a proxy it speaks HTTP/2 to the case's h2 origin and
// the proxy's Lua (Setup.Lua) redirects each request.

// RedirectField names, on a request through the proxy, the host:port of the
// HTTP/1.1 origin a redirected case's requests go to. The proxy's Lua
// removes it as it redirects.
const RedirectField = "x-conformance-redirect"

// redirectLua redirects a request carrying RedirectField to the origin it
// names, as a user's rule that changes the URL's host does.
const redirectLua = `

-- Conformance harness: a request carrying x-conformance-redirect goes to
-- the origin it names instead, as a rule that changes the URL's host does.
function conformanceRedirect(request)
	local headers = request:headers()
	local target = headers:get("X-Conformance-Redirect")
	if target then
		headers:delete("X-Conformance-Redirect")
		request:url():set_host(target)
	end
end

function processRequest(request)
	conformanceRedirect(request)
end
`

// extensionLua is the extension run's Lua: the redirect, and every body set
// to itself as conformance.HTTPExtension does.
const extensionLua = `

-- Conformance harness: a no-op edit through the extension path.
function processRequest(request)
	conformanceRedirect(request)
	request:set_body(request:body())
end

function processResponse(response)
	response:set_body(response:body())
end
`

// http1Origin is a TLS origin offering only http/1.1 by ALPN. It answers
// each request it reads with the same response bytes and records what each
// connection received.
type http1Origin struct {
	listener net.Listener
	response string
	timeout  time.Duration

	mu          sync.Mutex
	connections []*http1Conn
	wg          sync.WaitGroup
}

type http1Conn struct {
	conn    net.Conn
	alpn    string
	streams []h2.Stream
}

func startHTTP1Origin(listen, response string, timeout time.Duration, config *tls.Config) (*http1Origin, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(listen, "0"))
	if err != nil {
		return nil, err
	}
	config = config.Clone()
	config.NextProtos = []string{"http/1.1"}
	o := &http1Origin{listener: tls.NewListener(listener, config), response: response, timeout: timeout}
	o.wg.Go(func() {
		for {
			conn, err := o.listener.Accept()
			if err != nil {
				return
			}
			c := &http1Conn{conn: conn}
			o.mu.Lock()
			o.connections = append(o.connections, c)
			o.mu.Unlock()
			o.wg.Go(func() { o.serve(c) })
		}
	})
	return o, nil
}

func (o *http1Origin) port() string {
	_, port, _ := net.SplitHostPort(o.listener.Addr().String())
	return port
}

func (o *http1Origin) serve(c *http1Conn) {
	conn := c.conn.(*tls.Conn)
	_ = conn.SetDeadline(time.Now().Add(o.timeout))
	if err := conn.Handshake(); err != nil {
		return
	}
	o.mu.Lock()
	c.alpn = conn.ConnectionState().NegotiatedProtocol
	o.mu.Unlock()
	br := bufio.NewReader(conn)
	for {
		stream, err := readHTTP1Request(br)
		if err != nil {
			return
		}
		o.mu.Lock()
		c.streams = append(c.streams, stream)
		o.mu.Unlock()
		if _, err := io.WriteString(conn, o.response); err != nil {
			return
		}
	}
}

// snapshot records every connection as it is now, with every field value
// passed through normalise.
func (o *http1Origin) snapshot(normalise func(string) string) []h2.Connection {
	o.mu.Lock()
	defer o.mu.Unlock()
	connections := make([]h2.Connection, 0, len(o.connections))
	for _, c := range o.connections {
		connections = append(connections, h2.Connection{ALPN: c.alpn, Streams: normaliseStreams(c.streams, normalise)})
	}
	return connections
}

func (o *http1Origin) stop() {
	_ = o.listener.Close()
	o.mu.Lock()
	for _, c := range o.connections {
		_ = c.conn.Close()
	}
	o.mu.Unlock()
	o.wg.Wait()
}

// exchangeHTTP1 sends each request on conn as HTTP/1.1, one after the
// other, and records each response. The request is what an HTTP/2 request
// becomes in HTTP/1.1 (RFC 9113 §8.3.1): the request line, host from the
// authority, the fields as they are, and a content-length for a body.
func (s Suite) exchangeHTTP1(conn net.Conn, requests []Request, authority string) ([]h2.Stream, error) {
	br := bufio.NewReader(conn)
	streams := []h2.Stream{}
	for i, r := range requests {
		if len(r.Trailers) > 0 {
			return nil, fmt.Errorf("request %d: a redirected case's request cannot have trailers", i)
		}
		var b strings.Builder
		b.WriteString(r.Method + " " + r.Path + " HTTP/1.1\r\nhost: " + authority + "\r\n")
		for _, f := range r.Fields {
			b.WriteString(f.Name + ": " + f.Value + "\r\n")
		}
		if r.Body != "" {
			b.WriteString("content-length: " + strconv.Itoa(len(r.Body)) + "\r\n")
		}
		b.WriteString("\r\n" + r.Body)
		_ = conn.SetDeadline(time.Now().Add(s.timeout()))
		if _, err := io.WriteString(conn, b.String()); err != nil {
			return nil, fmt.Errorf("sending request %d: %w", i, err)
		}
		stream, err := readHTTP1Response(br)
		if err != nil {
			return nil, fmt.Errorf("reading the response to request %d: %w", i, err)
		}
		streams = append(streams, stream)
	}
	return streams, nil
}

// readHTTP1Request reads one request whose body, if any, has a
// content-length.
func readHTTP1Request(br *bufio.Reader) (h2.Stream, error) {
	line, err := readLine(br)
	if err != nil {
		return h2.Stream{}, err
	}
	parts := strings.Fields(line)
	if len(parts) != 3 {
		return h2.Stream{}, fmt.Errorf("malformed request line %q", line)
	}
	return readHTTP1Message(br, h2.Fields(":method", parts[0], ":path", parts[1]))
}

// readHTTP1Response reads one response, with any interim responses before
// it as header blocks of their own. Its body, if any, has a content-length.
func readHTTP1Response(br *bufio.Reader) (h2.Stream, error) {
	stream := h2.Stream{Headers: [][]h2.Field{}}
	for {
		line, err := readLine(br)
		if err != nil {
			return h2.Stream{}, err
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return h2.Stream{}, fmt.Errorf("malformed status line %q", line)
		}
		message, err := readHTTP1Message(br, h2.Fields(":status", parts[1]))
		if err != nil {
			return h2.Stream{}, err
		}
		stream.Headers = append(stream.Headers, message.Headers...)
		if !strings.HasPrefix(parts[1], "1") {
			stream.Data, stream.EndStream = message.Data, message.EndStream
			return stream, nil
		}
	}
}

// readHTTP1Message reads a header section after its first line, keeping
// each name's case, and the body a content-length gives.
func readHTTP1Message(br *bufio.Reader, fields []h2.Field) (h2.Stream, error) {
	length := 0
	for {
		line, err := readLine(br)
		if err != nil {
			return h2.Stream{}, err
		}
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return h2.Stream{}, fmt.Errorf("malformed field line %q", line)
		}
		value = strings.Trim(value, " \t")
		fields = append(fields, h2.Field{Name: name, Value: value})
		if strings.EqualFold(name, "content-length") {
			if length, err = strconv.Atoi(value); err != nil {
				return h2.Stream{}, fmt.Errorf("content-length %q: %w", value, err)
			}
		} else if strings.EqualFold(name, "transfer-encoding") {
			return h2.Stream{}, errors.New("a redirected case's messages are framed by content-length only")
		}
	}
	stream := h2.Stream{Headers: [][]h2.Field{fields}, EndStream: h2.EndHeaders}
	if length > 0 {
		stream.Data = make([]byte, length)
		if _, err := io.ReadFull(br, stream.Data); err != nil {
			return h2.Stream{}, err
		}
		stream.EndStream = h2.EndData
	}
	return stream, nil
}

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

// normaliseStreams copies streams with every field value passed through
// normalise.
func normaliseStreams(streams []h2.Stream, normalise func(string) string) []h2.Stream {
	out := make([]h2.Stream, len(streams))
	for i, s := range streams {
		s.Headers = make([][]h2.Field, len(streams[i].Headers))
		for j, block := range streams[i].Headers {
			fields := make([]h2.Field, len(block))
			for k, f := range block {
				fields[k] = h2.Field{Name: f.Name, Value: normalise(f.Value)}
			}
			s.Headers[j] = fields
		}
		out[i] = s
	}
	return out
}
