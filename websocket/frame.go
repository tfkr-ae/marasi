package websocket

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	// OpContinuation is the continuation frame opcode.
	OpContinuation = 0x0
	// OpText is the text data frame opcode.
	OpText = 0x1
	// OpBinary is the binary data frame opcode.
	OpBinary = 0x2
	// OpClose is the connection close control opcode.
	OpClose = 0x8
	// OpPing is the ping control opcode.
	OpPing = 0x9
	// OpPong is the pong control opcode.
	OpPong = 0xA
	// CloseNormalClosure indicates that the connection fulfilled its purpose.
	CloseNormalClosure = 1000
	// CloseGoingAway indicates that the endpoint is shutting down.
	CloseGoingAway = 1001
	// CloseNoStatusReceived is used when a close frame has no status code.
	CloseNoStatusReceived = 1005
	// CloseInvalidPayload indicates message data that does not fit the
	// message type, such as compressed data that cannot be inflated.
	CloseInvalidPayload = 1007
	// CloseMessageTooBig indicates a message too big to process.
	CloseMessageTooBig = 1009
)

// MaxPayload is the largest frame payload ReadFrame accepts. The relay does
// not use it: its limit is per message and configurable.
const MaxPayload = 64 * 1024 * 1024

// Frame is a single WebSocket protocol frame.
type Frame struct {
	// Fin indicates whether this is the final fragment of a message.
	Fin bool
	// Rsv1 is the first reserved bit. Without a negotiated extension it is a
	// protocol violation, which the relay forwards for the peer to judge.
	Rsv1 bool
	// Rsv2 is the second reserved bit.
	Rsv2 bool
	// Rsv3 is the third reserved bit.
	Rsv3 bool
	// Opcode is the frame opcode.
	Opcode int
	// Masked indicates whether the payload is masked.
	Masked bool
	// Key is the masking key when Masked is true.
	Key [4]byte
	// Payload is the unmasked frame payload.
	Payload []byte

	// deflate marks an uncompressed payload that the leg compresses for its
	// peer as it writes the frame, setting RSV1 when it does (docs/adr/2026-10-11-proxy-terminates-permessage-deflate-per-leg.md).
	deflate bool
}

// IsControl reports whether the frame is a control frame.
func (f *Frame) IsControl() bool {
	return f.Opcode&0x8 != 0
}

// IsData reports whether the frame is a data frame.
func (f Frame) IsData() bool {
	return f.Opcode == OpText ||
		f.Opcode == OpBinary ||
		f.Opcode == OpContinuation
}

// ReadFrame reads and decodes one WebSocket frame from r. It rejects a frame
// longer than MaxPayload.
func ReadFrame(r io.Reader) (Frame, error) {
	frame, length, err := readFrameHeader(r)
	if err != nil {
		return Frame{}, err
	}

	if length > MaxPayload {
		return Frame{}, fmt.Errorf("websocket payload too large: %d", length)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("reading payload : %w", err)
		}
	}
	if frame.Masked {
		maskBytes(payload, frame.Key, 0)
	}
	frame.Payload = payload

	return frame, nil
}

// readFrameHeader reads one frame up to its payload. It returns the frame
// with no payload and the payload length.
func readFrameHeader(r io.Reader) (Frame, int64, error) {
	var header [2]byte

	_, err := io.ReadFull(r, header[:])
	if err != nil {
		return Frame{}, 0, fmt.Errorf("reading header bytes : %w", err)
	}

	frame := Frame{
		Fin:    header[0]&0x80 != 0,
		Rsv1:   header[0]&0x40 != 0,
		Rsv2:   header[0]&0x20 != 0,
		Rsv3:   header[0]&0x10 != 0,
		Opcode: int(header[0] & 0x0f),
		Masked: header[1]&0x80 != 0,
	}
	payloadLength := int64(header[1] & 0x7f)

	switch payloadLength {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return Frame{}, 0, fmt.Errorf("reading 16-bit extended payload length : %w", err)
		}
		payloadLength = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return Frame{}, 0, fmt.Errorf("reading 64-bit extended payload length : %w", err)
		}
		rawLength := binary.BigEndian.Uint64(extended[:])
		if rawLength > math.MaxInt64 {
			return Frame{}, 0, fmt.Errorf("invalid 64-bit payload length: %d", rawLength)
		}
		payloadLength = int64(rawLength)
	}

	if frame.Masked {
		if _, err := io.ReadFull(r, frame.Key[:]); err != nil {
			return Frame{}, 0, fmt.Errorf("reading mask key : %w", err)
		}
	}

	return frame, payloadLength, nil
}

// maskBytes applies key to p in place. offset is the position of p[0] in the
// frame payload.
func maskBytes(p []byte, key [4]byte, offset int64) {
	for i := range p {
		p[i] ^= key[(offset+int64(i))%4]
	}
}

// newMaskKey returns a random masking key.
func newMaskKey() ([4]byte, error) {
	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, fmt.Errorf("generating masking key: %w", err)
	}
	return key, nil
}

// appendFrameHeader appends the header of frame, with a payload of length
// bytes, to dst. When mask is true it sets the mask bit and appends key.
func appendFrameHeader(dst []byte, frame Frame, length int64, mask bool, key [4]byte) []byte {
	firstHeaderByte := byte(frame.Opcode & 0x0f)
	if frame.Fin {
		firstHeaderByte |= 0x80
	}
	if frame.Rsv1 {
		firstHeaderByte |= 0x40
	}
	if frame.Rsv2 {
		firstHeaderByte |= 0x20
	}
	if frame.Rsv3 {
		firstHeaderByte |= 0x10
	}
	dst = append(dst, firstHeaderByte)

	secondHeaderByte := byte(0)
	if mask {
		secondHeaderByte |= 0x80
	}

	switch {
	case length <= 125:
		dst = append(dst, secondHeaderByte|byte(length))
	case length <= math.MaxUint16:
		dst = append(dst, secondHeaderByte|126)
		dst = binary.BigEndian.AppendUint16(dst, uint16(length))
	default:
		dst = append(dst, secondHeaderByte|127)
		dst = binary.BigEndian.AppendUint64(dst, uint64(length))
	}

	if mask {
		dst = append(dst, key[:]...)
	}
	return dst
}

// WriteFrame encodes frame and writes it to w in one call.
// When mask is true, a random masking key is generated and applied.
func WriteFrame(w io.Writer, frame Frame, mask bool) error {
	var key [4]byte
	if mask {
		var err error
		if key, err = newMaskKey(); err != nil {
			return err
		}
	}

	length := int64(len(frame.Payload))
	encoded := appendFrameHeader(make([]byte, 0, 14+length), frame, length, mask, key)
	headerLength := len(encoded)
	encoded = append(encoded, frame.Payload...)
	if mask {
		maskBytes(encoded[headerLength:], key, 0)
	}

	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("writing frame : %w", err)
	}

	return nil
}

// ParseClosePayload parses a close frame payload into a status code and reason.
func ParseClosePayload(payload []byte) (code int, reason string, err error) {
	switch len(payload) {
	case 0:
		return CloseNoStatusReceived, "", nil
	case 1:
		return 0, "", fmt.Errorf("close payload contains only one byte")
	}

	code = int(binary.BigEndian.Uint16(payload[:2]))
	if !isValidCloseCode(code) {
		return code, "", fmt.Errorf("invalid close code: %d", code)
	}

	reasonBytes := payload[2:]
	if !utf8.Valid(reasonBytes) {
		return code, "", fmt.Errorf("close reason is not valid UTF-8")
	}

	return code, string(reasonBytes), nil
}

// closeDetailsSeen reads the code and reason from any close payload, valid or
// not. A one-byte payload has no code (0); an invalid UTF-8 reason has its bad
// bytes replaced so it can be stored.
func closeDetailsSeen(payload []byte) (code int, reason string) {
	switch len(payload) {
	case 0:
		return CloseNoStatusReceived, ""
	case 1:
		return 0, ""
	}
	code = int(binary.BigEndian.Uint16(payload[:2]))
	return code, strings.ToValidUTF8(string(payload[2:]), "\uFFFD")
}

// EncodeClosePayload encodes a close status code and reason into a payload.
func EncodeClosePayload(code int, reason string) ([]byte, error) {
	if !isValidCloseCode(code) {
		return nil, fmt.Errorf("invalid close code: %d", code)
	}

	if !utf8.ValidString(reason) {
		return nil, fmt.Errorf("close reason is not valid UTF-8")
	}

	if len(reason) > 123 {
		return nil, fmt.Errorf("close reason too long: %d bytes", len(reason))
	}

	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], uint16(code))
	copy(payload[2:], reason)

	return payload, nil
}

func isValidCloseCode(code int) bool {
	switch code {
	case 1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014:
		return true
	default:
		return code >= 3000 && code <= 4999
	}
}
