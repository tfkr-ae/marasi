package h1_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// readAll takes everything a BodyReader gives, a piece at a time, and
// returns it with the error that ended it.
func readAll(r *h1.BodyReader) (string, error) {
	var got []byte
	for {
		data, err := r.Peek()
		if len(data) > 0 {
			got = append(got, data...)
			r.Discard(len(data))
			continue
		}
		return string(got), err
	}
}

func TestBodyReader(t *testing.T) {
	t.Run("should give each chunk's data as it arrives, before the body ends", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		r := h1.NewBodyReader(bufio.NewReader(server), h1.Framing{Kind: h1.Chunked})
		go io.WriteString(client, "5;ext\r\nhello\r\n")

		got := make(chan string, 1)
		go func() {
			data, _ := r.Peek()
			got <- string(data)
		}()
		select {
		case data := <-got:
			if data != "hello" {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", "hello", data)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("wanted the first chunk's data while the body was still open")
		}
	})

	cases := []struct {
		name    string
		framing h1.Framing
		input   string
		want    string
		err     error
	}{
		{"should end a chunked body at its last chunk", h1.Framing{Kind: h1.Chunked}, "3\r\nabc\r\n2\r\nde\r\n0\r\n\r\nNEXT", "abcde", io.EOF},
		{"should end a Content-Length body at its length", h1.Framing{Kind: h1.LengthFramed, Length: 4}, "abcdNEXT", "abcd", io.EOF},
		{"should end a body read until close at the close", h1.Framing{Kind: h1.UntilClose}, "abcdef", "abcdef", io.EOF},
		{"should report a chunked body cut inside a chunk as cut short", h1.Framing{Kind: h1.Chunked}, "5\r\nhel", "hel", h1.ErrBodyCutShort},
		{"should report a chunked body cut before its last chunk as cut short", h1.Framing{Kind: h1.Chunked}, "3\r\nabc\r\n", "abc", h1.ErrBodyCutShort},
		{"should report a Content-Length body cut short", h1.Framing{Kind: h1.LengthFramed, Length: 9}, "abc", "abc", h1.ErrBodyCutShort},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(c.input))
			got, err := readAll(h1.NewBodyReader(br, c.framing))
			if got != c.want || !errors.Is(err, c.err) {
				t.Fatalf("\nwanted:\n%q, %v\ngot:\n%q, %v", c.want, c.err, got, err)
			}
		})
	}

	t.Run("should leave the next message unread after the body", func(t *testing.T) {
		br := bufio.NewReader(strings.NewReader("3\r\nabc\r\n0\r\nX-Sum: 1\r\n\r\nGET / HTTP/1.1\r\n\r\n"))
		readAll(h1.NewBodyReader(br, h1.Framing{Kind: h1.Chunked}))
		rest, _ := io.ReadAll(br)
		if string(rest) != "GET / HTTP/1.1\r\n\r\n" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "GET / HTTP/1.1\r\n\r\n", rest)
		}
	})

	t.Run("should keep the trailer section as written", func(t *testing.T) {
		r := h1.NewBodyReader(bufio.NewReader(strings.NewReader("3\r\nabc\r\n0\r\nGrpc-Status: 0\r\nx-more: a\r\n\r\n")), h1.Framing{Kind: h1.Chunked})
		readAll(r)
		trailer := r.Trailer()
		if len(trailer.Fields) != 2 || trailer.Fields[0].Name() != "Grpc-Status" || trailer.Get("grpc-status") != "0" || trailer.Get("x-more") != "a" {
			t.Fatalf("\nwanted:\nGrpc-Status: 0, x-more: a\ngot:\n%q", trailer.Fields)
		}
	})

	t.Run("should report a malformed chunk size as a framing error", func(t *testing.T) {
		_, err := readAll(h1.NewBodyReader(bufio.NewReader(strings.NewReader("zz\r\nabc\r\n")), h1.Framing{Kind: h1.Chunked}))
		var framingErr *h1.FramingError
		if !errors.As(err, &framingErr) {
			t.Fatalf("\nwanted:\na *FramingError\ngot:\n%v", err)
		}
	})
}
