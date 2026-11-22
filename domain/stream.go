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
	// RecordStreamedPair stores how far the pair's streamed directions
	// have gone, so traffic lists and queries see a streamed pair while it
	// is live. A direction left nil, and an ending left "", keep what is
	// stored. A pair that no longer exists is not an error.
	RecordStreamedPair(record *StreamedPairRecord) error
}

// StreamedState is a streamed pair's framing per streamed direction, and
// each direction's ending once it ended. A nil direction is not streamed,
// or not yet known to be: a response direction is known once its head is
// admitted.
type StreamedState struct {
	Request  *StreamedDirectionState
	Response *StreamedDirectionState
}

// StreamedDirectionState is one streamed direction's framing and ending.
type StreamedDirectionState struct {
	// Framing is the direction's message framing: grpc, grpc-web,
	// connect, sse or ndjson.
	Framing string
	// Ending is complete, cancelled, reset or cut short once the
	// direction ended, and "" while it goes on (or when the instance
	// stopped before it ended).
	Ending string
}

// Direction returns the state of direction name, nil when it is not
// streamed.
func (s StreamedState) Direction(name string) *StreamedDirectionState {
	switch name {
	case StreamDirectionRequest:
		return s.Request
	case StreamDirectionResponse:
		return s.Response
	}
	return nil
}

// StreamedPairRecord is a DB write of a pair's StreamedState.
type StreamedPairRecord struct {
	PairID uuid.UUID
	State  StreamedState
}

// StreamedPairUpdated announces a direction of a started streamed pair
// that became known after it started, such as a gRPC call's response once
// its head is admitted. It is not stored; the pair's StreamedState is.
type StreamedPairUpdated struct {
	PairID uuid.UUID
	// Framings maps each direction known to be streamed to its framing,
	// the new one included.
	Framings map[string]string
}

// StreamedPairStarted announces a streamed pair once the first head naming
// a message framing is read. It is not stored.
type StreamedPairStarted struct {
	PairID uuid.UUID
	// Framings maps each direction known to be streamed when the pair
	// started to its framing. A direction recognised later is named on
	// its messages and in StreamedPairEnded.
	Framings map[string]string
}

// StreamMessageProgress announces a stream message still arriving. It is
// not stored; the whole message is, once it arrives.
type StreamMessageProgress struct {
	PairID uuid.UUID
	// Direction is StreamDirectionRequest or StreamDirectionResponse.
	Direction string
	Framing   string
	// Bytes is how many bytes of the message, its envelope included,
	// have arrived so far.
	Bytes int64
}

// StreamTrailer is one trailer field a streamed direction ended with.
type StreamTrailer struct {
	Name  string
	Value string
}

// StreamedDirection is how one direction of a streamed pair went.
type StreamedDirection struct {
	Framing  string
	Messages int
	// Ending is complete, cancelled, reset or cut short.
	Ending string
	// ResetCode is the RST_STREAM code of a reset direction.
	ResetCode string
	Trailers  []StreamTrailer
}

// StreamedPairEnded announces a streamed pair whose directions all ended.
// It is not stored; the pair's response metadata keeps the same record.
type StreamedPairEnded struct {
	PairID uuid.UUID
	// Directions maps each streamed direction to how it went.
	Directions map[string]StreamedDirection
}
