package armory

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// envelope is one envelope of a sent body: its flags byte and payload.
type envelope struct {
	flags   byte
	payload []byte
}

// flaggedEnvelopes cuts body into envelopes, keeping each one's flags,
// failing unless each length matches the bytes that follow it exactly.
func flaggedEnvelopes(t *testing.T, body string) []envelope {
	t.Helper()
	var out []envelope
	for len(body) > 0 {
		if len(body) < 5 {
			t.Fatalf("a trailing partial envelope header %q", body)
		}
		n := int(binary.BigEndian.Uint32([]byte(body[1:5])))
		if len(body) < 5+n {
			t.Fatalf("the envelope declares %d bytes but %d follow", n, len(body)-5)
		}
		out = append(out, envelope{flags: body[0], payload: []byte(body[5 : 5+n])})
		body = body[5+n:]
	}
	return out
}

// gunzip decodes a gzip-compressed payload, failing if it is not gzip.
func gunzip(t *testing.T, payload []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("the payload\nwanted:\ngzip\ngot:\n% x (%v)", payload, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading the gzip payload: %v", err)
	}
	return out
}

func TestManager_CompressedMessages(t *testing.T) {
	cases := []struct {
		name    string
		head    string
		framing stream.Framing
	}{
		{
			name:    "gRPC with grpc-encoding",
			head:    "POST /test.Echo/Say HTTP/2\r\n:authority: example.com\r\ncontent-type: application/grpc\r\ngrpc-encoding: gzip\r\nte: trailers\r\n\r\n",
			framing: "grpc",
		},
		{
			name:    "Connect streaming with connect-content-encoding",
			head:    "POST /test.Echo/Say HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/connect+proto\r\nConnect-Content-Encoding: gzip\r\n\r\n",
			framing: "connect",
		},
	}
	for _, tt := range cases {
		t.Run("should send a compressed message gzip-compressed with its flag set and a plain one as it is, for "+tt.name, func(t *testing.T) {
			raw := tt.head + "--- message compressed ---\n1:LEN {\"@@fallback@@\"}\n--- message ---\n2:VARINT 150\n"

			sent := runMessageForm(t, raw, tt.framing, domain.ArmoryAttackHarpoon, map[string][]string{"words": {"x", strings.Repeat("A", 300)}})

			if len(sent) != 2 {
				t.Fatalf("sent requests\nwanted:\n2\ngot:\n%d", len(sent))
			}
			for i, want := range []string{"x", strings.Repeat("A", 300)} {
				messages := flaggedEnvelopes(t, bodyOf(t, sent[i]))
				if len(messages) != 2 {
					t.Fatalf("request %d messages\nwanted:\n2\ngot:\n%d", i, len(messages))
				}
				if messages[0].flags != 1 {
					t.Fatalf("request %d first message's flags\nwanted:\n1\ngot:\n%d", i, messages[0].flags)
				}
				if got := stringField(t, gunzip(t, messages[0].payload)); got != want {
					t.Fatalf("request %d first message's field 1\nwanted:\n%q\ngot:\n%q", i, want, got)
				}
				if messages[1].flags != 0 || string(messages[1].payload) != "\x10\x96\x01" {
					t.Fatalf("request %d second message\nwanted:\nflags 0, 10 96 01\ngot:\nflags %d, % x", i, messages[1].flags, messages[1].payload)
				}
			}
		})
	}
}

func TestManager_ValidateCompressedTemplate(t *testing.T) {
	manager := &Manager{}
	refusals := []struct {
		name string
		head string
		want string
	}{
		{
			name: "a request with no encoding",
			head: grpcHead,
			want: "message 1 (line 6): a compressed message needs a grpc-encoding",
		},
		{
			name: "a request whose encoding is identity",
			head: "POST /test.Echo/Say HTTP/2\r\n:authority: example.com\r\ncontent-type: application/grpc\r\ngrpc-encoding: identity\r\n\r\n",
			want: "needs a grpc-encoding",
		},
		{
			name: "a request with an encoding Marasi cannot compress",
			head: "POST /test.Echo/Say HTTP/2\r\n:authority: example.com\r\ncontent-type: application/grpc\r\ngrpc-encoding: snappy\r\n\r\n",
			want: `cannot be compressed with the grpc-encoding "snappy"`,
		},
	}
	for _, tt := range refusals {
		t.Run("should refuse a compressed message on "+tt.name, func(t *testing.T) {
			raw := tt.head + "--- message compressed ---\n1:LEN {\"@@a@@\"}\n"
			err := manager.ValidateTemplate(&domain.ArmoryTemplate{RawTemplate: raw, Framing: "grpc"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("\nwanted:\nerror containing %q\ngot:\n%v", tt.want, err)
			}
		})
	}

	t.Run("should accept a plain message on a request with no encoding", func(t *testing.T) {
		raw := grpcHead + "--- message ---\n1:LEN {\"@@a@@\"}\n"
		if err := manager.ValidateTemplate(&domain.ArmoryTemplate{RawTemplate: raw, Framing: "grpc"}); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}
