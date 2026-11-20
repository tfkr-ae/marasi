package stream_test

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/tfkr-ae/marasi/stream"
)

func TestFramingOf(t *testing.T) {
	cases := []struct {
		contentType string
		response    bool
		want        stream.Framing
	}{
		{"application/grpc", false, stream.FramingGRPC},
		{"application/grpc+proto", true, stream.FramingGRPC},
		{"Application/GRPC; charset=utf-8", false, stream.FramingGRPC},
		{"application/grpc-web", false, stream.FramingGRPCWeb},
		{"application/grpc-web+proto", true, stream.FramingGRPCWeb},
		{"application/grpc-web-text", true, stream.FramingGRPCWeb},
		{"application/connect+proto", false, stream.FramingConnect},
		{"text/event-stream", true, stream.FramingSSE},
		{"text/event-stream", false, stream.FramingNone},
		{"application/x-ndjson", true, stream.FramingNDJSON},
		{"application/jsonl", true, stream.FramingNDJSON},
		{"application/jsonl", false, stream.FramingNone},
		{"application/proto", false, stream.FramingNone},
		{"application/json", true, stream.FramingNone},
		{"application/grpcx", false, stream.FramingNone},
		{"", false, stream.FramingNone},
	}
	for _, c := range cases {
		if got := stream.FramingOf(c.contentType, c.response); got != c.want {
			t.Errorf("FramingOf(%q, response %v)\nwanted:\n%q\ngot:\n%q", c.contentType, c.response, c.want, got)
		}
	}
}

// envelope is payload in a 5-byte envelope with flags.
func envelope(flags byte, payload string) string {
	var prefix [5]byte
	prefix[0] = flags
	binary.BigEndian.PutUint32(prefix[1:], uint32(len(payload)))
	return string(prefix[:]) + payload
}

// split feeds body to a splitter in pieces of size bytes (all at once when
// size is 0) and returns the messages it cut, and the error it stopped on.
func split(t *testing.T, framing stream.Framing, limit int64, body string, size int) ([]stream.Message, *stream.Splitter, error) {
	t.Helper()
	s, err := stream.NewSplitter(framing, limit)
	if err != nil {
		t.Fatalf("NewSplitter(%q): %v", framing, err)
	}
	var messages []stream.Message
	data := []byte(body)
	for len(data) > 0 {
		piece := data
		if size > 0 && len(piece) > size {
			piece = piece[:size]
		}
		for len(piece) > 0 {
			n, message, err := s.Feed(piece)
			piece, data = piece[n:], data[n:]
			if err != nil {
				return messages, s, err
			}
			if message != nil {
				messages = append(messages, *message)
			}
		}
	}
	return messages, s, nil
}

func TestSplitter(t *testing.T) {
	body := envelope(0, "ping 1") + envelope(1, "") + envelope(0x80, "grpc-status: 0\r\n")
	want := []stream.Message{
		{Raw: []byte(envelope(0, "ping 1")), Flags: 0, Payload: []byte("ping 1")},
		{Raw: []byte(envelope(1, "")), Flags: 1, Payload: []byte{}},
		{Raw: []byte(envelope(0x80, "grpc-status: 0\r\n")), Flags: 0x80, Payload: []byte("grpc-status: 0\r\n")},
	}
	for _, size := range []int{0, 1, 3} {
		t.Run("should cut envelopes whole, keeping their bytes, fed "+[]string{"in one piece", "one byte at a time", "three bytes at a time"}[min(size, 2)], func(t *testing.T) {
			messages, s, err := split(t, stream.FramingGRPC, 1<<20, body, size)
			if err != nil {
				t.Fatalf("splitting: %v", err)
			}
			if len(messages) != len(want) {
				t.Fatalf("messages\nwanted:\n%d\ngot:\n%d: %q", len(want), len(messages), messages)
			}
			for i := range want {
				if string(messages[i].Raw) != string(want[i].Raw) || messages[i].Flags != want[i].Flags || string(messages[i].Payload) != string(want[i].Payload) {
					t.Errorf("message %d\nwanted:\n%q flags %#x payload %q\ngot:\n%q flags %#x payload %q", i, want[i].Raw, want[i].Flags, want[i].Payload, messages[i].Raw, messages[i].Flags, messages[i].Payload)
				}
			}
			if partial := s.Partial(); partial != nil {
				t.Errorf("partial message after whole ones\nwanted:\nnone\ngot:\n%q", partial.Raw)
			}
		})
	}

	t.Run("should stop consuming at the end of each message, so the reader reads no further than it forwards", func(t *testing.T) {
		s, _ := stream.NewSplitter(stream.FramingGRPC, 1<<20)
		n, message, err := s.Feed([]byte(body))
		if err != nil || message == nil || n != len(envelope(0, "ping 1")) {
			t.Fatalf("Feed\nwanted:\nthe first message, %d bytes consumed\ngot:\n%v, %d bytes, %v", len(envelope(0, "ping 1")), message, n, err)
		}
	})

	t.Run("should give what arrived of a message the body ended inside as the partial message", func(t *testing.T) {
		cut := envelope(0, "ping 1") + envelope(0, "pong 2")[:7]
		messages, s, err := split(t, stream.FramingGRPC, 1<<20, cut, 2)
		if err != nil || len(messages) != 1 {
			t.Fatalf("splitting\nwanted:\none message\ngot:\n%d, %v", len(messages), err)
		}
		partial := s.Partial()
		if partial == nil || string(partial.Raw) != envelope(0, "pong 2")[:7] || string(partial.Payload) != "po" {
			t.Fatalf("partial\nwanted:\n%q payload %q\ngot:\n%+v", envelope(0, "pong 2")[:7], "po", partial)
		}
	})

	t.Run("should refuse an envelope that declares a payload over the limit, with the size it declared", func(t *testing.T) {
		messages, _, err := split(t, stream.FramingConnect, 4, envelope(0, "ok")+envelope(0, "too long"), 1)
		var tooLarge *stream.TooLargeError
		if len(messages) != 1 || !errors.As(err, &tooLarge) || tooLarge.Size != int64(len("too long")) || tooLarge.Limit != 4 {
			t.Fatalf("splitting\nwanted:\none message, then too large: size 8, limit 4\ngot:\n%d messages, %v", len(messages), err)
		}
	})

	t.Run("should refuse a framing it cannot split yet", func(t *testing.T) {
		if _, err := stream.NewSplitter(stream.FramingNone, 1); err == nil {
			t.Fatal("NewSplitter(none)\nwanted:\nan error\ngot:\nnone")
		}
	})
}
