package autobahn

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// The lenient peers are the second upstream: they echo whatever arrives and
// validate nothing (no UTF-8, close-code, RSV, opcode, or control-size checks,
// no extensions). A case that passes against them reflects the proxy, not a
// strict peer catching the violation. They are written independently of the
// product's websocket package on purpose.

const (
	opContinuation = 0x0
	opText         = 0x1
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	idleTimeout   = 60 * time.Second
)

type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

func readFrame(r *bufio.Reader) (frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return frame{}, err
	}
	f := frame{fin: head[0]&0x80 != 0, opcode: head[0] & 0x0F}
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > 1<<30 {
		return frame{}, fmt.Errorf("frame length %d too large for the lenient peer", length)
	}
	var key [4]byte
	if masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return frame{}, err
		}
	}
	f.payload = make([]byte, length)
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return frame{}, err
	}
	if masked {
		for i := range f.payload {
			f.payload[i] ^= key[i%4]
		}
	}
	return f, nil
}

func writeFrame(w *bufio.Writer, opcode byte, payload []byte, mask bool) error {
	head := []byte{0x80 | opcode, 0}
	var maskBit byte
	if mask {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		head[1] = maskBit | byte(n)
	case n <= 0xFFFF:
		head[1] = maskBit | 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = maskBit | 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	body := payload
	if mask {
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		head = append(head, key[:]...)
		body = make([]byte, len(payload))
		for i := range payload {
			body[i] = payload[i] ^ key[i%4]
		}
	}
	if _, err := w.Write(head); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return w.Flush()
}

// echo relays every whole message, answers pings, and echoes a close. It
// returns after the close is echoed or the connection fails. onText, when
// set, receives text messages instead of echoing them.
func echo(conn net.Conn, r *bufio.Reader, mask bool, onText func([]byte)) error {
	w := bufio.NewWriter(conn)
	var (
		messageOpcode byte
		message       []byte
		inMessage     bool
	)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
			return err
		}
		f, err := readFrame(r)
		if err != nil {
			return err
		}
		switch f.opcode {
		case opContinuation:
			if !inMessage {
				continue
			}
			message = append(message, f.payload...)
		case opClose:
			return writeFrame(w, opClose, f.payload, mask)
		case opPing:
			if err := writeFrame(w, opPong, f.payload, mask); err != nil {
				return err
			}
			continue
		case opPong:
			continue
		default:
			if f.opcode > 0x2 {
				continue
			}
			messageOpcode, message, inMessage = f.opcode, f.payload, true
		}
		if !f.fin {
			continue
		}
		inMessage = false
		if onText != nil && messageOpcode == opText {
			onText(message)
			continue
		}
		if err := writeFrame(w, messageOpcode, message, mask); err != nil {
			return err
		}
	}
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ServeLenient accepts WebSocket connections on listener and echoes on each
// until listener is closed.
func ServeLenient(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			r := bufio.NewReader(conn)
			request, err := http.ReadRequest(r)
			if err != nil {
				return
			}
			response := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
				"Sec-WebSocket-Accept: " + acceptKey(request.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"
			if _, err := io.WriteString(conn, response); err != nil {
				return
			}
			_ = echo(conn, r, false, nil)
		}()
	}
}

// RunLenientClient drives an Autobahn fuzzingserver at server (host:port) as
// the testee agent: it asks for the case count, runs each case, and asks
// the server to write its reports. When proxy is set, every connection goes
// to that HTTP proxy with an absolute-form request target.
func RunLenientClient(ctx context.Context, server, proxy, agent string) error {
	var count int
	err := lenientSession(ctx, server, proxy, "/getCaseCount", func(message []byte) {
		count, _ = strconv.Atoi(string(message))
	})
	if count == 0 {
		return errors.Join(errors.New("fuzzingserver reported no cases"), err)
	}
	for i := 1; i <= count; i++ {
		resource := fmt.Sprintf("/runCase?case=%d&agent=%s", i, agent)
		// A case that ends in a failed connection is the case's result, not a harness error.
		if err := lenientSession(ctx, server, proxy, resource, nil); ctx.Err() != nil {
			return err
		}
	}
	return lenientSession(ctx, server, proxy, "/updateReports?agent="+agent, nil)
}

func lenientSession(ctx context.Context, server, proxy, resource string, onText func([]byte)) error {
	address, target := server, resource
	if proxy != "" {
		address, target = proxy, "http://"+server+resource
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	request := "GET " + target + " HTTP/1.1\r\nHost: " + server + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return err
	}
	r := bufio.NewReader(conn)
	response, err := http.ReadResponse(r, nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("upgrade answered %s", response.Status)
	}
	if err := echo(conn, r, true, onText); err != nil {
		return err
	}
	// After echoing a close the client waits for the server to close TCP.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.Copy(io.Discard, r)
	return nil
}
