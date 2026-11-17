package h1_test

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/internal/h1"
)

func reader(s string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(s))
}

// roundTripRequest reads one request, head and body, and writes it back.
func roundTripRequest(t *testing.T, raw string) string {
	t.Helper()
	br := reader(raw)
	head, err := h1.ReadRequestHead(br)
	if err != nil {
		t.Fatalf("reading request head: %v", err)
	}
	framing, err := h1.RequestFraming(head)
	if err != nil {
		t.Fatalf("framing request: %v", err)
	}
	body, err := h1.ReadBody(br, framing)
	if err != nil {
		t.Fatalf("reading request body: %v", err)
	}
	var out bytes.Buffer
	if _, err := head.WriteTo(&out); err != nil {
		t.Fatalf("writing head: %v", err)
	}
	if _, err := body.WriteTo(&out); err != nil {
		t.Fatalf("writing body: %v", err)
	}
	return out.String()
}

func TestRequestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"should keep a plain GET", "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"},
		{"should keep odd name case and value whitespace", "GET / HTTP/1.1\r\nhOsT:example.com  \r\nX-odd:\t a  b \t\r\ncontent-TYPE:x\r\n\r\n"},
		{"should keep repeated names in order", "GET / HTTP/1.1\r\nCookie: a=1\r\nAccept: */*\r\ncookie: b=2\r\nCOOKIE:c=3\r\n\r\n"},
		{"should keep obsolete line folding", "GET / HTTP/1.1\r\nX-Fold: one\r\n  two\r\n\tthree\r\nHost: h\r\n\r\n"},
		{"should keep bare LF line ends", "GET / HTTP/1.1\nHost: h\nX-A: 1\r\n\n"},
		{"should keep empty lines before the request line", "\r\n\r\nGET / HTTP/1.1\r\nHost: h\r\n\r\n"},
		{"should keep an empty value", "GET / HTTP/1.1\r\nX-Empty:\r\nX-Space: \r\n\r\n"},
		{"should keep a Content-Length body", "POST /p HTTP/1.1\r\nContent-length: 5\r\n\r\nhello"},
		{"should keep a repeated equal Content-Length", "POST /p HTTP/1.1\r\nContent-Length: 5\r\nContent-Length: 5, 5\r\n\r\nhello"},
		{"should keep a zero Content-Length", "POST /p HTTP/1.1\r\nContent-Length: 0\r\n\r\n"},
		{"should keep a chunked body", "POST /p HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n"},
		{"should keep chunk size case and leading zeros", "POST /p HTTP/1.1\r\nTransfer-Encoding: Chunked\r\n\r\n00A\r\n0123456789\r\na\r\n0123456789\r\n000\r\n\r\n"},
		{"should keep chunk extensions", "POST /p HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5;name=\"v a\"\r\nhello\r\n3 ; x ;y=z\r\nabc\r\n0;last\r\n\r\n"},
		{"should keep trailers", "POST /p HTTP/1.1\r\nTransfer-Encoding: chunked\r\nTrailer: x-sum\r\n\r\n2\r\nhi\r\n0\r\nx-SUM:  abc \r\nOther: 1\r\n\r\n"},
		{"should keep bare LF chunk framing", "POST /p HTTP/1.1\nTransfer-Encoding: chunked\n\n2\nhi\n0\n\n"},
		{"should keep a coding before chunked", "POST /p HTTP/1.1\r\nTransfer-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nz\r\n0\r\n\r\n"},
		{"should keep an absolute-form target", "GET http://example.com:8080/a?b=c#d HTTP/1.1\r\nHost: example.com:8080\r\n\r\n"},
		{"should keep an authority-form CONNECT", "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"},
		{"should keep HTTP/1.0", "GET / HTTP/1.0\r\n\r\n"},
		{"should keep obs-text bytes in a value", "GET / HTTP/1.1\r\nX-Bin: \xff\xfe caf\xc3\xa9\r\n\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := roundTripRequest(t, c.raw)
			if got != c.raw {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.raw, got)
			}
		})
	}
}

// roundTripResponses reads responses to method until a final one, writing
// back each head and the final body.
func roundTripResponses(t *testing.T, raw, method string) string {
	t.Helper()
	br := reader(raw)
	var out bytes.Buffer
	for {
		head, err := h1.ReadResponseHead(br)
		if err != nil {
			t.Fatalf("reading response head: %v", err)
		}
		if _, err := head.WriteTo(&out); err != nil {
			t.Fatalf("writing head: %v", err)
		}
		if head.Interim() {
			continue
		}
		framing, err := h1.ResponseFraming(head, method)
		if err != nil {
			t.Fatalf("framing response: %v", err)
		}
		body, err := h1.ReadBody(br, framing)
		if err != nil {
			t.Fatalf("reading response body: %v", err)
		}
		if _, err := body.WriteTo(&out); err != nil {
			t.Fatalf("writing body: %v", err)
		}
		return out.String()
	}
}

func TestResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name, method, raw string
	}{
		{"should keep a Content-Length response", "GET", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nSERVER:  x \r\n\r\nhi"},
		{"should keep a status line without a reason", "GET", "HTTP/1.1 204\r\nDate: x\r\n\r\n"},
		{"should keep a status line with an empty reason", "GET", "HTTP/1.1 204 \r\n\r\n"},
		{"should keep an odd reason", "GET", "HTTP/1.1 200 Very  Fine\tIndeed\r\nContent-Length: 0\r\n\r\n"},
		{"should keep a body read until close", "GET", "HTTP/1.1 200 OK\r\nX: y\r\n\r\nrest of\r\nthe stream"},
		{"should keep a body read until close under a non-chunked coding", "GET", "HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip\r\n\r\n\x1f\x8b raw"},
		{"should keep a chunked response with trailers", "GET", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n3;e\r\nabc\r\n0\r\nGrpc-Status: 0\r\n\r\n"},
		{"should keep 1xx interim responses before the final one", "GET", "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 103 Early Hints\r\nLink: </s.css>; rel=preload\r\n\r\nHTTP/1.1 102 Processing\r\n\r\nHTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\nx"},
		{"should read no body after a HEAD request", "HEAD", "HTTP/1.1 200 OK\r\nContent-Length: 50\r\n\r\n"},
		{"should read no body for a 304", "GET", "HTTP/1.1 304 Not Modified\r\nContent-Length: 50\r\n\r\n"},
		{"should read no body for a 2xx to CONNECT", "CONNECT", "HTTP/1.1 200 Connection Established\r\n\r\n"},
		{"should keep a 101 as final", "GET", "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := roundTripResponses(t, c.raw, c.method)
			if got != c.raw {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.raw, got)
			}
		})
	}
}

func bufioReader(b []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(b))
}
