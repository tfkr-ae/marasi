package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	// CheckpointTypeRequest is a held HTTP request.
	CheckpointTypeRequest = "request"
	// CheckpointTypeResponse is a held HTTP response.
	CheckpointTypeResponse = "response"
	// CheckpointTypeWebSocket is a held WebSocket message.
	CheckpointTypeWebSocket = "websocket"
	// CheckpointTypeStream is a held stream message of a streamed pair.
	CheckpointTypeStream = "stream"
	// CheckpointTypeTrailer is the held trailer block of a streamed pair's
	// direction: its HTTP/2 trailers, or a gRPC-Web trailers frame.
	CheckpointTypeTrailer = "trailer"
)

// CheckpointItem is an in-flight request, response, WebSocket message,
// stream message or trailer block held by Checkpoint.
type CheckpointItem struct {
	// ID is the request/response pair UUID, or the WebSocket message,
	// stream message or trailer block UUID.
	ID uuid.UUID
	// Type is request, response, websocket, stream or trailer.
	Type string
	// Raw is the dumped HTTP request or response bytes, or a trailer
	// block: one "name: value" line per field, each ending in CRLF.
	Raw []byte
	// Payload is the WebSocket frame payload, or a stream message's payload
	// without its envelope, as it is on the wire.
	Payload []byte
	// Opcode is the WebSocket frame opcode.
	Opcode int
	// Direction is the WebSocket frame direction, or a stream message's or
	// trailer block's: request or response.
	Direction string
	// PairID is the streamed pair that carries a stream message or trailer
	// block.
	PairID uuid.UUID
	// Framing is a stream message's or trailer block's message framing,
	// such as grpc.
	Framing string
	// Flags is a stream message's envelope flags byte, nil for a framing
	// without one.
	Flags *uint8
	// Text is a stream message's payload in its text form, for a framing
	// and codec that has one (stream.TextForm); HasText reports it.
	Text    string
	HasText bool
	// ConnectionID is the WebSocket connection that carried the frame.
	ConnectionID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the WebSocket connection.
	RequestID uuid.UUID
	// FrameCount is how many frames carried the WebSocket message on the wire.
	FrameCount int
	// Compressed reports that the WebSocket message was compressed with
	// permessage-deflate on the wire, in which case Payload is uncompressed;
	// for a stream message, that its envelope's compressed flag is set.
	Compressed bool
	// Metadata is the WebSocket or stream message's extension and proxy
	// annotations.
	Metadata map[string]any
	// InterceptedAt is when Checkpoint held the item. A held response has its
	// own time, though it shares its request's id.
	InterceptedAt time.Time
}
