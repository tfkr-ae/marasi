package websocket

import (
	"sync"
	"testing"
	"time"
)

// recordedMessages is a process func that keeps the record of each message as
// the proxy stores it: the snapshot taken when processing ends.
type recordedMessages struct {
	mu      sync.Mutex
	records []map[string]any
	opcodes []int
	sources []string
	hold    *heldMessages
}

func (r *recordedMessages) process(message *Message) error {
	if r.hold != nil {
		_ = r.hold.process(message)
	}
	record := message.ToDomain()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record.Metadata)
	r.opcodes = append(r.opcodes, record.Opcode)
	r.sources = append(r.sources, record.Direction)
	return nil
}

// droppedAfterClose returns, for each recorded message from source with
// opcode, whether it was recorded as dropped after a close.
func (r *recordedMessages) droppedAfterClose(source string, opcode int) []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var got []bool
	for i, metadata := range r.records {
		if r.sources[i] == source && r.opcodes[i] == opcode {
			dropped, _ := metadata["dropped_after_close"].(bool)
			got = append(got, dropped)
		}
	}
	return got
}

func assertRecorded(t *testing.T, what string, want, got []bool) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("\nwanted %s dropped_after_close:\n%v\ngot:\n%v", what, want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("\nwanted %s dropped_after_close:\n%v\ngot:\n%v", what, want, got)
		}
	}
}

func assertNextFrame(t *testing.T, peer string, read func() (Frame, error), want Frame) {
	t.Helper()
	got, err := read()
	if err != nil {
		t.Fatalf("\nwanted frame at %s:\n%+v\ngot error:\n%v", peer, want, err)
	}
	assertSameFrame(t, want, got)
}

// A close frame sent again by the same peer still goes through, so it marks
// where the next forwarded frame would have been.
func TestRelay_ForwardsNothingButACloseAfterAClose(t *testing.T) {
	// The peers here never drop TCP, so only a test failure ends the relay.
	patient := func(c *Connection) { c.relayedCloseTimeout = time.Minute }
	closeFrame := Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xe8}}
	marker := Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xe9}}
	ping := Frame{Fin: true, Opcode: OpPing, Payload: []byte("p")}
	pong := Frame{Fin: true, Opcode: OpPong, Payload: []byte("q")}
	text := Frame{Fin: true, Opcode: OpText, Payload: []byte("hello")}

	t.Run("should record a client's ping after its close and not forward it", func(t *testing.T) {
		recorded := &recordedMessages{}
		relay := startRelayWith(t, recorded.process, patient)
		upstream := func() (Frame, error) { return ReadFrame(relay.upstream) }

		writeFrames(relay.client, true, ping)
		assertNextFrame(t, "upstream", upstream, ping)
		writeFrames(relay.client, true, closeFrame)
		assertNextFrame(t, "upstream", upstream, closeFrame)
		writeFrames(relay.client, true, ping, text, pong, marker)
		assertNextFrame(t, "upstream", upstream, marker)

		assertRecorded(t, "client ping", []bool{false, true}, recorded.droppedAfterClose(DirectionFromClient, OpPing))
		assertRecorded(t, "client text", []bool{true}, recorded.droppedAfterClose(DirectionFromClient, OpText))
		assertRecorded(t, "client pong", []bool{true}, recorded.droppedAfterClose(DirectionFromClient, OpPong))
		assertRecorded(t, "client close", []bool{false, false}, recorded.droppedAfterClose(DirectionFromClient, OpClose))
	})

	t.Run("should keep forwarding the other direction until its own close", func(t *testing.T) {
		recorded := &recordedMessages{}
		relay := startRelayWith(t, recorded.process, patient)
		upstream := func() (Frame, error) { return ReadFrame(relay.upstream) }
		client := func() (Frame, error) { return ReadFrame(relay.client) }

		writeFrames(relay.client, true, closeFrame)
		assertNextFrame(t, "upstream", upstream, closeFrame)
		writeFrames(relay.upstream, false, text)
		assertNextFrame(t, "client", client, text)
		writeFrames(relay.upstream, false, ping)
		assertNextFrame(t, "client", client, ping)
		writeFrames(relay.upstream, false, closeFrame)
		assertNextFrame(t, "client", client, closeFrame)
		writeFrames(relay.upstream, false, ping, marker)
		assertNextFrame(t, "client", client, marker)

		assertRecorded(t, "server text", []bool{false}, recorded.droppedAfterClose(DirectionFromServer, OpText))
		assertRecorded(t, "server ping", []bool{false, true}, recorded.droppedAfterClose(DirectionFromServer, OpPing))
	})
}

func TestRelay_HeldMessageIsNotForwardedAfterACloseOvertakesIt(t *testing.T) {
	closeFrame := Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xe8}}
	marker := Frame{Fin: true, Opcode: OpClose, Payload: []byte{0x03, 0xe9}}
	text := Frame{Fin: true, Opcode: OpText, Payload: []byte("held")}

	t.Run("should record a held server text as dropped when a close reached the client while it was held", func(t *testing.T) {
		held := holdMessages(t, func(message *Message) bool {
			return message.Direction == DirectionFromServer && message.Opcode == OpText
		})
		recorded := &recordedMessages{hold: held}
		relay := startRelayWith(t, recorded.process, func(c *Connection) { c.relayedCloseTimeout = time.Minute })
		client := func() (Frame, error) { return ReadFrame(relay.client) }

		writeFrames(relay.upstream, false, text)
		held.awaitHeld(t)
		injected := make(chan error, 1)
		go func() {
			_, err := relay.connection.Inject(DirectionFromServer, OpClose, closeFrame.Payload, InjectOptions{})
			injected <- err
		}()
		assertNextFrame(t, "client", client, closeFrame)
		if err := <-injected; err != nil {
			t.Fatalf("injecting close toward the client: %v", err)
		}
		held.releaseAll()
		writeFrames(relay.upstream, false, marker)
		assertNextFrame(t, "client", client, marker)

		assertRecorded(t, "server text", []bool{true}, recorded.droppedAfterClose(DirectionFromServer, OpText))
	})
}
