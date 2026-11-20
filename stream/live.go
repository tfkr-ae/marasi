package stream

import (
	"encoding/binary"

	"github.com/google/uuid"
)

// LiveMessage is a stream message on its way through the proxy, between
// being read and being forwarded: what the processStreamMessage hooks see
// and change.
type LiveMessage struct {
	// ID is the message's UUID, the one it is stored with.
	ID uuid.UUID
	// PairID is the request/response pair whose body carries the message.
	PairID uuid.UUID
	// Direction is "request" or "response".
	Direction string
	// Framing is the direction's message framing.
	Framing Framing
	// ContentType is the content type of the direction's head, which
	// names the codec the text form is read with.
	ContentType string
	// Flags is the envelope's flags byte.
	Flags byte
	// Payload is the message without its envelope, as it is on the wire.
	Payload []byte
	// Metadata holds extension annotations, keyed by extension name.
	Metadata map[string]any
	// Dropped means the message is not forwarded.
	Dropped bool
	// Skipped means later extensions do not see the message.
	Skipped bool
}

// Compressed reports whether the envelope's compressed flag is set: the
// payload is compressed on the wire.
func (m *LiveMessage) Compressed() bool {
	switch m.Framing {
	case FramingGRPC, FramingGRPCWeb, FramingConnect:
		return m.Flags&1 != 0
	}
	return false
}

// Text is the payload in its text form (TextForm), and false when the
// framing and codec have none.
func (m *LiveMessage) Text() (string, bool) {
	return TextForm(m.ContentType, m.Payload)
}

// Envelope is payload in the gRPC, gRPC-Web and Connect envelope: the flags
// byte and the big-endian payload length, then the payload.
func Envelope(flags byte, payload []byte) []byte {
	raw := make([]byte, envelopeLength+len(payload))
	raw[0] = flags
	binary.BigEndian.PutUint32(raw[1:envelopeLength], uint32(len(payload)))
	copy(raw[envelopeLength:], payload)
	return raw
}
