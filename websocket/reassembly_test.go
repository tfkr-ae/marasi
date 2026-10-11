package websocket

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

// holdDataMessages returns a process function that holds every text and
// binary message in interceptor, announcing each on the returned channel.
func holdDataMessages(interceptor *Interceptor) (ProcessFunc, <-chan domain.WebSocketMessage) {
	held := make(chan domain.WebSocketMessage, 4)
	process := func(message *Message) error {
		if message.Opcode != OpText && message.Opcode != OpBinary {
			return nil
		}
		return interceptor.Intercept(message, func(snapshot domain.WebSocketMessage) {
			held <- snapshot
		})
	}
	return process, held
}

// writeFrames writes frames from peer in the background, masked when mask is
// true, and reports the first write error.
func writeFrames(peer net.Conn, mask bool, frames ...Frame) <-chan error {
	result := make(chan error, 1)
	go func() {
		for _, frame := range frames {
			if err := WriteFrame(peer, frame, mask); err != nil {
				result <- err
				return
			}
		}
		result <- nil
	}()
	return result
}

func readFrame(t *testing.T, peer net.Conn) Frame {
	t.Helper()
	frame, err := ReadFrame(peer)
	if err != nil {
		t.Fatalf("reading frame: %v", err)
	}
	return frame
}

func receiveHeld(t *testing.T, held <-chan domain.WebSocketMessage) domain.WebSocketMessage {
	t.Helper()
	select {
	case message := <-held:
		return message
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for a held message")
		return domain.WebSocketMessage{}
	}
}

func TestRelay_HoldsTheWholeMessage(t *testing.T) {
	first := Frame{Fin: false, Opcode: OpText, Payload: []byte("Hel")}
	ping := Frame{Fin: true, Opcode: OpPing, Payload: []byte("p")}
	second := Frame{Fin: false, Opcode: OpContinuation, Payload: []byte("lo, ")}
	third := Frame{Fin: true, Opcode: OpContinuation, Payload: []byte("world")}

	t.Run("should relay a ping between fragments before the held message is resolved", func(t *testing.T) {
		interceptor := NewInterceptor()
		process, held := holdDataMessages(interceptor)
		relay := startRelayWith(t, process)
		written := writeFrames(relay.client, true, first, ping, second, third)

		assertSameFrame(t, ping, readFrame(t, relay.upstream))

		message := receiveHeld(t, held)
		if string(message.Payload) != "Hello, world" || message.Opcode != OpText || message.FrameCount != 3 {
			t.Fatalf("\nwanted held message:\n%q opcode %d, %d frames\ngot:\n%q opcode %d, %d frames",
				"Hello, world", OpText, 3, message.Payload, message.Opcode, message.FrameCount)
		}
		if err := <-written; err != nil {
			t.Fatalf("writing client frames: %v", err)
		}

		if err := interceptor.Resolve(message.ID, InterceptionDecision{Resume: true, Opcode: OpText, Payload: message.Payload}); err != nil {
			t.Fatalf("resolving held message: %v", err)
		}
		for _, want := range []Frame{first, second, third} {
			assertSameFrame(t, want, readFrame(t, relay.upstream))
		}
	})

	t.Run("should forward an edited message as one frame", func(t *testing.T) {
		interceptor := NewInterceptor()
		process, held := holdDataMessages(interceptor)
		relay := startRelayWith(t, process)
		writeFrames(relay.upstream, false, first, second, third)

		message := receiveHeld(t, held)
		if err := interceptor.Resolve(message.ID, InterceptionDecision{Resume: true, Opcode: OpBinary, Payload: []byte("edited")}); err != nil {
			t.Fatalf("resolving held message: %v", err)
		}

		assertSameFrame(t, Frame{Fin: true, Opcode: OpBinary, Payload: []byte("edited")}, readFrame(t, relay.client))
	})
}

func assertCloseCode(t *testing.T, want int, frame Frame) {
	t.Helper()
	code, _, err := ParseClosePayload(frame.Payload)
	if frame.Opcode != OpClose || err != nil || code != want {
		t.Fatalf("\nwanted close:\n%d\ngot:\n%+v (code %d, err %v)", want, frame, code, err)
	}
}

func TestRelay_LimitsMessageSize(t *testing.T) {
	first := Frame{Fin: false, Opcode: OpText, Payload: []byte("123456")}
	second := Frame{Fin: true, Opcode: OpContinuation, Payload: []byte("789012")}

	t.Run("should close with 1009 to the sender and 1001 to the receiver", func(t *testing.T) {
		tooBig := make(chan MessageTooBig, 1)
		relay := startRelayWith(t, nil, func(c *Connection) {
			c.SetMaxMessageSize(10)
			c.SetMessageTooBigHandler(func(event MessageTooBig) { tooBig <- event })
		})
		writeFrames(relay.client, true, first, second)

		assertCloseCode(t, CloseMessageTooBig, readFrame(t, relay.client))
		assertCloseCode(t, CloseGoingAway, readFrame(t, relay.upstream))
		want := MessageTooBig{Direction: DirectionFromClient, Size: 12, Limit: 10}
		if got := <-tooBig; got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if code, _ := relay.connection.CloseDetails(); code != CloseMessageTooBig {
			t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", CloseMessageTooBig, code)
		}
	})

	t.Run("should apply the limit to a message from the server", func(t *testing.T) {
		relay := startRelayWith(t, nil, func(c *Connection) { c.SetMaxMessageSize(10) })
		writeFrames(relay.upstream, false, first, second)

		assertCloseCode(t, CloseGoingAway, readFrame(t, relay.client))
		assertCloseCode(t, CloseMessageTooBig, readFrame(t, relay.upstream))
	})

	t.Run("should apply the limit mid-stream after earlier frames went out", func(t *testing.T) {
		relay := startRelayWith(t, nil, func(c *Connection) {
			c.SetMaxMessageSize(10)
			c.SetStreamFunc(func(string, int) bool { return true })
		})
		writeFrames(relay.client, true, first, second)

		assertSameFrame(t, first, readFrame(t, relay.upstream))
		assertCloseCode(t, CloseMessageTooBig, readFrame(t, relay.client))
		assertCloseCode(t, CloseGoingAway, readFrame(t, relay.upstream))
	})
}

func TestRelay_StreamsMessagesNoHookCanHold(t *testing.T) {
	t.Run("should forward payload bytes before the frame is complete", func(t *testing.T) {
		recorded := make(chan *Message, 1)
		relay := startRelayWith(t, func(message *Message) error {
			recorded <- message
			return nil
		}, func(c *Connection) {
			c.SetStreamFunc(func(direction string, opcode int) bool {
				return direction == DirectionFromClient && opcode == OpText
			})
		})

		var wire bytes.Buffer
		if err := WriteFrame(&wire, Frame{Fin: true, Opcode: OpText, Payload: []byte("abcdef")}, true); err != nil {
			t.Fatalf("encoding frame: %v", err)
		}
		encoded := wire.Bytes()
		const headerLength = 6
		written := make(chan error, 1)
		go func() {
			_, err := relay.client.Write(encoded[:headerLength+3])
			written <- err
		}()

		got := make([]byte, headerLength+3)
		if _, err := io.ReadFull(relay.upstream, got); err != nil {
			t.Fatalf("reading streamed bytes: %v", err)
		}
		var key [4]byte
		copy(key[:], got[2:6])
		maskBytes(got[headerLength:], key, 0)
		if got[0] != 0x81 || got[1] != 0x80|6 || string(got[headerLength:]) != "abc" {
			t.Fatalf("\nwanted first bytes:\n%x then %q\ngot:\n%x then %q", []byte{0x81, 0x86}, "abc", got[:2], got[headerLength:])
		}
		if err := <-written; err != nil {
			t.Fatalf("writing first chop: %v", err)
		}

		go func() {
			_, err := relay.client.Write(encoded[headerLength+3:])
			written <- err
		}()
		rest := make([]byte, 3)
		if _, err := io.ReadFull(relay.upstream, rest); err != nil {
			t.Fatalf("reading the rest: %v", err)
		}
		maskBytes(rest, key, 3)
		if string(rest) != "def" {
			t.Fatalf("\nwanted rest:\n%q\ngot:\n%q", "def", rest)
		}

		message := <-recorded
		if !message.Passthrough || string(message.Payload) != "abcdef" || message.FrameCount != 1 {
			t.Fatalf("\nwanted recorded passthrough message:\n%q, 1 frame\ngot:\n%q, %d frames, passthrough %v",
				"abcdef", message.Payload, message.FrameCount, message.Passthrough)
		}
	})

	t.Run("should keep original frame boundaries and record the whole message", func(t *testing.T) {
		recorded := make(chan *Message, 1)
		relay := startRelayWith(t, func(message *Message) error {
			recorded <- message
			return nil
		}, func(c *Connection) {
			c.SetStreamFunc(func(string, int) bool { return true })
		})
		frames := []Frame{
			{Fin: false, Opcode: OpBinary, Payload: []byte{1, 2}},
			{Fin: false, Opcode: OpContinuation, Payload: []byte{3}},
			{Fin: true, Opcode: OpContinuation, Payload: []byte{4, 5}},
		}
		writeFrames(relay.upstream, false, frames...)

		for _, want := range frames {
			assertSameFrame(t, want, readFrame(t, relay.client))
		}
		message := <-recorded
		if !bytes.Equal(message.Payload, []byte{1, 2, 3, 4, 5}) || message.Opcode != OpBinary || message.FrameCount != 3 {
			t.Fatalf("\nwanted recorded message:\n%v opcode %d, 3 frames\ngot:\n%v opcode %d, %d frames",
				[]byte{1, 2, 3, 4, 5}, OpBinary, message.Payload, message.Opcode, message.FrameCount)
		}
	})
}

func TestRelay_AnnouncesMessagesStillArriving(t *testing.T) {
	progress := make(chan domain.WebSocketMessageProgress, 4)
	recorded := make(chan *Message, 1)
	relay := startRelayWith(t, func(message *Message) error {
		recorded <- message
		return nil
	}, func(c *Connection) {
		c.SetProgressHandler(func(event domain.WebSocketMessageProgress) { progress <- event })
	})
	writeFrames(relay.client, true,
		Frame{Fin: false, Opcode: OpText, Payload: []byte("abc")},
		Frame{Fin: true, Opcode: OpContinuation, Payload: []byte("de")},
	)

	var event domain.WebSocketMessageProgress
	select {
	case event = <-progress:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for a progress event")
	}
	readFrame(t, relay.upstream)
	message := <-recorded
	want := domain.WebSocketMessageProgress{
		MessageID: message.ID,
		Direction: DirectionFromClient,
		Opcode:    OpText,
		Bytes:     3,
		Frames:    1,
	}
	if event != want {
		t.Fatalf("\nwanted progress:\n%+v\ngot:\n%+v", want, event)
	}
}

func TestRelay_ForwardsFragmentViolations(t *testing.T) {
	tests := []struct {
		name   string
		frames []Frame
	}{
		{
			name: "should forward a continuation with nothing to continue",
			frames: []Frame{
				{Fin: true, Opcode: OpContinuation, Payload: []byte("x")},
			},
		},
		{
			name: "should forward a new message that starts before the last one finished",
			frames: []Frame{
				{Fin: false, Opcode: OpText, Payload: []byte("a")},
				{Fin: true, Opcode: OpText, Payload: []byte("b")},
			},
		},
	}

	for _, tt := range tests {
		for _, streamed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, streamed %v", tt.name, streamed), func(t *testing.T) {
				relay := startRelayWith(t, nil, func(c *Connection) {
					c.SetStreamFunc(func(string, int) bool { return streamed })
				})
				writeFrames(relay.client, true, tt.frames...)

				for _, want := range tt.frames {
					assertSameFrame(t, want, readFrame(t, relay.upstream))
				}
			})
		}
	}
}
