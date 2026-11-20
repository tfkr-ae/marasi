package h1

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWireReader(t *testing.T) {
	const next = "NEXT"
	tests := []struct {
		name    string
		framing Framing
		input   string
		body    string
		err     error
	}{
		{"a length-framed body", Framing{Kind: LengthFramed, Length: 5}, "hello" + next, "hello", nil},
		{"an empty length-framed body", Framing{Kind: LengthFramed}, next, "", nil},
		{"a length-framed body cut short", Framing{Kind: LengthFramed, Length: 9}, "hel", "hel", ErrBodyCutShort},
		{"a body until close", Framing{Kind: UntilClose}, "all of it", "all of it", nil},
		{"a chunked body with extensions and trailers", Framing{Kind: Chunked}, "2;x=1\r\nab\r\n3\ncde\n0\r\nX-Sum: 1\r\n\r\n" + next, "2;x=1\r\nab\r\n3\ncde\n0\r\nX-Sum: 1\r\n\r\n", nil},
		{"a chunked body cut short in a chunk", Framing{Kind: Chunked}, "5\r\nab", "5\r\nab", ErrBodyCutShort},
		{"a chunked body cut short in a size line", Framing{Kind: Chunked}, "2\r\nab\r\n1", "2\r\nab\r\n1", ErrBodyCutShort},
		{"a chunked body cut short in its trailers", Framing{Kind: Chunked}, "0\r\nX-Sum: 1\r\n", "0\r\nX-Sum: 1\r\n", ErrBodyCutShort},
	}
	for _, tt := range tests {
		t.Run("should give "+tt.name+" exactly as on the wire and stop where ReadBody stops", func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(tt.input))
			got, err := io.ReadAll(iotestOneByte{NewWireReader(br, tt.framing)})
			if string(got) != tt.body || !errors.Is(err, tt.err) {
				t.Fatalf("wanted:\n%q %v\ngot:\n%q %v", tt.body, tt.err, got, err)
			}
			rest, _ := io.ReadAll(br)
			if want := strings.TrimPrefix(tt.input, tt.body); string(rest) != want {
				t.Fatalf("wanted the reader to leave:\n%q\ngot:\n%q", want, rest)
			}
			direct, directErr := ReadBody(bufio.NewReader(strings.NewReader(tt.input)), tt.framing)
			again, againErr := ReadBody(bufio.NewReader(bytes.NewReader(got)), tt.framing)
			var a, b bytes.Buffer
			direct.WriteTo(&a)
			again.WriteTo(&b)
			if a.String() != b.String() || !bytes.Equal(direct.Payload(), again.Payload()) || (directErr == nil) != (againErr == nil) {
				t.Fatalf("ReadBody of what it gave differs from ReadBody of the wire\nwanted:\n%q %v\ngot:\n%q %v", a.String(), directErr, b.String(), againErr)
			}
		})
	}

	t.Run("should refuse a malformed chunk size as ReadBody does", func(t *testing.T) {
		_, err := io.ReadAll(NewWireReader(bufio.NewReader(strings.NewReader("zz\r\n")), Framing{Kind: Chunked}))
		var framingErr *FramingError
		if !errors.As(err, &framingErr) || framingErr.Cause != CauseBadChunkSize {
			t.Fatalf("wanted a bad chunk size framing error\ngot:\n%v", err)
		}
	})
}

// iotestOneByte reads one byte at a time, as a slow peer sends.
type iotestOneByte struct{ r io.Reader }

func (o iotestOneByte) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}
