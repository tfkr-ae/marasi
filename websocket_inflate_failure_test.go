package marasi

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	marasiws "github.com/tfkr-ae/marasi/websocket"
)

// deflateHandoff hands a permessage-deflate WebSocket upgrade to the proxy
// over in-memory peers. It returns the client and upstream peers once the
// client has read the handshake response.
func deflateHandoff(t *testing.T, proxy *Proxy) (clientPeer, upstreamPeer net.Conn) {
	t.Helper()
	requestID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating request UUID: %v", err)
	}

	clientConnection, clientPeer := net.Pipe()
	upstreamConnection, upstreamPeer := net.Pipe()
	t.Cleanup(func() {
		_ = clientPeer.Close()
		_ = upstreamPeer.Close()
	})
	deadline := time.Now().Add(5 * time.Second)
	for _, peer := range []net.Conn{clientPeer, upstreamPeer} {
		if err := peer.SetDeadline(deadline); err != nil {
			t.Fatalf("setting peer deadline: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "https://marasi.app/socket", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	clientBuffer := bufio.NewReadWriter(bufio.NewReader(clientConnection), bufio.NewWriter(clientConnection))
	martianContext, remove, err := martian.TestContext(req, clientConnection, clientBuffer)
	if err != nil {
		t.Fatalf("applying martian context: %v", err)
	}
	t.Cleanup(remove)

	req = core.ContextWithRequestID(req, requestID)
	req = core.ContextWithSession(req, martianContext.Session())
	req = core.ContextWithWebSocketUpstream(req, upstreamConnection)
	req = req.WithContext(context.WithValue(req.Context(), webSocketNegotiationKey{}, webSocketNegotiation{
		accept:  []string{"permessage-deflate"},
		deflate: &domain.WebSocketDeflate{ServerMaxWindowBits: 15, ClientMaxWindowBits: 15},
	}))
	res := &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Request:    req,
		Header: http.Header{
			"Connection": []string{"Upgrade"},
			"Upgrade":    []string{"websocket"},
		},
		Body: http.NoBody,
	}
	res.Header.Set(marasiws.ExtensionsHeader, "permessage-deflate")
	go func() { _ = WebSocketHandoffModifier(proxy, res) }()

	clientReader := bufio.NewReader(clientPeer)
	for {
		line, err := clientReader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading handshake response: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	return clientPeer, upstreamPeer
}

// receiveLog returns the first log written to the proxy's DB write channel.
func receiveLog(t *testing.T, writes <-chan any) *domain.Log {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case item := <-writes:
			if log, ok := item.(*domain.Log); ok {
				return log
			}
		case <-timeout:
			t.Fatalf("timed out waiting for a proxy log")
			return nil
		}
	}
}

// zerosDeflated returns a permessage-deflate payload that inflates to size
// zero bytes.
func zerosDeflated(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		t.Fatalf("creating flate writer: %v", err)
	}
	if _, err := w.Write(make([]byte, size)); err != nil {
		t.Fatalf("compressing: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{0x00, 0x00, 0xff, 0xff})
}

func TestWebSocketHandoffModifier_LogsInflateFailures(t *testing.T) {
	tests := []struct {
		name    string
		limit   int64
		payload []byte
		want    string
	}{
		{name: "should write a proxy log naming the connection for corrupt deflate", payload: []byte{0x07, 0x00, 0x01, 0x02}, want: "Sent 1007"},
		{name: "should write a proxy log naming the connection for a deflate bomb", limit: 1024, payload: zerosDeflated(t, 64*1024), want: "Sent 1009"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := &Proxy{
				Config:            &Config{WebSocketMaxMessageSize: tt.limit},
				WebSocketRegistry: marasiws.NewRegistry(),
				DBWriteChannel:    make(chan any, 16),
			}
			clientPeer, upstreamPeer := deflateHandoff(t, proxy)

			first := receiveSessionDBWrite(t, proxy.DBWriteChannel)
			openRecord, ok := first.(*domain.WebSocketConnection)
			if !ok {
				t.Fatalf("wanted the open connection record first, got %#v", first)
			}
			frame := marasiws.Frame{Fin: true, Rsv1: true, Opcode: marasiws.OpBinary, Payload: tt.payload}
			if err := marasiws.WriteFrame(clientPeer, frame, true); err != nil {
				t.Fatalf("writing compressed frame: %v", err)
			}
			go func() { _, _ = marasiws.ReadFrame(upstreamPeer) }()
			if _, err := marasiws.ReadFrame(clientPeer); err != nil {
				t.Fatalf("reading close at client: %v", err)
			}

			log := receiveLog(t, proxy.DBWriteChannel)
			if log.Level != "WARN" || !strings.Contains(log.Message, openRecord.ID.String()) || !strings.Contains(log.Message, tt.want) {
				t.Fatalf("\nwanted log:\nWARN naming %s and %q\ngot:\n%s %q", openRecord.ID, tt.want, log.Level, log.Message)
			}
		})
	}
}
