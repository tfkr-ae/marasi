package marasi

import (
	"bufio"
	"fmt"
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

// h2cSwitch is an origin's 101 to a protocol that is not WebSocket.
const h2cSwitch = "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n"

func TestOpaqueUpgrade(t *testing.T) {
	for name, serve := range map[string]func(t *testing.T) (string, func(*testing.T) string){
		"base pipeline": func(t *testing.T) (string, func(*testing.T) string) {
			addr, captured := serveH1EngineProxy(t)
			return addr, func(t *testing.T) string {
				t.Helper()
				select {
				case res := <-captured.response:
					return string(res.Raw)
				case <-time.After(5 * time.Second):
					t.Fatal("the 101 was not stored")
					return ""
				}
			}
		},
		"default pipeline": func(t *testing.T) (string, func(*testing.T) string) {
			return serveDefaultH1EngineProxy(t), nil
		},
	} {
		t.Run("should write a 101 that is not WebSocket and relay bytes both ways, with the "+name, func(t *testing.T) {
			origin := newScriptedOrigin(t, func(conn net.Conn) {
				io.WriteString(conn, h2cSwitch)
				io.Copy(conn, conn)
			})
			addr, stored := serve(t)
			conn, br := dialProxy(t, addr)

			fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABkAAQAAP__\r\n\r\n", origin.addr, origin.addr)
			readExactly(t, br, h2cSwitch)
			io.WriteString(conn, "opaque bytes")
			readExactly(t, br, "opaque bytes")

			if stored != nil {
				if got := stored(t); got != h2cSwitch {
					t.Fatalf("\nwanted:\n%q\ngot:\n%q", h2cSwitch, got)
				}
			}
			conn.Close()
		})
	}
}
