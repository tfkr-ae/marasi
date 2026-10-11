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
	// Deflate is the negotiated permessage-deflate configuration, or nil when
	// the connection does not use deflate.
	Deflate *WebSocketDeflate
	// RemovedExtensions lists the WebSocket extensions the proxy removed from
	// the client's offer before forwarding it upstream.
	RemovedExtensions []string
}

// WebSocketDeflate is the permessage-deflate configuration (RFC 7692) that the
// upstream accepted and the client received for one WebSocket connection.
type WebSocketDeflate struct {
	// ServerNoContextTakeover reports that the upstream resets its compression
	// context after each message.
	ServerNoContextTakeover bool
	// ClientNoContextTakeover reports that the client resets its compression
	// context after each message.
	ClientNoContextTakeover bool
	// ServerMaxWindowBits is the upstream's LZ77 window size, 8 to 15.
	// It is 15 when the upstream did not limit it.
	ServerMaxWindowBits int
	// ClientMaxWindowBits is the client's LZ77 window size, 8 to 15.
	// It is 15 when the upstream did not limit it.
	ClientMaxWindowBits int
}

// WebSocketMessageProgress announces a message whose frames are still
// arriving. It is not stored; the whole message is, once complete.
type WebSocketMessageProgress struct {
	// MessageID is the ID the message gets once complete.
	MessageID uuid.UUID
	// ConnectionID is the connection that carries the message.
	ConnectionID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the connection.
	RequestID uuid.UUID
	// Direction is client or server.
	Direction string
	// Opcode is the message opcode.
	Opcode int
	// Bytes is how many payload bytes have arrived so far.
	Bytes int64
	// Frames is how many frames have started arriving so far.
	Frames int
}

// WebSocketMessage is a persisted WebSocket message snapshot: a whole text or
// binary message, or a single ping, pong, or close (ADR-0027).
type WebSocketMessage struct {
	// ID uniquely identifies the message.
	ID uuid.UUID
	// ConnectionID is the connection that carried the message.
	ConnectionID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the connection.
	RequestID uuid.UUID
	// Direction is client or server.
	Direction string
	// Opcode is the message opcode: the opcode of its first frame.
	Opcode int
	// FrameCount is how many frames carried the message on the wire.
	FrameCount int
	// Compressed reports that the message was compressed with
	// permessage-deflate on the wire. Payload is always uncompressed.
	Compressed bool
	// Payload is the whole message payload.
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
	// ListConnections returns at most limit connections in descending UUID order.
	// A nil cursor starts the first page. Otherwise, only UUIDs less than cursor are included.
	// The returned cursor identifies the next page and is nil when no more rows remain.
	// limit must be positive.
	ListConnections(cursor *uuid.UUID, limit int) ([]*WebSocketConnection, *uuid.UUID, error)
	// ListMessages returns at most limit messages for connectionID in descending UUID order.
	// A nil cursor starts the first page. Otherwise, only UUIDs less than cursor are included.
	// The returned cursor identifies the next page and is nil when no more rows remain.
	// limit must be positive.
	ListMessages(connectionID uuid.UUID, cursor *uuid.UUID, limit int) ([]*WebSocketMessage, *uuid.UUID, error)
	InsertMessage(msg *WebSocketMessage) error
	GetMessage(id uuid.UUID) (*WebSocketMessage, error)
	GetMessages(connectionID uuid.UUID) ([]*WebSocketMessage, error)
	CountMessages(connectionID uuid.UUID) (int, error)
	GetMessagesByRequestID(requestID uuid.UUID) ([]*WebSocketMessage, error)
}
