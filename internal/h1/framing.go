package h1

import (
	"fmt"
	"net/http"
	"strings"
)

// FramingKind is how a message body's end is found (RFC 9112 §6.3).
type FramingKind int

const (
	// NoBody: the message has no body, by its method, status or headers.
	NoBody FramingKind = iota
	// LengthFramed: the body is Content-Length bytes long.
	LengthFramed
	// Chunked: the body uses the chunked transfer coding.
	Chunked
	// UntilClose: the body runs until the connection closes. Only responses
	// are framed this way.
	UntilClose
)

// Framing says how to read a message's body.
type Framing struct {
	Kind FramingKind
	// Length is the Content-Length when Kind is LengthFramed.
	Length int64
}

// FramingCause names why a message's framing is ambiguous.
type FramingCause string

const (
	// CauseLengthAndTransferEncoding: both Content-Length and
	// Transfer-Encoding are present.
	CauseLengthAndTransferEncoding FramingCause = "both Content-Length and Transfer-Encoding"
	// CauseDifferingLengths: Content-Length values that differ.
	CauseDifferingLengths FramingCause = "differing Content-Length values"
	// CauseBadLength: a Content-Length value that is not a decimal length.
	CauseBadLength FramingCause = "invalid Content-Length"
	// CauseChunkedNotFinal: a request whose Transfer-Encoding does not end in
	// chunked, so it has no length (RFC 9112 §6.3 rule 4).
	CauseChunkedNotFinal FramingCause = "Transfer-Encoding does not end in chunked"
	// CauseChunkedTwice: chunked applied more than once (RFC 9112 §6.1).
	CauseChunkedTwice FramingCause = "chunked applied more than once"
	// CauseBadChunkSize: a chunk size line that is not hexadecimal digits
	// followed by optional extensions.
	CauseBadChunkSize FramingCause = "invalid chunk size"
	// CauseBadChunkEnd: chunk data not followed by a line end.
	CauseBadChunkEnd FramingCause = "chunk data not followed by a line end"
)

// FramingError reports a message whose body length RFC 9112 §6.3 does not
// settle, the way a request can smuggle another past a proxy.
type FramingError struct {
	Cause FramingCause
	// Detail quotes the offending input.
	Detail string
}

func (e *FramingError) Error() string {
	if e.Detail == "" {
		return "ambiguous framing: " + string(e.Cause)
	}
	return fmt.Sprintf("ambiguous framing: %s: %s", e.Cause, e.Detail)
}

// RequestFraming finds how a request's body is framed. When both
// Content-Length and Transfer-Encoding are present it returns a
// *FramingError along with the chunked framing RFC 9112 §6.3 would apply, so
// a caller that chooses to be lenient can still read the body.
func RequestFraming(h *RequestHead) (Framing, error) {
	f, err := framing(&h.Header, true)
	if err != nil {
		return f, err
	}
	if f.Kind == UntilClose {
		// Only responses run until close; a request without a length has
		// no body.
		f.Kind = NoBody
	}
	return f, nil
}

// ResponseFraming finds how a response's body is framed. method is the
// request's method, since a response to HEAD has no body and a 2xx response
// to CONNECT starts a tunnel. Errors are as for RequestFraming.
func ResponseFraming(h *ResponseHead, method string) (Framing, error) {
	switch {
	case h.StatusCode < 200, h.StatusCode == 204, h.StatusCode == 304,
		method == http.MethodHead,
		method == http.MethodConnect && h.StatusCode < 300:
		return Framing{Kind: NoBody}, nil
	}
	return framing(&h.Header, false)
}

func framing(h *Header, request bool) (Framing, error) {
	codings := h.Values("Transfer-Encoding")
	lengths := h.Values("Content-Length")
	if len(codings) > 0 {
		f, err := transferFraming(codings, request)
		if err != nil {
			return f, err
		}
		if len(lengths) > 0 {
			return f, &FramingError{Cause: CauseLengthAndTransferEncoding}
		}
		return f, nil
	}
	if len(lengths) > 0 {
		n, err := contentLength(lengths)
		if err != nil {
			return Framing{}, err
		}
		return Framing{Kind: LengthFramed, Length: n}, nil
	}
	return Framing{Kind: UntilClose}, nil
}

func transferFraming(values []string, request bool) (Framing, error) {
	var codings []string
	for _, v := range values {
		for _, c := range strings.Split(v, ",") {
			if c = strings.TrimSpace(c); c != "" {
				codings = append(codings, c)
			}
		}
	}
	chunked := 0
	for _, c := range codings {
		if strings.EqualFold(c, "chunked") {
			chunked++
		}
	}
	switch {
	case chunked > 1:
		return Framing{}, &FramingError{Cause: CauseChunkedTwice, Detail: strings.Join(values, ", ")}
	case len(codings) > 0 && chunked == 1 && strings.EqualFold(codings[len(codings)-1], "chunked"):
		return Framing{Kind: Chunked}, nil
	case request:
		return Framing{}, &FramingError{Cause: CauseChunkedNotFinal, Detail: strings.Join(values, ", ")}
	default:
		return Framing{Kind: UntilClose}, nil
	}
}

// contentLength accepts one length, or a list of the same length repeated,
// which RFC 9110 §8.6 lets a recipient take as that length.
func contentLength(values []string) (int64, error) {
	n := int64(-1)
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			m, ok := parseLength(strings.TrimSpace(part))
			if !ok {
				return 0, &FramingError{Cause: CauseBadLength, Detail: fmt.Sprintf("%q", v)}
			}
			if n >= 0 && m != n {
				return 0, &FramingError{Cause: CauseDifferingLengths, Detail: strings.Join(values, ", ")}
			}
			n = m
		}
	}
	return n, nil
}

// maxLengthDigits keeps a parsed length inside int64.
const maxLengthDigits = 18

func parseLength(s string) (int64, bool) {
	if s == "" || len(s) > maxLengthDigits {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}
