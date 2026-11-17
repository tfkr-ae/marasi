package h1_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/tfkr-ae/marasi/internal/h1"
)

func TestBodyCutShort(t *testing.T) {
	cases := []struct {
		name    string
		head    string
		arrived string
		payload string
	}{
		{"should keep what arrived of a Content-Length body", "POST / HTTP/1.1\r\nContent-Length: 10\r\n\r\n", "hello", "hello"},
		{"should report an empty Content-Length body", "POST / HTTP/1.1\r\nContent-Length: 10\r\n\r\n", "", ""},
		{"should keep a chunk cut inside its data", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", "3\r\nabc\r\n9;x\r\nhel", "abchel"},
		{"should keep a chunk cut inside its size line", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", "3\r\nabc\r\n1", "abc"},
		{"should keep a chunk cut inside its line end", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", "3\r\nabc\r", "abc"},
		{"should keep a body cut before its trailers", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", "3\r\nabc\r\n0\r\n", "abc"},
		{"should keep a body cut inside a trailer line", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", "3\r\nabc\r\n0\r\nX-Sum: 1\r\nX-Ot", "abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, err := readRequest(t, c.head+c.arrived)
			if !errors.Is(err, h1.ErrBodyCutShort) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", h1.ErrBodyCutShort, err)
			}
			if got := string(body.Payload()); got != c.payload {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.payload, got)
			}
			var out bytes.Buffer
			if _, err := body.WriteTo(&out); err != nil {
				t.Fatalf("writing body: %v", err)
			}
			if out.String() != c.arrived {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.arrived, out.String())
			}
		})
	}
}

func TestBodyPayload(t *testing.T) {
	t.Run("should decode chunked coding and keep trailers apart", func(t *testing.T) {
		body, err := readRequest(t, "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5;a=b\r\nhello\r\n1\r\n \r\n5\r\nworld\r\n0\r\nX-Sum: 9\r\n\r\n")
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		if got := string(body.Payload()); got != "hello world" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "hello world", got)
		}
		if got := body.Trailer.Get("x-sum"); got != "9" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "9", got)
		}
		if got := body.Chunks[0].Ext(); got != ";a=b" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", ";a=b", got)
		}
	})
	t.Run("should leave the next request unread", func(t *testing.T) {
		br := reader("POST / HTTP/1.1\r\nContent-Length: 2\r\n\r\nhiGET /next HTTP/1.1\r\n\r\n")
		head, _ := h1.ReadRequestHead(br)
		f, _ := h1.RequestFraming(head)
		if _, err := h1.ReadBody(br, f); err != nil {
			t.Fatalf("reading body: %v", err)
		}
		next, err := h1.ReadRequestHead(br)
		if err != nil || next.Target != "/next" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%v %v", "/next", next, err)
		}
		if _, err := h1.ReadRequestHead(br); err != io.EOF {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", io.EOF, err)
		}
	})
}

func TestHeadCutShort(t *testing.T) {
	t.Run("should report a head cut short as an unexpected EOF", func(t *testing.T) {
		_, err := h1.ReadResponseHead(reader("HTTP/1.1 200 OK\r\nX: 1\r\n"))
		if err != io.ErrUnexpectedEOF {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", io.ErrUnexpectedEOF, err)
		}
	})
	t.Run("should refuse a head past the limit", func(t *testing.T) {
		raw := "GET / HTTP/1.1\r\nX: " + string(bytes.Repeat([]byte("a"), h1.MaxHeadBytes)) + "\r\n\r\n"
		_, err := h1.ReadRequestHead(reader(raw))
		if !errors.Is(err, h1.ErrHeadTooLarge) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", h1.ErrHeadTooLarge, err)
		}
	})
}
