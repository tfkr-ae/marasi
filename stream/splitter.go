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
// declared, or, for server-sent events and NDJSON, which declare none, the
// bytes seen: one past the limit.
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
	// For server-sent events: whether the bytes so far end a line (or none
	// arrived), and whether the last byte was a CR, which a LF may follow
	// in the same line ending; crBlank is that the CR ended a blank line,
	// so the event is whole whether or not a LF follows.
	lineStart, pendingCR, crBlank bool
}

// NewSplitter is a splitter for framing, refusing a message whose payload
// is over limit bytes (DefaultMaxMessageSize when limit is not positive).
// It splits every message framing:
//   - gRPC, gRPC-Web and Connect into 5-byte envelopes, a gRPC-Web trailers
//     frame (flag 0x80) and a Connect end-of-stream envelope (flag 0x02)
//     included;
//   - server-sent events into events, each ending at a blank line, its
//     lines ending in CRLF, LF or CR (WHATWG HTML §9.2.5);
//   - NDJSON into lines, each with its LF.
func NewSplitter(framing Framing, limit int64) (*Splitter, error) {
	switch framing {
	case FramingGRPC, FramingGRPCWeb, FramingConnect, FramingSSE, FramingNDJSON:
	default:
		return nil, fmt.Errorf("the %q message framing cannot be split", framing)
	}
	if limit <= 0 {
		limit = DefaultMaxMessageSize
	}
	return &Splitter{framing: framing, limit: limit, lineStart: true}, nil
}

// Feed consumes bytes of p until a message is whole or p runs out, and
// returns how many it consumed and the message, nil when none is whole yet.
// It never consumes past the end of a message, so the bytes after it stay
// with the caller. A message over the limit is a *TooLargeError, returned
// once its envelope's prefix is read, or once a server-sent event or line
// passes the limit; the splitter is done then.
func (s *Splitter) Feed(p []byte) (int, *Message, error) {
	switch s.framing {
	case FramingSSE:
		return s.feedEvent(p)
	case FramingNDJSON:
		return s.feedLine(p)
	}
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

// feedEvent reads a server-sent event up to the blank line that ends it.
// A blank line ended by a CR is whole at once when the byte after it has
// arrived, and when it has not, once it does or the body ends (Flush): a
// LF after it belongs to the same line ending.
func (s *Splitter) feedEvent(p []byte) (int, *Message, error) {
	for i, b := range p {
		if s.pendingCR {
			s.pendingCR = false
			if s.crBlank {
				if b == '\n' {
					s.buf = append(s.buf, b)
					return i + 1, s.take(), nil
				}
				return i, s.take(), nil
			}
			if b == '\n' {
				if err := s.add(b); err != nil {
					return i + 1, nil, err
				}
				continue
			}
		}
		if err := s.add(b); err != nil {
			return i + 1, nil, err
		}
		switch b {
		case '\n':
			if s.lineStart {
				return i + 1, s.take(), nil
			}
			s.lineStart = true
		case '\r':
			s.pendingCR, s.crBlank, s.lineStart = true, s.lineStart, true
		default:
			s.lineStart = false
		}
	}
	return len(p), nil, nil
}

// feedLine reads an NDJSON line up to its LF.
func (s *Splitter) feedLine(p []byte) (int, *Message, error) {
	for i, b := range p {
		if err := s.add(b); err != nil {
			return i + 1, nil, err
		}
		if b == '\n' {
			return i + 1, s.take(), nil
		}
	}
	return len(p), nil, nil
}

// add appends b to a message whose framing declares no length, refusing it
// once it passes the limit.
func (s *Splitter) add(b byte) error {
	s.buf = append(s.buf, b)
	if int64(len(s.buf)) > s.limit {
		return &TooLargeError{Size: int64(len(s.buf)), Limit: s.limit}
	}
	return nil
}

// take hands back the message read and starts the next.
func (s *Splitter) take() *Message {
	message := s.message()
	s.buf, s.want = nil, 0
	s.lineStart, s.pendingCR, s.crBlank = true, false, false
	return message
}

// Flush is the message the end of the body makes whole, nil when it makes
// none: an NDJSON line with no LF, or a server-sent event whose blank line
// ended in a CR as the body's last byte. Call it once the body ended,
// before Partial.
func (s *Splitter) Flush() *Message {
	switch {
	case len(s.buf) == 0:
		return nil
	case s.framing == FramingNDJSON, s.framing == FramingSSE && s.pendingCR && s.crBlank:
		return s.take()
	}
	return nil
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
	if s.framing == FramingSSE || s.framing == FramingNDJSON {
		return &Message{Raw: s.buf, Payload: s.buf}
	}
	m := &Message{Raw: s.buf, Payload: []byte{}}
	if len(s.buf) > 0 {
		m.Flags = s.buf[0]
	}
	if len(s.buf) > envelopeLength {
		m.Payload = s.buf[envelopeLength:]
	}
	return m
}
