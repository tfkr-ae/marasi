package h1_test

import (
	"errors"
	"testing"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// readRequest reads a request head and body, returning the first error.
func readRequest(t *testing.T, raw string) (*h1.Body, error) {
	t.Helper()
	br := reader(raw)
	head, err := h1.ReadRequestHead(br)
	if err != nil {
		t.Fatalf("reading request head: %v", err)
	}
	framing, err := h1.RequestFraming(head)
	if err != nil {
		return nil, err
	}
	return h1.ReadBody(br, framing)
}

func TestAmbiguousRequestFraming(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want h1.FramingCause
	}{
		{"should refuse Content-Length with Transfer-Encoding", "POST / HTTP/1.1\r\nContent-Length: 3\r\nTransfer-Encoding: chunked\r\n\r\n", h1.CauseLengthAndTransferEncoding},
		{"should refuse differing Content-Length lines", "POST / HTTP/1.1\r\nContent-Length: 3\r\nContent-Length: 4\r\n\r\n", h1.CauseDifferingLengths},
		{"should refuse a differing Content-Length list", "POST / HTTP/1.1\r\nContent-Length: 3, 4\r\n\r\n", h1.CauseDifferingLengths},
		{"should refuse a signed Content-Length", "POST / HTTP/1.1\r\nContent-Length: +3\r\n\r\n", h1.CauseBadLength},
		{"should refuse an empty Content-Length", "POST / HTTP/1.1\r\nContent-Length: \r\n\r\n", h1.CauseBadLength},
		{"should refuse an overflowing Content-Length", "POST / HTTP/1.1\r\nContent-Length: 99999999999999999999\r\n\r\n", h1.CauseBadLength},
		{"should refuse a request coding that does not end in chunked", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked, gzip\r\n\r\n", h1.CauseChunkedNotFinal},
		{"should refuse an unknown request coding", "POST / HTTP/1.1\r\nTransfer-Encoding: xchunked\r\n\r\n", h1.CauseChunkedNotFinal},
		{"should refuse chunked twice", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n", h1.CauseChunkedTwice},
		{"should refuse a chunk size that is not hex", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n", h1.CauseBadChunkSize},
		{"should refuse a negative chunk size", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n-1\r\n", h1.CauseBadChunkSize},
		{"should refuse an overflowing chunk size", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n1ffffffffffffffff\r\n", h1.CauseBadChunkSize},
		{"should refuse a chunk size with junk after it", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5 x\r\nhello\r\n", h1.CauseBadChunkSize},
		{"should refuse an empty chunk size", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n\r\n", h1.CauseBadChunkSize},
		{"should refuse chunk data longer than its size", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nhello\r\n0\r\n\r\n", h1.CauseBadChunkEnd},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := readRequest(t, c.raw)
			var fe *h1.FramingError
			if !errors.As(err, &fe) {
				t.Fatalf("\nwanted:\n*h1.FramingError\ngot:\n%v", err)
			}
			if fe.Cause != c.want {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", c.want, fe.Cause)
			}
		})
	}
}

func TestFramingErrorNamesTheCause(t *testing.T) {
	t.Run("should name the cause in the message", func(t *testing.T) {
		_, err := readRequest(t, "POST / HTTP/1.1\r\nContent-Length: 3\r\nContent-Length: 4\r\n\r\n")
		want := "ambiguous framing: differing Content-Length values: 3, 4"
		if err == nil || err.Error() != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, err)
		}
	})
}

func TestResponseFramingWithBothLengths(t *testing.T) {
	t.Run("should report the ambiguity and offer chunked framing", func(t *testing.T) {
		head, err := h1.ReadResponseHead(reader("HTTP/1.1 200 OK\r\nContent-Length: 3\r\nTransfer-Encoding: chunked\r\n\r\n"))
		if err != nil {
			t.Fatalf("reading head: %v", err)
		}
		f, err := h1.ResponseFraming(head, "GET")
		var fe *h1.FramingError
		if !errors.As(err, &fe) || fe.Cause != h1.CauseLengthAndTransferEncoding {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", h1.CauseLengthAndTransferEncoding, err)
		}
		if f.Kind != h1.Chunked {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", h1.Chunked, f.Kind)
		}
	})
}

func TestMalformedHead(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"should refuse whitespace before the colon", "GET / HTTP/1.1\r\nContent-Length : 3\r\n\r\n"},
		{"should refuse a field line without a colon", "GET / HTTP/1.1\r\nHost\r\n\r\n"},
		{"should refuse a fold before any field", "GET / HTTP/1.1\r\n X: 1\r\n\r\n"},
		{"should refuse a bare CR in a value", "GET / HTTP/1.1\r\nX: a\rb\r\n\r\n"},
		{"should refuse a NUL in a value", "GET / HTTP/1.1\r\nX: a\x00b\r\n\r\n"},
		{"should refuse an unknown version", "GET / HTTP/2.0\r\n\r\n"},
		{"should refuse a request line with extra spaces", "GET  / HTTP/1.1\r\n\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := h1.ReadRequestHead(reader(c.raw))
			if !errors.Is(err, h1.ErrMalformedHead) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", h1.ErrMalformedHead, err)
			}
		})
	}
}
