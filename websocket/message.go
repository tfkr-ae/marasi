package websocket

import (
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// Message is a live WebSocket message flowing through the proxy relay.
type Message struct {
	// ID uniquely identifies the message.
	ID uuid.UUID
	// ConnectionID is the live connection this message belongs to.
	ConnectionID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the connection.
	RequestID uuid.UUID
	// Direction is either DirectionFromClient or DirectionFromServer.
	Direction string
	// Frame is the underlying protocol frame.
	Frame Frame
	// Metadata stores extension and proxy annotations for the message.
	Metadata map[string]any
	// CreatedAt is when the message was created by the proxy.
	CreatedAt time.Time
	// Dropped indicates the message should not be forwarded.
	Dropped bool
	// Skipped indicates later extension processing should stop while still forwarding.
	Skipped bool
}

// IsBinary reports whether the message carries a binary payload.
func (m *Message) IsBinary() bool {
	return m.Frame.Opcode == OpBinary
}

// ToDomain returns a serializable domain snapshot of the message.
func (m *Message) ToDomain() domain.WebSocketMessage {
	return domain.WebSocketMessage{
		ID:           m.ID,
		ConnectionID: m.ConnectionID,
		RequestID:    m.RequestID,
		Direction:    m.Direction,
		Opcode:       m.Frame.Opcode,
		Fin:          m.Frame.Fin,
		Payload:      append([]byte(nil), m.Frame.Payload...),
		IsBinary:     m.Frame.Opcode == OpBinary,
		CreatedAt:    m.CreatedAt,
		Metadata:     maps.Clone(m.Metadata),
	}
}

// NewMessage creates a message for the given connection and frame.
func NewMessage(connectionID, requestID uuid.UUID, direction string, frame Frame) (*Message, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("creating uuid : %w", err)
	}

	return &Message{
		ID:           id,
		ConnectionID: connectionID,
		RequestID:    requestID,
		Direction:    direction,
		Frame:        frame,
		Metadata:     make(map[string]any),
		CreatedAt:    time.Now(),
	}, nil
}
