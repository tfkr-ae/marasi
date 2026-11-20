package marasi

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestRoute_ConnectOverTLS(t *testing.T) {
	t.Run("should offer an HTTPS upstream proxy http/1.1 only, whatever the client offered the origin", func(t *testing.T) {
		issuer, ca := newTestLeafIssuer(t, 24*time.Hour)
		offered := make(chan []string, 1)
		config := issuer.config("127.0.0.1:443")
		config.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			offered <- hello.SupportedProtos
			return nil, nil
		}
		l, err := tls.Listen("tcp", "127.0.0.1:0", config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			if _, err := http.ReadRequest(bufio.NewReader(conn)); err == nil {
				io.WriteString(conn, "HTTP/1.1 200 OK\r\n\r\n")
			}
		}()
		_, port, _ := net.SplitHostPort(l.Addr().String())
		portNumber, _ := strconv.Atoi(port)
		route := &Route{
			Name: "tls", Scheme: UpstreamProxyHTTPS, Host: "127.0.0.1", Port: portNumber,
			ca: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})), timeout: 5 * time.Second,
		}
		conn, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx := withOriginALPN(context.Background(), []string{"h2", "http/1.1"})

		tunnel, err := route.connectOverTLS(ctx, conn, "origin.test:443")
		if err != nil {
			t.Fatalf("opening the tunnel\nwanted:\nnil\ngot:\n%v", err)
		}
		tunnel.Close()
		if got := <-offered; !slices.Equal(got, []string{"http/1.1"}) {
			t.Fatalf("ALPN offered the upstream proxy\nwanted:\n[http/1.1]\ngot:\n%v", got)
		}
	})
}
