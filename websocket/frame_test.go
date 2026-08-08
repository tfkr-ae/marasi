package websocket

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestFrame_IsControl(t *testing.T) {
	tests := []struct {
		name   string
		opcode int
		want   bool
	}{
		{name: "continuation", opcode: OpContinuation, want: false},
		{name: "text", opcode: OpText, want: false},
		{name: "binary", opcode: OpBinary, want: false},
		{name: "reserved data 0x3", opcode: 0x3, want: false},
		{name: "reserved data 0x4", opcode: 0x4, want: false},
		{name: "close", opcode: OpClose, want: true},
		{name: "ping", opcode: OpPing, want: true},
		{name: "pong", opcode: OpPong, want: true},
		{name: "reserved control 0xB", opcode: 0xB, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Frame{Opcode: tt.opcode}
			got := f.IsControl()
			if got != tt.want {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}

func TestFrame_IsData(t *testing.T) {
	tests := []struct {
		name   string
		opcode int
		want   bool
	}{
		{name: "continuation", opcode: OpContinuation, want: true},
		{name: "text", opcode: OpText, want: true},
		{name: "binary", opcode: OpBinary, want: true},
		{name: "reserved data 0x3", opcode: 0x3, want: false},
		{name: "close", opcode: OpClose, want: false},
		{name: "ping", opcode: OpPing, want: false},
		{name: "pong", opcode: OpPong, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Frame{Opcode: tt.opcode}
			got := f.IsData()
			if got != tt.want {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}

func TestFrame_ReadFrame(t *testing.T) {
	payload126 := bytes.Repeat([]byte{'a'}, 126)
	frame126 := append(
		[]byte{
			0x82, // FIN + binary
			126,  // 16-bit extended length follows
			0x00, 0x7e,
		},
		payload126...,
	)

	payload65536 := bytes.Repeat([]byte{'b'}, 65536)
	frame65536 := append(
		[]byte{
			0x82, // FIN + binary
			127,  // 64-bit extended length follows
			0x00, 0x00, 0x00, 0x00,
			0x00, 0x01, 0x00, 0x00,
		},
		payload65536...,
	)

	oversized := make([]byte, 10)
	oversized[0] = 0x82
	oversized[1] = 127
	binary.BigEndian.PutUint64(oversized[2:], uint64(MaxPayload)+1)

	tests := []struct {
		name    string
		input   []byte
		want    Frame
		wantErr string
	}{
		{
			name: "unmasked text",
			input: []byte{
				0x81,
				0x05,
				'H', 'e', 'l', 'l', 'o',
			},
			want: Frame{
				Fin:     true,
				Opcode:  OpText,
				Masked:  false,
				Payload: []byte("Hello"),
			},
		},
		{
			name: "masked text",
			input: []byte{
				0x81,
				0x80 | 0x05,
				0x37, 0xfa, 0x21, 0x3d,
				0x7f, 0x9f, 0x4d, 0x51, 0x58,
			},
			want: Frame{
				Fin:     true,
				Opcode:  OpText,
				Masked:  true,
				Key:     [4]byte{0x37, 0xfa, 0x21, 0x3d},
				Payload: []byte("Hello"),
			},
		},
		{
			name:  "empty payload",
			input: []byte{0x89, 0x00},
			want: Frame{
				Fin:     true,
				Opcode:  OpPing,
				Masked:  false,
				Payload: []byte{},
			},
		},
		{
			name: "unfinished fragmented frame",
			input: []byte{
				0x01,
				0x02,
				'H', 'i',
			},
			want: Frame{
				Fin:     false,
				Opcode:  OpText,
				Masked:  false,
				Payload: []byte("Hi"),
			},
		},
		{
			name:  "16-bit extended payload length",
			input: frame126,
			want: Frame{
				Fin:     true,
				Opcode:  OpBinary,
				Masked:  false,
				Payload: payload126,
			},
		},
		{
			name:  "64-bit extended payload length",
			input: frame65536,
			want: Frame{
				Fin:     true,
				Opcode:  OpBinary,
				Masked:  false,
				Payload: payload65536,
			},
		},
		{
			name:    "payload exceeds maximum",
			input:   oversized,
			wantErr: "websocket payload too large",
		},
		{
			name:    "truncated header",
			input:   []byte{0x81},
			wantErr: "reading header bytes",
		},
		{
			name:    "truncated 16-bit extended length",
			input:   []byte{0x82, 126, 0x00},
			wantErr: "reading 16-bit extended payload length",
		},
		{
			name: "truncated 64-bit extended length",
			input: []byte{
				0x82, 127,
				0x00, 0x00, 0x00,
			},
			wantErr: "reading 64-bit extended payload length",
		},
		{
			name: "invalid 64-bit payload length",
			input: []byte{
				0x82, 127,
				0x80, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x01,
			},
			wantErr: "invalid 64-bit payload length",
		},
		{
			name: "truncated mask key",
			input: []byte{
				0x81,
				0x80 | 0x01,
				0x01, 0x02,
			},
			wantErr: "reading mask key",
		},
		{
			name: "truncated payload",
			input: []byte{
				0x81,
				0x05,
				'H', 'i',
			},
			wantErr: "reading payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadFrame(bytes.NewReader(tt.input))

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("wanted error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("ReadFrame() returned unexpected error: %v", err)
			}

			if got.Fin != tt.want.Fin {
				t.Fatalf("\nwanted Fin:\n%v\ngot:\n%v", tt.want.Fin, got.Fin)
			}
			if got.Opcode != tt.want.Opcode {
				t.Fatalf("\nwanted Opcode:\n%v\ngot:\n%v", tt.want.Opcode, got.Opcode)
			}
			if got.Masked != tt.want.Masked {
				t.Fatalf("\nwanted Masked:\n%v\ngot:\n%v", tt.want.Masked, got.Masked)
			}
			if got.Key != tt.want.Key {
				t.Fatalf("\nwanted Key:\n%x\ngot:\n%x", tt.want.Key, got.Key)
			}
			if !bytes.Equal(got.Payload, tt.want.Payload) {
				t.Fatalf(
					"\nwanted payload:\n%q\ngot:\n%q",
					tt.want.Payload,
					got.Payload,
				)
			}
		})
	}
}

func TestFrame_WriteFrame(t *testing.T) {
	tests := []struct {
		name           string
		frame          Frame
		mask           bool
		wantFirstByte  byte
		wantLengthCode byte
	}{
		{
			name: "unmasked text",
			frame: Frame{
				Fin:     true,
				Opcode:  OpText,
				Payload: []byte("Hello"),
			},
			wantFirstByte:  0x81,
			wantLengthCode: 5,
		},
		{
			name: "masked text",
			frame: Frame{
				Fin:     true,
				Opcode:  OpText,
				Payload: []byte("Hello"),
			},
			mask:           true,
			wantFirstByte:  0x81,
			wantLengthCode: 5,
		},
		{
			name: "empty ping",
			frame: Frame{
				Fin:    true,
				Opcode: OpPing,
			},
			wantFirstByte:  0x89,
			wantLengthCode: 0,
		},
		{
			name: "unfinished fragmented frame",
			frame: Frame{
				Fin:     false,
				Opcode:  OpText,
				Payload: []byte("Hi"),
			},
			wantFirstByte:  0x01,
			wantLengthCode: 2,
		},
		{
			name: "16-bit extended payload length",
			frame: Frame{
				Fin:     true,
				Opcode:  OpBinary,
				Payload: bytes.Repeat([]byte{'a'}, 126),
			},
			wantFirstByte:  0x82,
			wantLengthCode: 126,
		},
		{
			name: "64-bit extended payload length",
			frame: Frame{
				Fin:     true,
				Opcode:  OpBinary,
				Payload: bytes.Repeat([]byte{'b'}, 65536),
			},
			wantFirstByte:  0x82,
			wantLengthCode: 127,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalPayload := append([]byte(nil), tt.frame.Payload...)

			var output bytes.Buffer
			if err := WriteFrame(&output, tt.frame, tt.mask); err != nil {
				t.Fatalf("WriteFrame() returned unexpected error: %v", err)
			}

			wire := output.Bytes()
			if got := wire[0]; got != tt.wantFirstByte {
				t.Fatalf("\nwanted first header byte:\n%#x\ngot:\n%#x", tt.wantFirstByte, got)
			}
			if got := wire[1] & 0x7f; got != tt.wantLengthCode {
				t.Fatalf("\nwanted payload length code:\n%d\ngot:\n%d", tt.wantLengthCode, got)
			}
			if got := wire[1]&0x80 != 0; got != tt.mask {
				t.Fatalf("\nwanted mask bit:\n%v\ngot:\n%v", tt.mask, got)
			}

			extendedLengthBytes := 0
			switch tt.wantLengthCode {
			case 126:
				extendedLengthBytes = 2
				if got := int(binary.BigEndian.Uint16(wire[2:4])); got != len(tt.frame.Payload) {
					t.Fatalf("\nwanted extended payload length:\n%d\ngot:\n%d", len(tt.frame.Payload), got)
				}
			case 127:
				extendedLengthBytes = 8
				if got := int(binary.BigEndian.Uint64(wire[2:10])); got != len(tt.frame.Payload) {
					t.Fatalf("\nwanted extended payload length:\n%d\ngot:\n%d", len(tt.frame.Payload), got)
				}
			}

			wantWireLength := 2 + extendedLengthBytes + len(tt.frame.Payload)
			if tt.mask {
				wantWireLength += 4
			}
			if got := len(wire); got != wantWireLength {
				t.Fatalf("\nwanted wire length:\n%d\ngot:\n%d", wantWireLength, got)
			}

			got, err := ReadFrame(bytes.NewReader(wire))
			if err != nil {
				t.Fatalf("ReadFrame() returned unexpected error: %v", err)
			}
			if got.Fin != tt.frame.Fin {
				t.Fatalf("\nwanted Fin:\n%v\ngot:\n%v", tt.frame.Fin, got.Fin)
			}
			if got.Opcode != tt.frame.Opcode {
				t.Fatalf("\nwanted Opcode:\n%v\ngot:\n%v", tt.frame.Opcode, got.Opcode)
			}
			if got.Masked != tt.mask {
				t.Fatalf("\nwanted Masked:\n%v\ngot:\n%v", tt.mask, got.Masked)
			}
			if !bytes.Equal(got.Payload, tt.frame.Payload) {
				t.Fatalf("\nwanted payload:\n%q\ngot:\n%q", tt.frame.Payload, got.Payload)
			}
			if !bytes.Equal(tt.frame.Payload, originalPayload) {
				t.Fatalf("\nwanted original payload:\n%q\ngot:\n%q", originalPayload, tt.frame.Payload)
			}
		})
	}
}

func TestFrame_WriteFramePayloadTooLarge(t *testing.T) {
	frame := Frame{Payload: make([]byte, MaxPayload+1)}

	err := WriteFrame(&bytes.Buffer{}, frame, false)
	if err == nil {
		t.Fatal("wanted payload too large error, got nil")
	}
	if !strings.Contains(err.Error(), "payload too large") {
		t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", "payload too large", err)
	}
}

func TestFrame_ParseClosePayload(t *testing.T) {
	tests := []struct {
		name       string
		payload    []byte
		wantCode   int
		wantReason string
		wantErr    string
	}{
		{
			name:     "no status received",
			wantCode: CloseNoStatusReceived,
		},
		{
			name:     "code without reason",
			payload:  []byte{0x03, 0xe8},
			wantCode: 1000,
		},
		{
			name:       "code with reason",
			payload:    []byte{0x03, 0xe8, 'G', 'o', 'o', 'd', 'b', 'y', 'e'},
			wantCode:   1000,
			wantReason: "Goodbye",
		},
		{
			name:       "UTF-8 reason",
			payload:    append([]byte{0x03, 0xe8}, []byte("再见")...),
			wantCode:   1000,
			wantReason: "再见",
		},
		{
			name:    "one-byte payload",
			payload: []byte{0x03},
			wantErr: "close payload contains only one byte",
		},
		{
			name:     "invalid close code",
			payload:  []byte{0x03, 0xed},
			wantCode: CloseNoStatusReceived,
			wantErr:  "invalid close code",
		},
		{
			name:     "invalid UTF-8 reason",
			payload:  []byte{0x03, 0xe8, 0xff},
			wantCode: 1000,
			wantErr:  "close reason is not valid UTF-8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCode, gotReason, err := ParseClosePayload(tt.payload)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("wanted error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", tt.wantErr, err)
				}
			} else if err != nil {
				t.Fatalf("ParseClosePayload() returned unexpected error: %v", err)
			}

			if gotCode != tt.wantCode {
				t.Fatalf("\nwanted code:\n%d\ngot:\n%d", tt.wantCode, gotCode)
			}
			if gotReason != tt.wantReason {
				t.Fatalf("\nwanted reason:\n%q\ngot:\n%q", tt.wantReason, gotReason)
			}
		})
	}
}

func TestFrame_EncodeClosePayload(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		reason  string
		want    []byte
		wantErr string
	}{
		{
			name: "code without reason",
			code: 1000,
			want: []byte{0x03, 0xe8},
		},
		{
			name:   "code with reason",
			code:   1000,
			reason: "Goodbye",
			want:   []byte{0x03, 0xe8, 'G', 'o', 'o', 'd', 'b', 'y', 'e'},
		},
		{
			name:   "UTF-8 reason",
			code:   1000,
			reason: "再见",
			want:   append([]byte{0x03, 0xe8}, []byte("再见")...),
		},
		{
			name:   "maximum reason length",
			code:   1000,
			reason: string(bytes.Repeat([]byte{'a'}, 123)),
			want:   append([]byte{0x03, 0xe8}, bytes.Repeat([]byte{'a'}, 123)...),
		},
		{
			name:    "invalid close code",
			code:    CloseNoStatusReceived,
			wantErr: "invalid close code",
		},
		{
			name:    "invalid UTF-8 reason",
			code:    1000,
			reason:  string([]byte{0xff}),
			wantErr: "close reason is not valid UTF-8",
		},
		{
			name:    "reason too long",
			code:    1000,
			reason:  string(bytes.Repeat([]byte{'a'}, 124)),
			wantErr: "close reason too long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EncodeClosePayload(tt.code, tt.reason)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("wanted error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("\nwanted error containing:\n%q\ngot:\n%q", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("EncodeClosePayload() returned unexpected error: %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("\nwanted payload:\n%x\ngot:\n%x", tt.want, got)
			}
		})
	}
}

func TestFrame_IsValidCloseCode(t *testing.T) {
	tests := []struct {
		name string
		code int
		want bool
	}{
		{name: "normal closure", code: 1000, want: true},
		{name: "going away", code: 1001, want: true},
		{name: "protocol error", code: 1002, want: true},
		{name: "unsupported data", code: 1003, want: true},
		{name: "invalid payload data", code: 1007, want: true},
		{name: "policy violation", code: 1008, want: true},
		{name: "message too big", code: 1009, want: true},
		{name: "mandatory extension", code: 1010, want: true},
		{name: "internal error", code: 1011, want: true},
		{name: "service restart", code: 1012, want: true},
		{name: "try again later", code: 1013, want: true},
		{name: "bad gateway", code: 1014, want: true},
		{name: "application lower boundary", code: 3000, want: true},
		{name: "private use upper boundary", code: 4999, want: true},
		{name: "below valid range", code: 999, want: false},
		{name: "reserved 1004", code: 1004, want: false},
		{name: "no status received", code: 1005, want: false},
		{name: "abnormal closure", code: 1006, want: false},
		{name: "TLS handshake failure", code: 1015, want: false},
		{name: "below application range", code: 2999, want: false},
		{name: "above private use range", code: 5000, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidCloseCode(tt.code)
			if got != tt.want {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}
