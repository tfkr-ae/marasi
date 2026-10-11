package websocket

import (
	"bytes"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// Message is a live WebSocket message flowing through the proxy relay: a whole
// text or binary message reassembled from its frames, or a single control
// frame (ADR-0027).
type Message struct {
	// ID uniquely identifies the message.
	ID uuid.UUID
	// ConnectionID is the live connection this message belongs to.
	ConnectionID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the connection.
	RequestID uuid.UUID
	// Direction is either DirectionFromClient or DirectionFromServer.
	Direction string
	// Opcode is the message opcode: the opcode of its first frame.
	Opcode int
	// Payload is the whole message payload.
	Payload []byte
	// FrameCount is how many frames carried the message on the wire.
	FrameCount int
	// Compressed reports that the message is compressed with
	// permessage-deflate on the wire. Payload is always uncompressed. The
	// proxy compresses a compressed message again for the receiver.
	Compressed bool
	// Metadata stores extension and proxy annotations for the message.
	Metadata map[string]any
	// CreatedAt is when the message was created by the proxy.
	CreatedAt time.Time
	// Dropped indicates the message should not be forwarded.
	Dropped bool
	// Skipped indicates later extension processing should stop while still forwarding.
	Skipped bool
	// Passthrough reports that the relay forwards the message without holding
	// or changing it: its bytes were streamed as they arrived. Process records
	// a passthrough message and runs no hooks on it.
	Passthrough bool

	// frames are the frames as received. An unchanged message is forwarded
	// with them, keeping its original frame boundaries.
	frames []Frame
	// incomplete marks a message cut short by a protocol violation: a new
	// message started before its final frame.
	incomplete bool
}

// IsBinary reports whether the message carries a binary payload.
func (m *Message) IsBinary() bool {
	return m.Opcode == OpBinary
}

// ToDomain returns a serializable domain snapshot of the message.
func (m *Message) ToDomain() domain.WebSocketMessage {
	return domain.WebSocketMessage{
		ID:           m.ID,
		ConnectionID: m.ConnectionID,
		RequestID:    m.RequestID,
		Direction:    m.Direction,
		Opcode:       m.Opcode,
		FrameCount:   m.FrameCount,
		Compressed:   m.Compressed,
		Payload:      append([]byte(nil), m.Payload...),
		IsBinary:     m.Opcode == OpBinary,
		CreatedAt:    m.CreatedAt,
		Metadata:     maps.Clone(m.Metadata),
	}
}

// NewMessage creates a one-frame message for the given connection.
func NewMessage(connectionID, requestID uuid.UUID, direction string, frame Frame) (*Message, error) {
	message, err := newMessage(connectionID, requestID, direction)
	if err != nil {
		return nil, err
	}
	message.Opcode = frame.Opcode
	message.Payload = frame.Payload
	message.FrameCount = 1
	message.frames = []Frame{frame}
	return message, nil
}

func newMessage(connectionID, requestID uuid.UUID, direction string) (*Message, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("creating uuid : %w", err)
	}

	return &Message{
		ID:           id,
		ConnectionID: connectionID,
		RequestID:    requestID,
		Direction:    direction,
		Metadata:     make(map[string]any),
		CreatedAt:    time.Now(),
	}, nil
}

// addFrame appends a received frame to the message.
func (m *Message) addFrame(frame Frame) {
	if len(m.frames) == 0 {
		m.Opcode = frame.Opcode
	}
	m.frames = append(m.frames, frame)
	m.Payload = append(m.Payload, frame.Payload...)
	m.FrameCount++
}

// unchanged reports whether the opcode and payload still match the frames as
// received.
func (m *Message) unchanged() bool {
	if len(m.frames) == 0 || m.frames[0].Opcode != m.Opcode {
		return false
	}
	rest := m.Payload
	for _, frame := range m.frames {
		if len(rest) < len(frame.Payload) || !bytes.Equal(rest[:len(frame.Payload)], frame.Payload) {
			return false
		}
		rest = rest[len(frame.Payload):]
	}
	return len(rest) == 0
}

// outgoing returns the frames to forward. A compressed text or binary message
// goes as one frame that the receiver's leg compresses. Any other message goes
// with the frames as received when it is unchanged, otherwise as one frame
// with the message's opcode and payload.
func (m *Message) outgoing() []Frame {
	compress := m.Compressed && (m.Opcode == OpText || m.Opcode == OpBinary)
	if !compress && m.unchanged() {
		return m.frames
	}
	// RSV1 marks a compressed payload. The receiver's leg sets it when it
	// compresses this frame; the payload is never compressed here.
	frame := Frame{Fin: !m.incomplete, Opcode: m.Opcode, Payload: m.Payload, deflate: compress}
	if len(m.frames) > 0 {
		frame.Rsv2 = m.frames[0].Rsv2
		frame.Rsv3 = m.frames[0].Rsv3
	}
	return []Frame{frame}
}
