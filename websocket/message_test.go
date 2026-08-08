package websocket

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMessage_NewMessage(t *testing.T) {
	connectionID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating connection UUID: %v", err)
	}
	requestID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating request UUID: %v", err)
	}

	frame := Frame{
		Fin:     true,
		Opcode:  OpText,
		Masked:  true,
		Key:     [4]byte{1, 2, 3, 4},
		Payload: []byte("Hello"),
	}
	createdBefore := time.Now()

	got, err := NewMessage(connectionID, requestID, "client", frame)
	if err != nil {
		t.Fatalf("NewMessage() returned unexpected error: %v", err)
	}

	if got.ID == uuid.Nil {
		t.Fatal("wanted non-nil message ID, got nil UUID")
	}
	if got.ID.Version() != 7 {
		t.Fatalf("\nwanted UUID version:\n7\ngot:\n%d", got.ID.Version())
	}
	if got.ConnectionID != connectionID {
		t.Fatalf("\nwanted connection ID:\n%s\ngot:\n%s", connectionID, got.ConnectionID)
	}
	if got.RequestID != requestID {
		t.Fatalf("\nwanted request ID:\n%s\ngot:\n%s", requestID, got.RequestID)
	}
	if got.Direction != "client" {
		t.Fatalf("\nwanted direction:\n%q\ngot:\n%q", "client", got.Direction)
	}
	if got.Frame.Fin != frame.Fin {
		t.Fatalf("\nwanted Fin:\n%v\ngot:\n%v", frame.Fin, got.Frame.Fin)
	}
	if got.Frame.Opcode != frame.Opcode {
		t.Fatalf("\nwanted Opcode:\n%d\ngot:\n%d", frame.Opcode, got.Frame.Opcode)
	}
	if got.Frame.Masked != frame.Masked {
		t.Fatalf("\nwanted Masked:\n%v\ngot:\n%v", frame.Masked, got.Frame.Masked)
	}
	if got.Frame.Key != frame.Key {
		t.Fatalf("\nwanted Key:\n%x\ngot:\n%x", frame.Key, got.Frame.Key)
	}
	if !bytes.Equal(got.Frame.Payload, frame.Payload) {
		t.Fatalf("\nwanted payload:\n%q\ngot:\n%q", frame.Payload, got.Frame.Payload)
	}
	if got.Metadata == nil {
		t.Fatal("wanted initialized metadata, got nil")
	}
	if len(got.Metadata) != 0 {
		t.Fatalf("\nwanted empty metadata:\n%v\ngot:\n%v", map[string]any{}, got.Metadata)
	}
	if got.CreatedAt.Before(createdBefore) {
		t.Fatalf("wanted CreatedAt on or after %v, got %v", createdBefore, got.CreatedAt)
	}
	if got.CreatedAt.After(time.Now()) {
		t.Fatalf("wanted CreatedAt no later than now, got %v", got.CreatedAt)
	}
	if got.Dropped {
		t.Fatal("wanted Dropped to be false, got true")
	}
}

func TestMessage_IsBinary(t *testing.T) {
	tests := []struct {
		name   string
		opcode int
		want   bool
	}{
		{name: "continuation", opcode: OpContinuation, want: false},
		{name: "text", opcode: OpText, want: false},
		{name: "binary", opcode: OpBinary, want: true},
		{name: "close", opcode: OpClose, want: false},
		{name: "ping", opcode: OpPing, want: false},
		{name: "pong", opcode: OpPong, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := &Message{Frame: Frame{Opcode: tt.opcode}}
			got := message.IsBinary()
			if got != tt.want {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}

func TestMessage_ToDomain(t *testing.T) {
	messageID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating message UUID: %v", err)
	}
	connectionID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating connection UUID: %v", err)
	}
	createdAt := time.Date(2026, time.August, 2, 12, 30, 0, 0, time.UTC)

	message := &Message{
		ID:           messageID,
		ConnectionID: connectionID,
		Direction:    "server",
		Frame: Frame{
			Fin:     true,
			Opcode:  OpBinary,
			Masked:  true,
			Key:     [4]byte{1, 2, 3, 4},
			Payload: []byte("original"),
		},
		Metadata: map[string]any{
			"source": "upstream",
		},
		CreatedAt: createdAt,
		Dropped:   true,
	}

	got := message.ToDomain()

	if got.ID != messageID {
		t.Fatalf("\nwanted message ID:\n%s\ngot:\n%s", messageID, got.ID)
	}
	if got.ConnectionID != connectionID {
		t.Fatalf("\nwanted connection ID:\n%s\ngot:\n%s", connectionID, got.ConnectionID)
	}
	if got.Direction != "server" {
		t.Fatalf("\nwanted direction:\n%q\ngot:\n%q", "server", got.Direction)
	}
	if got.Opcode != OpBinary {
		t.Fatalf("\nwanted Opcode:\n%d\ngot:\n%d", OpBinary, got.Opcode)
	}
	if !got.Fin {
		t.Fatal("wanted Fin to be true, got false")
	}
	if !got.IsBinary {
		t.Fatal("wanted IsBinary to be true, got false")
	}
	if !bytes.Equal(got.Payload, []byte("original")) {
		t.Fatalf("\nwanted payload:\n%q\ngot:\n%q", "original", got.Payload)
	}
	if got.CreatedAt != createdAt {
		t.Fatalf("\nwanted CreatedAt:\n%v\ngot:\n%v", createdAt, got.CreatedAt)
	}
	if got.Metadata["source"] != "upstream" {
		t.Fatalf("\nwanted metadata source:\n%q\ngot:\n%v", "upstream", got.Metadata["source"])
	}

	message.Frame.Payload[0] = 'X'
	message.Metadata["source"] = "modified"
	if !bytes.Equal(got.Payload, []byte("original")) {
		t.Fatalf("domain payload changed with message payload: %q", got.Payload)
	}
	if got.Metadata["source"] != "upstream" {
		t.Fatalf("domain metadata changed with message metadata: %v", got.Metadata["source"])
	}

	got.Payload[1] = 'Y'
	got.Metadata["source"] = "domain"
	if !bytes.Equal(message.Frame.Payload, []byte("Xriginal")) {
		t.Fatalf("message payload changed with domain payload: %q", message.Frame.Payload)
	}
	if message.Metadata["source"] != "modified" {
		t.Fatalf("message metadata changed with domain metadata: %v", message.Metadata["source"])
	}
}
