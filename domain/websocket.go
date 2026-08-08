package domain

import (
	"time"

	"github.com/google/uuid"
)

// WebSocketConnectionUpdate wraps a connection record queued for persistence update.
type WebSocketConnectionUpdate struct {
	// Connection is the updated connection state.
	Connection WebSocketConnection
}

// WebSocketConnection is the persisted state of a proxied WebSocket connection.
type WebSocketConnection struct {
	// ID uniquely identifies the connection.
	ID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the connection.
	RequestID uuid.UUID
	// State is one of open, closed, or error.
	State string
	// Transport is ws or wss.
	Transport string
	// Host is the target host from the upgrade request.
	Host string
	// Path is the target path, including query string when present.
	Path string
	// StartedAt is when the connection opened.
	StartedAt time.Time
	// ClosedAt is when the connection closed, if closed.
	ClosedAt *time.Time
	// CloseCode is the WebSocket close status code.
	CloseCode int
	// CloseReason is the WebSocket close reason string.
	CloseReason string
}

// WebSocketMessage is a persisted WebSocket frame snapshot.
type WebSocketMessage struct {
	// ID uniquely identifies the message.
	ID uuid.UUID
	// ConnectionID is the connection that carried the message.
	ConnectionID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the connection.
	RequestID uuid.UUID
	// Direction is client or server.
	Direction string
	// Opcode is the WebSocket frame opcode.
	Opcode int
	// Fin indicates whether this was the final fragment.
	Fin bool
	// Payload is the frame payload.
	Payload []byte
	// IsBinary reports whether the opcode was binary.
	IsBinary bool
	// CreatedAt is when the proxy observed the message.
	CreatedAt time.Time
	// Metadata stores extension and proxy annotations.
	Metadata map[string]any
}

// WebSocketRepository persists WebSocket connections and messages.
type WebSocketRepository interface {
	InsertConnection(conn *WebSocketConnection) error
	UpdateConnection(conn *WebSocketConnection) error
	GetConnection(id uuid.UUID) (*WebSocketConnection, error)
	GetConnectionByRequestID(requestID uuid.UUID) (*WebSocketConnection, error)
	InsertMessage(msg *WebSocketMessage) error
	GetMessage(id uuid.UUID) (*WebSocketMessage, error)
	GetMessages(connectionID uuid.UUID) ([]*WebSocketMessage, error)
	CountMessages(connectionID uuid.UUID) (int, error)
	GetMessagesByRequestID(requestID uuid.UUID) ([]*WebSocketMessage, error)
}
