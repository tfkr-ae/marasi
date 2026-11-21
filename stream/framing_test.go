package stream_test

import (
	"encoding/binary"
	"errors"
	"strings"
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

func TestIsGRPCWebText(t *testing.T) {
	for contentType, want := range map[string]bool{
		"application/grpc-web-text":            true,
		"Application/gRPC-Web-Text+proto; x=1": true,
		"application/grpc-web":                 false,
		"application/grpc-web+proto":           false,
		"application/grpc-web-textual":         false,
	} {
		if got := stream.IsGRPCWebText(contentType); got != want {
			t.Errorf("IsGRPCWebText(%q)\nwanted:\n%v\ngot:\n%v", contentType, want, got)
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

	t.Run("should refuse an ordinary body, which has no framing to split", func(t *testing.T) {
		if _, err := stream.NewSplitter(stream.FramingNone, 1); err == nil {
			t.Fatal("NewSplitter(none)\nwanted:\nan error\ngot:\nnone")
		}
	})
}

// wantMessages checks messages are want, each by its bytes, and that no
// message is left partial.
func wantMessages(t *testing.T, messages []stream.Message, s *stream.Splitter, err error, want ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("splitting: %v", err)
	}
	got := make([]string, len(messages))
	for i, m := range messages {
		got[i] = string(m.Raw)
		if string(m.Payload) != string(m.Raw) || m.Flags != 0 {
			t.Errorf("message %d\nwanted:\nits payload as its bytes, no flags\ngot:\npayload %q flags %#x", i, m.Payload, m.Flags)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
		t.Fatalf("messages\nwanted:\n%q\ngot:\n%q", want, got)
	}
	if partial := s.Partial(); partial != nil {
		t.Errorf("partial message after whole ones\nwanted:\nnone\ngot:\n%q", partial.Raw)
	}
}

// pieces names the sizes split feeds a body in.
var pieces = map[int]string{0: "in one piece", 1: "one byte at a time", 3: "three bytes at a time"}

func TestSplitter_EnvelopeFramings(t *testing.T) {
	t.Run("should cut a gRPC-Web body's trailers frame as a stream message whose payload is the trailer block", func(t *testing.T) {
		trailers := "grpc-status: 0\r\ngrpc-message: \r\n"
		for _, size := range []int{0, 1} {
			messages, _, err := split(t, stream.FramingGRPCWeb, 1<<20, envelope(0, "hi")+envelope(0x80, trailers), size)
			if err != nil || len(messages) != 2 || messages[1].Flags != 0x80 || string(messages[1].Payload) != trailers {
				t.Fatalf("splitting %s\nwanted:\nthe message, then the trailers frame with flag 0x80\ngot:\n%q, %v", pieces[size], messages, err)
			}
		}
	})

	t.Run("should cut a Connect body's end-of-stream envelope as a stream message", func(t *testing.T) {
		end := `{"metadata":{"k":["v"]}}`
		for _, size := range []int{0, 1} {
			messages, _, err := split(t, stream.FramingConnect, 1<<20, envelope(1, "zz")+envelope(0x02, end), size)
			if err != nil || len(messages) != 2 || messages[0].Flags != 1 || messages[1].Flags != 0x02 || string(messages[1].Payload) != end {
				t.Fatalf("splitting %s\nwanted:\na compressed message, then the end-of-stream envelope with flag 0x02\ngot:\n%q, %v", pieces[size], messages, err)
			}
		}
	})
}

func TestSplitter_SSE(t *testing.T) {
	for _, size := range []int{0, 1, 3} {
		t.Run("should cut events at a blank line, whatever line endings they use, fed "+pieces[size], func(t *testing.T) {
			events := []string{"id: 1\ndata: first\n\n", "data: a\r\ndata: b\r\n\r\n", ": comment\rdata: c\r\r", "event: x\ndata: d\r\n\n"}
			messages, s, err := split(t, stream.FramingSSE, 1<<20, strings.Join(events, ""), size)
			wantMessages(t, messages, s, err, events...)
		})
	}

	t.Run("should hand back an event as soon as its blank line arrives, before the next event starts", func(t *testing.T) {
		s, _ := stream.NewSplitter(stream.FramingSSE, 1<<20)
		n, message, err := s.Feed([]byte("data: a\n\ndata: b\n\n"))
		if err != nil || message == nil || string(message.Raw) != "data: a\n\n" || n != len("data: a\n\n") {
			t.Fatalf("Feed\nwanted:\nthe first event, %d bytes consumed\ngot:\n%v, %d bytes, %v", len("data: a\n\n"), message, n, err)
		}
	})

	t.Run("should hand back an event cut at a CRLF blank line split across two writes with its LF", func(t *testing.T) {
		messages, s, err := split(t, stream.FramingSSE, 1<<20, "data: a\r\n\r\ndata: b\r\n\r\n", 11)
		wantMessages(t, messages, s, err, "data: a\r\n\r\n", "data: b\r\n\r\n")
	})

	t.Run("should hand back an event ended by a lone CR at the end of the body", func(t *testing.T) {
		messages, s, err := split(t, stream.FramingSSE, 1<<20, "data: a\r\r", 1)
		if err != nil || len(messages) != 0 {
			t.Fatalf("splitting\nwanted:\nno message before the body ends\ngot:\n%q, %v", messages, err)
		}
		last := s.Flush()
		if last == nil || string(last.Raw) != "data: a\r\r" || s.Partial() != nil {
			t.Fatalf("Flush\nwanted:\n%q, nothing partial\ngot:\n%v", "data: a\r\r", last)
		}
	})

	t.Run("should give an event the body ended inside as the partial message", func(t *testing.T) {
		messages, s, err := split(t, stream.FramingSSE, 1<<20, "data: a\n\ndata: b\n", 1)
		if err != nil || len(messages) != 1 || s.Flush() != nil {
			t.Fatalf("splitting\nwanted:\none event, nothing the end completes\ngot:\n%q, %v", messages, err)
		}
		if partial := s.Partial(); partial == nil || string(partial.Raw) != "data: b\n" {
			t.Fatalf("partial\nwanted:\n%q\ngot:\n%v", "data: b\n", partial)
		}
	})

	t.Run("should refuse an event over the limit, with the bytes seen", func(t *testing.T) {
		messages, _, err := split(t, stream.FramingSSE, 10, "data: a\n\ndata: too long\n\n", 1)
		var tooLarge *stream.TooLargeError
		if len(messages) != 1 || !errors.As(err, &tooLarge) || tooLarge.Size != 11 || tooLarge.Limit != 10 {
			t.Fatalf("splitting\nwanted:\none event, then too large: size 11, limit 10\ngot:\n%d messages, %v", len(messages), err)
		}
	})
}

func TestSplitter_NDJSON(t *testing.T) {
	for _, size := range []int{0, 1, 3} {
		t.Run("should cut one message per line, keeping its line ending, fed "+pieces[size], func(t *testing.T) {
			lines := []string{"{\"n\":1}\n", "{\"n\":2}\r\n", "\n", "[3]\n"}
			messages, s, err := split(t, stream.FramingNDJSON, 1<<20, strings.Join(lines, ""), size)
			wantMessages(t, messages, s, err, lines...)
		})
	}

	t.Run("should hand back a last line with no line ending as a whole message at the end of the body", func(t *testing.T) {
		messages, s, err := split(t, stream.FramingNDJSON, 1<<20, "{\"n\":1}\n{\"n\":2}", 1)
		if err != nil || len(messages) != 1 {
			t.Fatalf("splitting\nwanted:\none line\ngot:\n%q, %v", messages, err)
		}
		if last := s.Flush(); last == nil || string(last.Raw) != `{"n":2}` || s.Partial() != nil {
			t.Fatalf("Flush\nwanted:\n%q, nothing partial\ngot:\n%v", `{"n":2}`, last)
		}
	})

	t.Run("should refuse a line over the limit, with the bytes seen", func(t *testing.T) {
		_, _, err := split(t, stream.FramingNDJSON, 4, "{\"long\":1}\n", 1)
		var tooLarge *stream.TooLargeError
		if !errors.As(err, &tooLarge) || tooLarge.Size != 5 || tooLarge.Limit != 4 {
			t.Fatalf("splitting\nwanted:\ntoo large: size 5, limit 4\ngot:\n%v", err)
		}
	})
}
