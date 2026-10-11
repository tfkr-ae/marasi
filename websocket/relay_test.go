package websocket

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
)

// runningRelay is a Connection running between two in-memory peers.
type runningRelay struct {
	connection *Connection
	client     net.Conn
	upstream   net.Conn
	result     chan error
}

func startRelay(t *testing.T) *runningRelay {
	t.Helper()

	clientConnection, clientPeer := net.Pipe()
	upstreamConnection, upstreamPeer := net.Pipe()
	t.Cleanup(func() {
		_ = clientPeer.Close()
		_ = upstreamPeer.Close()
	})

	deadline := time.Now().Add(5 * time.Second)
	for _, peer := range []net.Conn{clientPeer, upstreamPeer} {
		if err := peer.SetDeadline(deadline); err != nil {
			t.Fatalf("setting peer deadline: %v", err)
		}
	}

	relay := &runningRelay{
		connection: NewConnection(uuid.Nil, uuid.Nil, clientConnection, clientConnection, upstreamConnection, nil),
		client:     clientPeer,
		upstream:   upstreamPeer,
		result:     make(chan error, 1),
	}
	relay.connection.relayedCloseTimeout = 200 * time.Millisecond
	go func() { relay.result <- relay.connection.Run() }()

	return relay
}

// relayFromClient writes frame from the client peer and returns what the upstream peer reads.
func (r *runningRelay) relayFromClient(t *testing.T, frame Frame) Frame {
	t.Helper()
	if err := WriteFrame(r.client, frame, true); err != nil {
		t.Fatalf("writing client frame: %v", err)
	}
	got, err := ReadFrame(r.upstream)
	if err != nil {
		t.Fatalf("reading frame at upstream peer: %v", err)
	}
	return got
}

// relayFromServer writes frame from the upstream peer and returns what the client peer reads.
func (r *runningRelay) relayFromServer(t *testing.T, frame Frame) Frame {
	t.Helper()
	if err := WriteFrame(r.upstream, frame, false); err != nil {
		t.Fatalf("writing server frame: %v", err)
	}
	got, err := ReadFrame(r.client)
	if err != nil {
		t.Fatalf("reading frame at client peer: %v", err)
	}
	return got
}

func assertSameFrame(t *testing.T, want, got Frame) {
	t.Helper()
	if got.Fin != want.Fin || got.Opcode != want.Opcode ||
		got.Rsv1 != want.Rsv1 || got.Rsv2 != want.Rsv2 || got.Rsv3 != want.Rsv3 ||
		!bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("\nwanted frame:\n%+v\ngot:\n%+v", want, got)
	}
}

func TestRelay_ForwardsProtocolViolationsUnchanged(t *testing.T) {
	tests := []struct {
		name  string
		frame Frame
	}{
		{name: "should forward RSV bits with no extension", frame: Frame{Fin: true, Rsv1: true, Rsv2: true, Rsv3: true, Opcode: OpText, Payload: []byte("hi")}},
		{name: "should forward a reserved data opcode", frame: Frame{Fin: true, Opcode: 0x3}},
		{name: "should forward a reserved control opcode", frame: Frame{Fin: true, Opcode: 0xB, Payload: []byte("x")}},
		{name: "should forward an oversized ping", frame: Frame{Fin: true, Opcode: OpPing, Payload: bytes.Repeat([]byte{'p'}, 126)}},
		{name: "should forward a fragmented ping", frame: Frame{Fin: false, Opcode: OpPing, Payload: []byte("frag")}},
		{name: "should forward invalid UTF-8 in text", frame: Frame{Fin: true, Opcode: OpText, Payload: []byte{0xce, 0xba, 0xff}}},
	}

	for _, tt := range tests {
		t.Run(tt.name+" from the client", func(t *testing.T) {
			relay := startRelay(t)
			assertSameFrame(t, tt.frame, relay.relayFromClient(t, tt.frame))
		})
		t.Run(tt.name+" from the server", func(t *testing.T) {
			relay := startRelay(t)
			assertSameFrame(t, tt.frame, relay.relayFromServer(t, tt.frame))
		})
	}
}

func (r *runningRelay) assertReturnsCleanly(t *testing.T) {
	t.Helper()
	select {
	case err := <-r.result:
		if err != nil {
			t.Fatalf("Run() returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("\nwanted Run state:\n%q\ngot:\n%q", "returned", "blocked")
	}
}

func TestRelay_CompletesTheCloseHandshake(t *testing.T) {
	closeFrame := Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xe8}}

	t.Run("should relay the server's reply to a client close", func(t *testing.T) {
		relay := startRelay(t)

		relay.relayFromClient(t, closeFrame)
		assertSameFrame(t, closeFrame, relay.relayFromServer(t, closeFrame))

		_ = relay.upstream.Close()
		relay.assertReturnsCleanly(t)
	})

	t.Run("should relay the client's reply to a server close", func(t *testing.T) {
		relay := startRelay(t)

		relay.relayFromServer(t, closeFrame)
		assertSameFrame(t, closeFrame, relay.relayFromClient(t, closeFrame))

		_ = relay.upstream.Close()
		relay.assertReturnsCleanly(t)
		if _, err := ReadFrame(relay.client); err == nil {
			t.Fatalf("\nwanted client connection:\n%q\ngot:\n%q", "closed", "open")
		}
	})

	t.Run("should stop waiting for a reply that never comes", func(t *testing.T) {
		relay := startRelay(t)

		relay.relayFromClient(t, closeFrame)

		relay.assertReturnsCleanly(t)
	})
}

func TestRelay_RecordsInvalidCloseOnTheStoredMessage(t *testing.T) {
	var seen map[string]any
	connection := NewConnection(uuid.Nil, uuid.Nil, nil, &testReadWriteCloser{}, &testReadWriteCloser{},
		func(message *Message) error {
			seen = message.Metadata
			return nil
		})
	source := bytes.NewReader(encodeTestFrames(t, Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xe7}}))

	if err := connection.relayFrames(source, func(Frame) error { return nil }, DirectionFromClient); err != nil {
		t.Fatalf("relayFrames() returned unexpected error: %v", err)
	}

	if got, _ := seen["close_error"].(string); got != "invalid close code: 999" {
		t.Fatalf("\nwanted close_error metadata:\n%q\ngot:\n%q", "invalid close code: 999", got)
	}
}

func TestRelay_ForwardsInvalidClosePayloads(t *testing.T) {
	tests := []struct {
		name       string
		payload    []byte
		wantCode   int
		wantReason string
	}{
		{name: "should forward a one-byte close payload", payload: []byte{0x03}, wantCode: 0},
		{name: "should forward a reserved close code", payload: []byte{0x03, 0xe7}, wantCode: 999},
		{name: "should forward close code 1005", payload: []byte{0x03, 0xed}, wantCode: 1005},
		{name: "should forward close code 5000", payload: []byte{0x13, 0x88}, wantCode: 5000},
		{
			name:       "should forward a non-UTF-8 close reason",
			payload:    []byte{0x03, 0xe8, 'o', 'k', 0xff},
			wantCode:   1000,
			wantReason: "ok�",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" from the client", func(t *testing.T) {
			relay := startRelay(t)
			closeFrame := Frame{Fin: true, Opcode: OpClose, Payload: tt.payload}

			assertSameFrame(t, closeFrame, relay.relayFromClient(t, closeFrame))

			code, reason := relay.connection.CloseDetails()
			if code != tt.wantCode || reason != tt.wantReason {
				t.Fatalf("\nwanted close details:\n%d %q\ngot:\n%d %q", tt.wantCode, tt.wantReason, code, reason)
			}
		})
		t.Run(tt.name+" from the server", func(t *testing.T) {
			relay := startRelay(t)
			closeFrame := Frame{Fin: true, Opcode: OpClose, Payload: tt.payload}

			assertSameFrame(t, closeFrame, relay.relayFromServer(t, closeFrame))
		})
	}
}
