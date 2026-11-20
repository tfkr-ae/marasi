package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Stream message directions: the request is the client's, the response the
// origin's.
const (
	StreamDirectionRequest  = "request"
	StreamDirectionResponse = "response"
)

// ErrStreamMessageNotFound means no stored stream message has the id asked for.
var ErrStreamMessageNotFound = errors.New("stream message not found")

// StreamMessage is one stored stream message of a streamed pair (GLOSSARY:
// Stream message): one gRPC, gRPC-Web or Connect envelope, server-sent event
// or NDJSON line, in one direction.
type StreamMessage struct {
	// ID uniquely identifies the message. IDs are UUIDv7, so they sort in
	// the order the messages arrived.
	ID uuid.UUID
	// PairID is the request/response pair whose body carried the message.
	PairID uuid.UUID
	// Direction is StreamDirectionRequest or StreamDirectionResponse.
	Direction string
	// Framing is the direction's message framing: grpc, grpc-web,
	// connect, sse or ndjson.
	Framing string
	// Flags is the envelope's flags byte, nil for a framing without one.
	Flags *uint8
	// Payload is the message's payload, without its envelope.
	Payload []byte
	// Compressed reports that the payload was compressed on the wire.
	Compressed bool
	// CreatedAt is when the proxy observed the message.
	CreatedAt time.Time
	// Metadata holds proxy and extension annotations: too_large and
	// size_seen, cut_short, and later injected, intercepted, dropped and
	// edited.
	Metadata map[string]any
}

// StreamRepository persists stream messages. Deleting a pair deletes them.
type StreamRepository interface {
	InsertStreamMessage(message *StreamMessage) error
	// GetStreamMessage returns ErrStreamMessageNotFound for an unknown id.
	GetStreamMessage(id uuid.UUID) (*StreamMessage, error)
	// ListStreamMessages returns at most limit of the pair's messages in
	// the order they arrived, both directions together. A nil cursor
	// starts at the first; otherwise only messages after cursor are
	// included. The returned cursor is the last message's id when more
	// remain, nil otherwise. limit must be positive.
	ListStreamMessages(pairID uuid.UUID, cursor *uuid.UUID, limit int) ([]*StreamMessage, *uuid.UUID, error)
}
