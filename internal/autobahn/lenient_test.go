package autobahn

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// maskedFrame encodes a client frame by hand: first is the first header byte
// (FIN, RSV, and opcode), and long forces the 64-bit length form.
func maskedFrame(first byte, payload []byte, long bool) []byte {
	key := [4]byte{0x11, 0x22, 0x33, 0x44}
	out := []byte{first}
	if long {
		out = append(out, 0x80|127)
		out = binary.BigEndian.AppendUint64(out, uint64(len(payload)))
	} else {
		out = append(out, 0x80|byte(len(payload)))
	}
	out = append(out, key[:]...)
	for i, b := range payload {
		out = append(out, b^key[i%4])
	}
	return out
}

func TestServeLenient(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go ServeLenient(listener)

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting deadline: %v", err)
	}

	request := "GET / HTTP/1.1\r\nHost: peer\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Extensions: permessage-deflate; client_max_window_bits\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("writing handshake: %v", err)
	}
	r := bufio.NewReader(conn)
	response, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatalf("reading handshake: %v", err)
	}

	t.Run("should refuse an offered deflate", func(t *testing.T) {
		if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
			t.Fatalf("\nwanted:\n101 with the RFC 6455 sample accept\ngot:\n%s, accept %q", response.Status, response.Header.Get("Sec-WebSocket-Accept"))
		}
		if extensions, ok := response.Header["Sec-Websocket-Extensions"]; ok {
			t.Fatalf("\nwanted:\nno Sec-WebSocket-Extensions\ngot:\n%q", extensions)
		}
	})

	t.Run("should answer a ping between fragments and echo the joined message", func(t *testing.T) {
		var wire []byte
		wire = append(wire, maskedFrame(0x40|opText, []byte{0xff, 0xfe}, false)...) // RSV1, not FIN, invalid UTF-8
		wire = append(wire, maskedFrame(0x80|opPing, []byte("p"), false)...)
		wire = append(wire, maskedFrame(0x80|opContinuation, []byte("tail"), true)...)
		if _, err := conn.Write(wire); err != nil {
			t.Fatalf("writing frames: %v", err)
		}

		pong, err := readFrame(r)
		if err != nil || pong.opcode != opPong || !pong.fin || string(pong.payload) != "p" {
			t.Fatalf("\nwanted:\npong %q\ngot:\n%+v (err %v)", "p", pong, err)
		}
		echoed, err := readFrame(r)
		want := append([]byte{0xff, 0xfe}, "tail"...)
		if err != nil || echoed.opcode != opText || !echoed.fin || !bytes.Equal(echoed.payload, want) {
			t.Fatalf("\nwanted:\none FIN text frame %x\ngot:\n%+v (err %v)", want, echoed, err)
		}
	})

	t.Run("should echo close code 999", func(t *testing.T) {
		payload := binary.BigEndian.AppendUint16(nil, 999)
		if _, err := conn.Write(maskedFrame(0x80|opClose, payload, false)); err != nil {
			t.Fatalf("writing close: %v", err)
		}
		closed, err := readFrame(r)
		if err != nil || closed.opcode != opClose || !bytes.Equal(closed.payload, payload) {
			t.Fatalf("\nwanted:\nclose %x\ngot:\n%+v (err %v)", payload, closed, err)
		}
	})
}
