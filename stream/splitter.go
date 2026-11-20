package stream

import (
	"encoding/binary"
	"fmt"
)

// Message is one stream message as a Splitter cut it.
type Message struct {
	// Raw is the message as it came, its envelope included: an unedited
	// message goes out as these bytes.
	Raw []byte
	// Flags is the envelope's flags byte, for the framings that have one.
	Flags byte
	// Payload is Raw without its envelope. It shares Raw's bytes.
	Payload []byte
}

// TooLargeError is a message over the per-message limit. Size is the size it
// declared, or the bytes seen when the framing declares none.
type TooLargeError struct {
	Size  int64
	Limit int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("the stream message is %d bytes, over the %d-byte limit", e.Size, e.Limit)
}

// envelopeLength is the gRPC, gRPC-Web and Connect envelope prefix: a flags
// byte and a big-endian payload length.
const envelopeLength = 5

// Splitter cuts one direction's body into stream messages. It holds at most
// one message, so a reader that feeds it only after forwarding the last
// message it handed back holds at most one message in memory.
type Splitter struct {
	framing Framing
	limit   int64
	buf     []byte
	// want is the whole length of the message being read, its envelope
	// included, once its prefix arrived; 0 before.
	want int
}

// NewSplitter is a splitter for framing, refusing a message whose payload
// is over limit bytes (DefaultMaxMessageSize when limit is not positive).
// It splits the envelope framings: gRPC, gRPC-Web and Connect.
func NewSplitter(framing Framing, limit int64) (*Splitter, error) {
	switch framing {
	case FramingGRPC, FramingGRPCWeb, FramingConnect:
	default:
		return nil, fmt.Errorf("the %q message framing cannot be split", framing)
	}
	if limit <= 0 {
		limit = DefaultMaxMessageSize
	}
	return &Splitter{framing: framing, limit: limit}, nil
}

// Feed consumes bytes of p until a message is whole or p runs out, and
// returns how many it consumed and the message, nil when none is whole yet.
// It never consumes past the end of a message, so the bytes after it stay
// with the caller. A message over the limit is a *TooLargeError, returned
// once its envelope's prefix is read; the splitter is done then.
func (s *Splitter) Feed(p []byte) (int, *Message, error) {
	consumed := 0
	if s.want == 0 {
		n := min(envelopeLength-len(s.buf), len(p))
		s.buf = append(s.buf, p[:n]...)
		consumed, p = n, p[n:]
		if len(s.buf) < envelopeLength {
			return consumed, nil, nil
		}
		length := int64(binary.BigEndian.Uint32(s.buf[1:envelopeLength]))
		if length > s.limit {
			return consumed, nil, &TooLargeError{Size: length, Limit: s.limit}
		}
		s.want = envelopeLength + int(length)
	}
	n := min(s.want-len(s.buf), len(p))
	s.buf = append(s.buf, p[:n]...)
	consumed += n
	if len(s.buf) < s.want {
		return consumed, nil, nil
	}
	message := s.message()
	s.buf, s.want = nil, 0
	return consumed, message, nil
}

// Partial is what arrived of a message that is not whole, nil when none
// started: when a body ends there, the message is cut short.
func (s *Splitter) Partial() *Message {
	if len(s.buf) == 0 {
		return nil
	}
	return s.message()
}

// Buffered is how many bytes of the message being read the splitter holds.
func (s *Splitter) Buffered() int { return len(s.buf) }

func (s *Splitter) message() *Message {
	m := &Message{Raw: s.buf, Payload: []byte{}}
	if len(s.buf) > 0 {
		m.Flags = s.buf[0]
	}
	if len(s.buf) > envelopeLength {
		m.Payload = s.buf[envelopeLength:]
	}
	return m
}
