package domain

import (
	"time"

	"github.com/google/uuid"
)

type WebSocketConnection struct {
	ID          uuid.UUID
	RequestID   uuid.UUID
	State       string
	Transport   string
	Host        string
	Path        string
	StartedAt   time.Time
	ClosedAt    *time.Time
	CloseCode   int
	CloseReason string
}
type WebSocketMessage struct {
	ID           uuid.UUID
	ConnectionID uuid.UUID
	Direction    string
	Opcode       int
	Fin          bool
	Payload      []byte
	IsBinary     bool
	CreatedAt    time.Time
	Metadata     map[string]any
}

type WebSocketRepository interface {
	// Connections
	InsertConnection(conn *WebSocketConnection) error
	UpdateConnection(conn *WebSocketConnection) error
	GetConnection(id uuid.UUID) (*WebSocketConnection, error)
	GetConnectionByRequestID(requestID uuid.UUID) (*WebSocketConnection, error)
	//Messages
	InsertMessage(msg *WebSocketMessage) error
	GetMessage(id uuid.UUID) (*WebSocketMessage, error)
	GetMessages(connectionID uuid.UUID) ([]*WebSocketMessage, error)
	CountMessages(connectionID uuid.UUID) (int, error)
	GetMessagesByRequestID(requestID uuid.UUID) ([]*WebSocketMessage, error)
}
