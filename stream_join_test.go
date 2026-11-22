package marasi

import (
	"bytes"
	"testing"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

func TestWithBody(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		body string
		want string
	}{
		{
			name: "should drop an HTTP/1.1 head's Transfer-Encoding and add a Content-Length of the body",
			raw:  "POST /svc/M HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\nContent-Type: application/grpc-web+proto\r\n\r\n",
			body: "12345",
			want: "POST /svc/M HTTP/1.1\r\nHost: a\r\nContent-Type: application/grpc-web+proto\r\nContent-Length: 5\r\n\r\n12345",
		},
		{
			name: "should set an HTTP/1.1 head's Content-Length in place, keeping its name's case",
			raw:  "POST /svc/M HTTP/1.1\r\ncontent-length: 0\r\nHost: a\r\n\r\n",
			body: "abc",
			want: "POST /svc/M HTTP/1.1\r\ncontent-length: 3\r\nHost: a\r\n\r\nabc",
		},
		{
			name: "should keep an HTTP/2 head's fields and add no content-length",
			raw:  "POST /svc/M HTTP/2\r\n:authority: a\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n",
			body: "abc",
			want: "POST /svc/M HTTP/2\r\n:authority: a\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\nabc",
		},
		{
			name: "should set an HTTP/2 head's content-length to the body's length",
			raw:  "POST /svc/M HTTP/2\r\n:authority: a\r\ncontent-length: 0\r\n\r\n",
			body: "abcd",
			want: "POST /svc/M HTTP/2\r\n:authority: a\r\ncontent-length: 4\r\n\r\nabcd",
		},
		{
			name: "should replace whatever body the stored request had",
			raw:  "POST /svc/M HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
			body: "",
			want: "POST /svc/M HTTP/1.1\r\nHost: a\r\nContent-Length: 0\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(withBody([]byte(c.raw), []byte(c.body))); got != c.want {
				t.Fatalf("withBody()\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}

func TestForwardedMessage(t *testing.T) {
	flags := func(f uint8) *uint8 { return &f }
	message := func(payload string, compressed bool, f uint8, metadata map[string]any) *domain.StreamMessage {
		if metadata == nil {
			metadata = map[string]any{}
		}
		return &domain.StreamMessage{
			ID: uuid.Must(uuid.NewV7()), Direction: "request", Framing: "grpc", Flags: flags(f),
			Payload: []byte(payload), Compressed: compressed, Metadata: metadata,
		}
	}

	t.Run("should leave out a message that never reached the origin whole", func(t *testing.T) {
		for _, key := range []string{"dropped", "too_large", "cut_short", "inflate_error"} {
			if _, ok, err := forwardedMessage(message("x", false, 0, map[string]any{key: true})); ok || err != nil {
				t.Errorf("forwardedMessage() of a %s message\nwanted:\nleft out\ngot:\nkept (%v)", key, err)
			}
		}
	})

	t.Run("should put an uncompressed message, an edited or injected one too, in its envelope as stored", func(t *testing.T) {
		got, ok, err := forwardedMessage(message("edited", false, 0, map[string]any{"edited": true}))
		if err != nil || !ok || string(got) != string(stream.Envelope(0, []byte("edited"))) {
			t.Fatalf("forwardedMessage()\nwanted:\n%q\ngot:\n%q %v %v", stream.Envelope(0, []byte("edited")), got, ok, err)
		}
	})

	t.Run("should compress a message stored decompressed again with its encoding, keeping the compressed flag", func(t *testing.T) {
		got, ok, err := forwardedMessage(message("hello hello hello", true, 1, map[string]any{"encoding": "gzip"}))
		if err != nil || !ok {
			t.Fatalf("forwardedMessage() returned %v, %v", ok, err)
		}
		if got[0] != 1 {
			t.Fatalf("the envelope's flags\nwanted:\n1\ngot:\n%d", got[0])
		}
		payload, err := stream.Decompress("gzip", got[5:], 1<<20)
		if err != nil || string(payload) != "hello hello hello" {
			t.Fatalf("the joined message\nwanted:\nthe payload gzip-compressed\ngot:\n%q %v", payload, err)
		}
		if length := int(got[1])<<24 | int(got[2])<<16 | int(got[3])<<8 | int(got[4]); length != len(got)-5 {
			t.Fatalf("the envelope's length\nwanted:\n%d\ngot:\n%d", len(got)-5, length)
		}
	})

	t.Run("should keep a message stored as compressed bytes it does not decode", func(t *testing.T) {
		got, ok, err := forwardedMessage(message("\x01\x02snappy", true, 1, map[string]any{"encoding": "snappy"}))
		if err != nil || !ok || !bytes.Equal(got, stream.Envelope(1, []byte("\x01\x02snappy"))) {
			t.Fatalf("forwardedMessage()\nwanted:\nthe stored bytes in their envelope\ngot:\n%q %v %v", got, ok, err)
		}
	})
}
