package h1

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Field is one header field as it was on the wire: its name, colon, value and
// line end, plus any obsolete folded continuation lines (RFC 9112 §5.2).
type Field struct {
	// Raw is the field's bytes, line ends included.
	Raw []byte
}

// Name returns the field name as spelled on the wire.
func (f Field) Name() string {
	return string(f.name())
}

// name is the field name as spelled on the wire, sharing Raw's bytes.
func (f Field) name() []byte {
	name, _, _ := bytes.Cut(f.Raw, []byte(":"))
	return name
}

// named reports whether the field's name is name, compared without regard
// to case.
func (f Field) named(name string) bool {
	return equalFold(f.name(), name)
}

// equalFold reports whether b and s are the same ASCII text, compared
// without regard to case. Field names are tokens, which are ASCII.
func equalFold(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range len(b) {
		if lowerASCII(b[i]) != lowerASCII(s[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// Value returns the field value without surrounding whitespace. Each obsolete
// line fold becomes one space, as RFC 9112 §5.2 asks of a recipient.
func (f Field) Value() string {
	_, rest, _ := bytes.Cut(f.Raw, []byte(":"))
	if line, next, _ := bytes.Cut(rest, []byte("\n")); len(next) == 0 {
		// One line, the usual case: no fold to join.
		return string(trimOWS(trimEOL(line)))
	}
	var parts []string
	for len(rest) > 0 {
		line, next, _ := bytes.Cut(rest, []byte("\n"))
		rest = next
		if part := trimOWS(trimEOL(line)); len(part) > 0 {
			parts = append(parts, string(part))
		}
	}
	return strings.Join(parts, " ")
}

// Header is a header or trailer section: its field lines in wire order and
// the empty line that ends it.
type Header struct {
	Fields []Field
	// End is the empty line that ends the section: CRLF, a bare LF, or, for a
	// section cut short, nothing.
	End []byte
}

// Values returns the values of every field named name, compared without
// regard to case, in wire order.
func (h *Header) Values(name string) []string {
	var vs []string
	for _, f := range h.Fields {
		if f.named(name) {
			vs = append(vs, f.Value())
		}
	}
	return vs
}

// Get returns the value of the first field named name, or "".
func (h *Header) Get(name string) string {
	for _, f := range h.Fields {
		if f.named(name) {
			return f.Value()
		}
	}
	return ""
}

// Has reports whether a field named name is present.
func (h *Header) Has(name string) bool {
	for _, f := range h.Fields {
		if f.named(name) {
			return true
		}
	}
	return false
}

// eol returns the line end new lines in this section use: a bare LF when the
// section ends with one, CRLF otherwise.
func (h *Header) eol() string {
	if string(h.End) == "\n" {
		return "\n"
	}
	return "\r\n"
}

func (h *Header) size() int {
	n := len(h.End)
	for _, f := range h.Fields {
		n += len(f.Raw)
	}
	return n
}

func (h *Header) appendTo(b []byte) []byte {
	for _, f := range h.Fields {
		b = append(b, f.Raw...)
	}
	return append(b, h.End...)
}

// WriteTo writes the section's field lines and its ending empty line.
func (h *Header) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(h.appendTo(make([]byte, 0, h.size())))
	return int64(n), err
}

// fieldSpans records where each field and the end line sit in a buffer that
// is still growing, so they can be sliced once it stops moving.
type fieldSpans struct {
	starts []int
	end    int // start of the ending empty line
	stop   int // end of the section
}

func (s fieldSpans) header(buf []byte) Header {
	var h Header
	if len(s.starts) > 0 {
		h.Fields = make([]Field, len(s.starts))
		for i, start := range s.starts {
			stop := s.end
			if i+1 < len(s.starts) {
				stop = s.starts[i+1]
			}
			h.Fields[i] = Field{Raw: buf[start:stop:stop]}
		}
	}
	if s.stop > s.end {
		h.End = buf[s.end:s.stop:s.stop]
	}
	return h
}

// readFields appends field lines to buf up to and including the empty line
// that ends the section.
func readFields(br *bufio.Reader, buf []byte, limit int) ([]byte, fieldSpans, error) {
	var s fieldSpans
	for {
		start := len(buf)
		var err error
		buf, err = readLine(br, buf, limit)
		if err != nil {
			s.end, s.stop = start, start
			if err == io.EOF {
				// What arrived of the cut-short line stays out of the spans;
				// callers that keep partial input read it from buf.
				return buf, s, io.ErrUnexpectedEOF
			}
			return buf, s, err
		}
		line := buf[start:]
		content := trimEOL(line)
		if len(content) == 0 {
			s.end, s.stop = start, len(buf)
			return buf, s, nil
		}
		if content[0] == ' ' || content[0] == '\t' {
			// An obsolete line fold continues the field above it.
			if len(s.starts) == 0 {
				return buf, s, fmt.Errorf("parsing field line %q: continuation without a field: %w", content, ErrMalformedHead)
			}
			if !validValue(content) {
				return buf, s, fmt.Errorf("parsing field line %q: %w", content, ErrMalformedHead)
			}
			continue
		}
		name, value, ok := bytes.Cut(content, []byte(":"))
		if !ok || !isToken(name) || !validValue(value) {
			return buf, s, fmt.Errorf("parsing field line %q: %w", content, ErrMalformedHead)
		}
		s.starts = append(s.starts, start)
	}
}

// validValue rejects the bytes RFC 9110 §5.5 forbids in a field value: CR,
// LF and NUL. Other odd bytes are carried as written.
func validValue(v []byte) bool {
	return !bytes.ContainsAny(v, "\r\n\x00")
}

func trimOWS(b []byte) []byte {
	return bytes.Trim(b, " \t")
}
