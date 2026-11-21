package streamorigin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// h1Conn is one side of an HTTP/1.1 connection carrying one request and
// its response. Its reader records the message that arrives, cutting the
// body into stream messages as each chunk or read arrives.
type h1Conn struct {
	side string
	t    *transcript
	conn net.Conn

	mu      sync.Mutex // guards writes
	chunked bool       // the head sent declared Transfer-Encoding: chunked
	// untilClose is set when the head sent was a response's with neither
	// a length nor chunked coding: its body ends when the connection does.
	untilClose bool
	// absolute, on a client sending to a proxy in absolute form, is the
	// origin's host:port for the request target.
	absolute string
}

func newH1Conn(side string, t *transcript, conn net.Conn) *h1Conn {
	return &h1Conn{side: side, t: t, conn: conn}
}

// head sends a start line made of the pseudo-header fields, then the
// other fields as they are.
func (c *h1Conn) head(fields []h2.Field, _ bool) error {
	var b strings.Builder
	pseudo := map[string]string{}
	var rest []h2.Field
	for _, f := range fields {
		if strings.HasPrefix(f.Name, ":") {
			pseudo[f.Name] = f.Value
		} else {
			rest = append(rest, f)
		}
	}
	if status, ok := pseudo[":status"]; ok {
		code, _ := strconv.Atoi(status)
		fmt.Fprintf(&b, "HTTP/1.1 %s %s\r\n", status, http.StatusText(code))
	} else {
		target := pseudo[":path"]
		if c.absolute != "" {
			target = "http://" + c.absolute + target
		}
		fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", pseudo[":method"], target)
	}
	chunked, length := false, false
	for _, f := range rest {
		fmt.Fprintf(&b, "%s: %s\r\n", f.Name, f.Value)
		if strings.EqualFold(f.Name, "transfer-encoding") && strings.EqualFold(f.Value, "chunked") {
			chunked = true
		}
		if strings.EqualFold(f.Name, "content-length") {
			length = true
		}
	}
	b.WriteString("\r\n")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunked = chunked
	_, response := pseudo[":status"]
	c.untilClose = response && !chunked && !length
	_, err := io.WriteString(c.conn, b.String())
	return err
}

// message sends data as one chunk of a chunked body, ending it with the
// last chunk when end is set, or as it is otherwise. The last message of a
// body read until close closes the connection's write side.
func (c *h1Conn) message(data []byte, end bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := data
	if c.chunked {
		out = fmt.Appendf(nil, "%x\r\n%s\r\n", len(data), data)
		if end {
			out = append(out, "0\r\n\r\n"...)
		}
	}
	if _, err := c.conn.Write(out); err != nil {
		return err
	}
	if end && c.untilClose {
		if cw, ok := c.conn.(interface{ CloseWrite() error }); ok {
			return cw.CloseWrite()
		}
		return c.conn.Close()
	}
	return nil
}

// reset closes the connection: HTTP/1.1 has no stream to reset.
func (c *h1Conn) reset(http2.ErrCode) error { return c.conn.Close() }

// trailers sends the last chunk with a trailer section.
func (c *h1Conn) trailers(fields []h2.Field) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.chunked {
		return errors.New("trailers need a chunked body")
	}
	var b strings.Builder
	b.WriteString("0\r\n")
	for _, f := range fields {
		fmt.Fprintf(&b, "%s: %s\r\n", f.Name, f.Value)
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(c.conn, b.String())
	return err
}

// read records one message: the origin reads a request, the client a
// response after any interim ones. onHead is called once the request's
// head is recorded. A connection that ends before the message does is
// recorded as closed.
func (c *h1Conn) read(br *bufio.Reader, method string, onHead func()) {
	var split splitter
	closed := func() {
		for _, a := range split.end(c.side) {
			c.t.add(a)
		}
		c.t.add(Arrival{Side: c.side, Kind: KindClosed})
	}
	var fields []h2.Field
	var status int
	for {
		line, err := readLine(br)
		if err != nil {
			if c.side == SideClient {
				closed()
			}
			return
		}
		parts := strings.SplitN(line, " ", 3)
		if c.side == SideOrigin {
			if len(parts) != 3 {
				closed()
				return
			}
			fields = h2.Fields(":method", parts[0], ":path", parts[1])
		} else {
			if len(parts) < 2 {
				closed()
				return
			}
			status, _ = strconv.Atoi(parts[1])
			fields = h2.Fields(":status", parts[1])
		}
		header, err := readFields(br)
		if err != nil {
			closed()
			return
		}
		fields = append(fields, header...)
		contentType := ""
		for _, f := range header {
			if strings.EqualFold(f.Name, "content-type") {
				contentType = f.Value
			}
		}
		c.t.add(Arrival{Side: c.side, Kind: KindHead, Fields: fields})
		split.head(contentType, header)
		if c.side == SideOrigin || status >= 200 || status == 101 {
			break
		}
	}
	body := func(data []byte) {
		for _, m := range split.write(data) {
			c.t.add(Arrival{Side: c.side, Kind: KindMessage, Data: m})
		}
	}
	end := func() {
		for _, a := range split.end(c.side) {
			c.t.add(a)
		}
		c.t.add(Arrival{Side: c.side, Kind: KindEnd})
	}
	length, chunked, hasLength := 0, false, false
	for _, f := range fields {
		switch {
		case strings.EqualFold(f.Name, "transfer-encoding"):
			chunked = strings.EqualFold(strings.TrimSpace(f.Value), "chunked")
		case strings.EqualFold(f.Name, "content-length"):
			length, _ = strconv.Atoi(f.Value)
			hasLength = true
		}
	}
	noBody := status == 204 || status == 304 || method == "HEAD" || status == 101
	// A message with no body has ended once its head is read: record that
	// before onHead lets the origin's script answer, or the answer can
	// reach the client and be recorded first.
	if noBody || (!chunked && hasLength && length == 0) || (c.side == SideOrigin && !chunked && !hasLength) {
		end()
		if onHead != nil {
			onHead()
		}
		return
	}
	if onHead != nil {
		onHead()
	}
	switch {
	case chunked:
		for {
			line, err := readLine(br)
			if err != nil {
				closed()
				return
			}
			size, _, _ := strings.Cut(line, ";")
			n, err := strconv.ParseInt(strings.TrimSpace(size), 16, 64)
			if err != nil {
				closed()
				return
			}
			if n == 0 {
				trailers, err := readFields(br)
				if err != nil {
					closed()
					return
				}
				if len(trailers) > 0 {
					c.t.add(Arrival{Side: c.side, Kind: KindTrailers, Fields: trailers})
				}
				end()
				return
			}
			data := make([]byte, n)
			if _, err := io.ReadFull(br, data); err != nil {
				body(data)
				closed()
				return
			}
			body(data)
			if _, err := readLine(br); err != nil {
				closed()
				return
			}
		}
	case hasLength || c.side == SideOrigin:
		// A request with neither framing has no body (RFC 9112 §6.3).
		for length > 0 {
			buf := make([]byte, min(length, 32<<10))
			n, err := br.Read(buf)
			body(buf[:n])
			length -= n
			if err != nil {
				closed()
				return
			}
		}
		end()
	default:
		// A response body that runs until the connection closes.
		buf := make([]byte, 32<<10)
		for {
			n, err := br.Read(buf)
			body(buf[:n])
			if err != nil {
				end()
				return
			}
		}
	}
}

// readFields reads field lines up to the empty line, keeping each name's case.
func readFields(br *bufio.Reader) ([]h2.Field, error) {
	var fields []h2.Field
	for {
		line, err := readLine(br)
		if err != nil {
			return nil, err
		}
		if line == "" {
			return fields, nil
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("malformed field line %q", line)
		}
		fields = append(fields, h2.Field{Name: name, Value: strings.Trim(value, " \t")})
	}
}

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
