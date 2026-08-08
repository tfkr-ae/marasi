package websocket

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math"
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
)

// MaxPayload is the largest accepted WebSocket payload size.
const MaxPayload = 64 * 1024 * 1024

// Frame is a single WebSocket protocol frame.
type Frame struct {
	// Fin indicates whether this is the final fragment of a message.
	Fin bool
	// Opcode is the frame opcode.
	Opcode int
	// Masked indicates whether the payload is masked.
	Masked bool
	// Key is the masking key when Masked is true.
	Key [4]byte
	// Payload is the unmasked frame payload.
	Payload []byte
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

// ReadFrame reads and decodes one WebSocket frame from r.
func ReadFrame(r io.Reader) (Frame, error) {
	var header [2]byte

	_, err := io.ReadFull(r, header[:])
	if err != nil {
		return Frame{}, fmt.Errorf("reading header bytes : %w", err)
	}

	fin := header[0]&0x80 != 0
	opcode := int(header[0] & 0x0f)
	masked := header[1]&0x80 != 0
	payloadLength := int64(header[1] & 0x7f)

	switch payloadLength {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return Frame{}, fmt.Errorf("reading 16-bit extended payload length : %w", err)
		}
		payloadLength = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return Frame{}, fmt.Errorf("reading 64-bit extended payload length : %w", err)
		}
		rawLength := binary.BigEndian.Uint64(extended[:])
		if rawLength > math.MaxInt64 {
			return Frame{}, fmt.Errorf("invalid 64-bit payload length: %d", rawLength)
		}
		payloadLength = int64(rawLength)
	}

	if payloadLength > MaxPayload {
		return Frame{}, fmt.Errorf("websocket payload too large: %d", payloadLength)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return Frame{}, fmt.Errorf("reading mask key : %w", err)
		}
	}

	payload := make([]byte, payloadLength)
	if payloadLength > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("reading payload : %w", err)
		}
	}

	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return Frame{
		Fin:     fin,
		Opcode:  opcode,
		Masked:  masked,
		Key:     maskKey,
		Payload: payload,
	}, nil
}

// WriteFrame encodes and writes frame to w.
// When mask is true, a random masking key is generated and applied.
func WriteFrame(w io.Writer, frame Frame, mask bool) error {
	payloadLength := len(frame.Payload)
	if payloadLength > MaxPayload {
		return fmt.Errorf("payload too large: %d", payloadLength)
	}

	firstHeaderByte := byte(frame.Opcode & 0x0f)
	if frame.Fin {
		firstHeaderByte |= 0x80
	}

	var header []byte
	header = append(header, firstHeaderByte)

	secondHeaderByte := byte(0)
	if mask {
		secondHeaderByte |= 0x80
	}

	switch {
	case payloadLength <= 125:
		secondHeaderByte |= byte(payloadLength)
		header = append(header, secondHeaderByte)
	case payloadLength <= math.MaxUint16:
		secondHeaderByte |= 126
		header = append(header, secondHeaderByte)

		var extendedLength [2]byte
		binary.BigEndian.PutUint16(extendedLength[:], uint16(payloadLength))
		header = append(header, extendedLength[:]...)
	default:
		secondHeaderByte |= 127
		header = append(header, secondHeaderByte)

		var extendedLength [8]byte
		binary.BigEndian.PutUint64(extendedLength[:], uint64(payloadLength))
		header = append(header, extendedLength[:]...)
	}

	_, err := w.Write(header)
	if err != nil {
		return fmt.Errorf("writing frame header : %w", err)
	}

	payload := frame.Payload

	if mask {
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return fmt.Errorf("generating masking key: %w", err)
		}

		if _, err := w.Write(key[:]); err != nil {
			return fmt.Errorf("writing masking key: %w", err)
		}

		maskedPayload := make([]byte, len(payload))
		for i := range payload {
			maskedPayload[i] = payload[i] ^ key[i%4]
		}

		payload = maskedPayload
	}

	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("writing frame payload: %w", err)
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
