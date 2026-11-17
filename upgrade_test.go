package marasi

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// serveUpgradeOrigin answers one request with a 101 and then echoes every
// byte it reads, until the proxy closes the connection.
func serveUpgradeOrigin(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for origin: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n")
		_, _ = io.Copy(conn, reader)
	}()
	return listener.Addr().String()
}

func TestUpgradeHandler(t *testing.T) {
	t.Run("should hand both connections to the registered handler after the response chain, behind the 101", func(t *testing.T) {
		originAddr := serveUpgradeOrigin(t)

		var mu sync.Mutex
		var events []string
		record := func(event string) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event)
		}
		handled := make(chan struct{})
		var proxy *Proxy
		servePairProxy(t, func(p *Proxy) {
			proxy = p
			p.AddRequestPairModifier(SetupRequestModifier)
			p.AddResponsePairModifier(WebSocketPrepareModifier)
			p.AddResponsePairModifier(func(*Proxy, *Pair) error {
				record("response chain")
				return nil
			})
			p.SetUpgradeHandler(func(_ *Proxy, pair *Pair, client, upstream io.ReadWriteCloser) error {
				defer close(handled)
				defer client.Close()
				defer upstream.Close()
				record("handler")
				if pair.Response.StatusCode != http.StatusSwitchingProtocols {
					t.Errorf("wanted: %d\ngot: %d", http.StatusSwitchingProtocols, pair.Response.StatusCode)
				}
				go func() {
					_, _ = io.Copy(upstream, client)
					upstream.Close()
				}()
				_, _ = io.Copy(client, upstream)
				return nil
			})
		})

		client, err := net.Dial("tcp", net.JoinHostPort(proxy.Addr, proxy.Port))
		if err != nil {
			t.Fatalf("dialling proxy: %v", err)
		}
		defer client.Close()
		_ = client.SetDeadline(time.Now().Add(10 * time.Second))
		_, err = io.WriteString(client, "GET http://"+originAddr+"/socket HTTP/1.1\r\nHost: "+originAddr+
			"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
		if err != nil {
			t.Fatalf("writing upgrade request: %v", err)
		}
		reader := bufio.NewReader(client)
		status, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading status line: %v", err)
		}
		if status != "HTTP/1.1 101 Switching Protocols\r\n" {
			t.Fatalf("wanted: %q\ngot: %q", "HTTP/1.1 101 Switching Protocols\r\n", status)
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("reading handshake header: %v", err)
			}
			if line == "\r\n" {
				break
			}
		}

		if _, err := io.WriteString(client, "ping"); err != nil {
			t.Fatalf("writing through the handler: %v", err)
		}
		echo := make([]byte, 4)
		if _, err := io.ReadFull(reader, echo); err != nil {
			t.Fatalf("reading the echo: %v", err)
		}
		if string(echo) != "ping" {
			t.Fatalf("wanted: %q\ngot: %q", "ping", echo)
		}

		client.Close()
		select {
		case <-handled:
		case <-time.After(5 * time.Second):
			t.Fatal("upgrade handler did not return after the client closed")
		}
		mu.Lock()
		defer mu.Unlock()
		if got := strings.Join(events, ", "); got != "response chain, handler" {
			t.Fatalf("wanted: %q\ngot: %q", "response chain, handler", got)
		}
	})
}
