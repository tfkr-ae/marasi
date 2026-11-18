// Package h1 is Marasi's HTTP/1.1 codec. It reads and writes the start line,
// the header section as raw lines kept byte for byte, and the body framed by
// Content-Length, chunked coding or the end of the connection, with chunk
// extensions, trailers and 1xx interim responses.
//
// A message read and written back unchanged is byte-identical. Modifiers edit a
// message through http.Request and http.Response views, and Merge folds the
// edit back into the raw lines so that only what the edit names changes.
//
// The codec knows nothing about modifiers, storage or routing, and it imports
// only the standard library.
package h1

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
)

// MaxHeadBytes caps a head: the start line and the header section. It matches
// net/http's DefaultMaxHeaderBytes, so Marasi refuses no head that Go's own
// server accepted.
const MaxHeadBytes = 1 << 20

var (
	// ErrMalformedHead is returned for a start line or field line that does
	// not parse as HTTP/1.1.
	ErrMalformedHead = errors.New("malformed message head")
	// ErrUnsupportedVersion is returned, together with ErrMalformedHead,
	// for a request line whose version is well formed but whose major
	// version is not 1, which a server answers with 505 (RFC 9110 §15.6.6).
	ErrUnsupportedVersion = errors.New("unsupported HTTP major version")
	// ErrHeadTooLarge is returned when a head runs past MaxHeadBytes.
	ErrHeadTooLarge = errors.New("message head too large")
)

// RequestHead is a request line and its header section as they were on the
// wire.
type RequestHead struct {
	// Lead holds the empty lines a client sent before the request line, which
	// RFC 9112 §2.2 lets a server skip. They are kept so that writing the head
	// back gives the bytes that were read.
	Lead []byte
	// Line is the request line with its line end, as on the wire.
	Line []byte
	// Method, Target and Proto are parsed from Line.
	Method, Target, Proto string
	Header                Header
}

// ResponseHead is a status line and its header section as they were on the
// wire.
type ResponseHead struct {
	// Line is the status line with its line end, as on the wire.
	Line []byte
	// Proto, StatusCode and Reason are parsed from Line. Reason is empty when
	// the status line has none.
	Proto      string
	StatusCode int
	Reason     string
	Header     Header
}

// Interim reports whether the response is a 1xx interim response, which is
// followed by another response to the same request. 101 Switching Protocols
// is final.
func (h *ResponseHead) Interim() bool {
	return h.StatusCode >= 100 && h.StatusCode < 200 && h.StatusCode != 101
}

// ReadRequestHead reads a request line and header section. It returns io.EOF
// when the connection ends before the first byte of a request, and
// io.ErrUnexpectedEOF when it ends inside the head.
func ReadRequestHead(br *bufio.Reader) (*RequestHead, error) {
	var buf []byte
	var lead int
	for {
		start := len(buf)
		var err error
		buf, err = readLine(br, buf, MaxHeadBytes)
		if err != nil {
			if err == io.EOF && start == 0 {
				return nil, io.EOF
			}
			return nil, headReadError(err)
		}
		if len(trimEOL(buf[start:])) != 0 {
			lead = start
			break
		}
	}
	h := &RequestHead{}
	line := buf[lead:]
	method, target, proto, ok := parseRequestLine(trimEOL(line))
	if !ok {
		if otherMajorVersion(trimEOL(line)) {
			return nil, fmt.Errorf("parsing request line %q: %w: %w", trimEOL(line), ErrUnsupportedVersion, ErrMalformedHead)
		}
		return nil, fmt.Errorf("parsing request line %q: %w", trimEOL(line), ErrMalformedHead)
	}
	h.Method, h.Target, h.Proto = method, target, proto
	buf, fields, err := readFields(br, buf, MaxHeadBytes)
	if err != nil {
		return nil, err
	}
	if lead > 0 {
		h.Lead = buf[:lead:lead]
	}
	h.Line = buf[lead : lead+len(line) : lead+len(line)]
	h.Header = fields.header(buf)
	return h, nil
}

// ReadResponseHead reads a status line and header section. It returns io.EOF
// when the connection ends before the first byte of a response, and
// io.ErrUnexpectedEOF when it ends inside the head.
func ReadResponseHead(br *bufio.Reader) (*ResponseHead, error) {
	buf, err := readLine(br, nil, MaxHeadBytes)
	if err != nil {
		if err == io.EOF && len(buf) == 0 {
			return nil, io.EOF
		}
		return nil, headReadError(err)
	}
	lineLen := len(buf)
	h := &ResponseHead{}
	var ok bool
	h.Proto, h.StatusCode, h.Reason, ok = parseStatusLine(trimEOL(buf))
	if !ok {
		return nil, fmt.Errorf("parsing status line %q: %w", trimEOL(buf), ErrMalformedHead)
	}
	buf, fields, err := readFields(br, buf, MaxHeadBytes)
	if err != nil {
		return nil, err
	}
	h.Line = buf[:lineLen:lineLen]
	h.Header = fields.header(buf)
	return h, nil
}

// WriteTo writes the head as it stands: the bytes that were read, apart from
// what Merge or a set method changed.
func (h *RequestHead) WriteTo(w io.Writer) (int64, error) {
	return writeParts(w, h.Lead, h.Line, h.Header)
}

// WriteTo writes the head as it stands: the bytes that were read, apart from
// what Merge or a set method changed.
func (h *ResponseHead) WriteTo(w io.Writer) (int64, error) {
	return writeParts(w, nil, h.Line, h.Header)
}

// Bytes returns the head as WriteTo would write it.
func (h *RequestHead) Bytes() []byte {
	return appendParts(nil, h.Lead, h.Line, h.Header)
}

// Bytes returns the head as WriteTo would write it.
func (h *ResponseHead) Bytes() []byte {
	return appendParts(nil, nil, h.Line, h.Header)
}

// Clone returns a copy of the head that Merge and the other editing methods
// can change without changing h. The field bytes are shared: nothing in this
// package writes into them.
func (h *RequestHead) Clone() *RequestHead {
	c := *h
	c.Header.Fields = slices.Clone(h.Header.Fields)
	return &c
}

// Clone returns a copy of the head that Merge and the other editing methods
// can change without changing h, as RequestHead.Clone.
func (h *ResponseHead) Clone() *ResponseHead {
	c := *h
	c.Header.Fields = slices.Clone(h.Header.Fields)
	return &c
}

func writeParts(w io.Writer, lead, line []byte, header Header) (int64, error) {
	n, err := w.Write(appendParts(nil, lead, line, header))
	return int64(n), err
}

func appendParts(b, lead, line []byte, header Header) []byte {
	b = slices.Grow(b, len(lead)+len(line)+header.size())
	b = append(b, lead...)
	b = append(b, line...)
	return header.appendTo(b)
}

func headReadError(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readLine appends one line, up to and including its LF, to buf, failing with
// ErrHeadTooLarge once buf would pass limit bytes. It returns
// io.EOF, with what arrived appended, when the input ends before the LF.
func readLine(br *bufio.Reader, buf []byte, limit int) ([]byte, error) {
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > limit {
			return buf, ErrHeadTooLarge
		}
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, err
	}
}

// trimEOL drops a trailing LF or CRLF.
func trimEOL(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}

// eolOf returns the line end of line: CRLF, a bare LF, or nothing.
func eolOf(line []byte) []byte {
	if bytes.HasSuffix(line, []byte("\r\n")) {
		return line[len(line)-2:]
	}
	if bytes.HasSuffix(line, []byte("\n")) {
		return line[len(line)-1:]
	}
	return nil
}

func parseRequestLine(line []byte) (method, target, proto string, ok bool) {
	m, rest, ok1 := bytes.Cut(line, []byte(" "))
	t, p, ok2 := bytes.Cut(rest, []byte(" "))
	if !ok1 || !ok2 || len(m) == 0 || len(t) == 0 || !isToken(m) || !validProto(p) {
		return "", "", "", false
	}
	for _, c := range t {
		if c <= ' ' || c == 0x7f {
			return "", "", "", false
		}
	}
	return string(m), string(t), string(p), true
}

func parseStatusLine(line []byte) (proto string, code int, reason string, ok bool) {
	p, rest, _ := bytes.Cut(line, []byte(" "))
	c, r, _ := bytes.Cut(rest, []byte(" "))
	if !validProto(p) || len(c) != 3 {
		return "", 0, "", false
	}
	n, err := strconv.Atoi(string(c))
	if err != nil || n < 100 {
		return "", 0, "", false
	}
	for _, b := range r {
		if b == '\r' || b == 0 {
			return "", 0, "", false
		}
	}
	return string(p), n, string(r), true
}

// otherMajorVersion reports whether a request line is well formed apart from
// a version of the form HTTP/DIGIT.DIGIT (RFC 9112 §2.3) whose major version
// is not 1. The HTTP/2 connection preface is not: it is built to fail as an
// HTTP/1.1 request (RFC 9113 §3.4).
func otherMajorVersion(line []byte) bool {
	if string(line) == "PRI * HTTP/2.0" {
		return false
	}
	i := bytes.LastIndexByte(line, ' ')
	if i < 0 {
		return false
	}
	p := line[i+1:]
	if len(p) != len("HTTP/1.1") || string(p[:5]) != "HTTP/" || p[6] != '.' ||
		p[5] < '0' || p[5] > '9' || p[7] < '0' || p[7] > '9' || p[5] == '1' {
		return false
	}
	head := append(append([]byte(nil), line[:i+1]...), "HTTP/1.1"...)
	_, _, _, ok := parseRequestLine(head)
	return ok
}

// validProto accepts HTTP/1.0, HTTP/1.1 and any later HTTP/1 minor version,
// which a recipient processes as the highest minor version it implements
// (RFC 9112 §2.3), so this codec frames it as HTTP/1.1.
func validProto(p []byte) bool {
	return len(p) == len("HTTP/1.1") && string(p[:len("HTTP/1.")]) == "HTTP/1." && '0' <= p[7] && p[7] <= '9'
}

// isToken reports whether b is a non-empty RFC 9110 token.
func isToken(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if !tokenChar[c] {
			return false
		}
	}
	return true
}

var tokenChar = func() [256]bool {
	var t [256]bool
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		t[c] = true
		t[c-'a'+'A'] = true
	}
	for _, c := range "!#$%&'*+-.^_`|~" {
		t[c] = true
	}
	return t
}()
