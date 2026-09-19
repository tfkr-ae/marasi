package domain

import "github.com/google/uuid"

const (
	// CheckpointTypeRequest is a held HTTP request.
	CheckpointTypeRequest = "request"
	// CheckpointTypeResponse is a held HTTP response.
	CheckpointTypeResponse = "response"
	// CheckpointTypeWebSocket is a held WebSocket message.
	CheckpointTypeWebSocket = "websocket"
)

// CheckpointItem is an in-flight request, response, or WebSocket message held by Checkpoint.
type CheckpointItem struct {
	// ID is the request/response pair UUID or the WebSocket message UUID.
	ID uuid.UUID
	// Type is request, response, or websocket.
	Type string
	// Raw is the dumped HTTP request or response bytes.
	Raw []byte
	// Payload is the WebSocket frame payload.
	Payload []byte
	// Opcode is the WebSocket frame opcode.
	Opcode int
	// Direction is the WebSocket frame direction.
	Direction string
	// ConnectionID is the WebSocket connection that carried the frame.
	ConnectionID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the WebSocket connection.
	RequestID uuid.UUID
}
