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
	// Payload is the message without its envelope, decoded when it is
	// compressed with a Decodable encoding, as it is on the wire otherwise.
	Payload []byte
	// Encoding is the direction's per-message encoding, from the head
	// field EncodingHeader names; "" when the head names none.
	Encoding string
	// ReadOnly means the payload is compressed with an encoding the proxy
	// does not decode, so it is shown as it came and a change to it is
	// refused.
	ReadOnly bool
	// Metadata holds extension annotations, keyed by extension name.
	Metadata map[string]any
	// Dropped means the message is not forwarded.
	Dropped bool
	// Skipped means later extensions do not see the message.
	Skipped bool
}

// Compressed reports whether the envelope's compressed flag is set: the
// message is compressed on the wire. Payload is decoded unless ReadOnly.
func (m *LiveMessage) Compressed() bool {
	return Compressed(m.Framing, m.Flags)
}

// Compressed reports whether an envelope with flags, of framing, carries a
// compressed message.
func Compressed(framing Framing, flags byte) bool {
	return framing.Enveloped() && flags&flagCompressed != 0
}

// flagTrailers is the envelope flag bit of a gRPC-Web trailers frame.
const flagTrailers = 0x80

// IsTrailersFrame reports whether a message with flags is a gRPC-Web
// trailers frame, whose payload is the call's trailer block.
func IsTrailersFrame(framing Framing, flags byte) bool {
	return framing == FramingGRPCWeb && flags&flagTrailers != 0
}

// Uncompressed is flags with the compressed flag cleared: the flags of an
// edited message, which goes out uncompressed.
func Uncompressed(flags byte) byte {
	return flags &^ flagCompressed
}

// Text is the payload in its text form (TextForm), and false when the
// framing and codec have none or the payload is compressed with an encoding
// the proxy does not decode.
func (m *LiveMessage) Text() (string, bool) {
	if m.ReadOnly {
		return "", false
	}
	return TextForm(m.ContentType, m.Payload)
}

// SetText replaces the payload with what text encodes to (FromText). Text
// that cannot be encoded is refused, and the payload is unchanged.
func (m *LiveMessage) SetText(text string) error {
	payload, err := FromText(m.ContentType, text)
	if err != nil {
		return err
	}
	m.Payload = payload
	return nil
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
