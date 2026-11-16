package shim

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// target accepts connections and runs serve on each.
func target(t *testing.T, config *tls.Config, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if config != nil {
		l = tls.NewListener(l, config)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	return l.Addr().String()
}

// answer reads one request head and writes response.
func answer(response string, then func(net.Conn)) func(net.Conn) {
	return func(conn net.Conn) {
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				conn.Close()
				return
			}
			if line == "\r\n" {
				break
			}
		}
		io.WriteString(conn, response)
		then(conn)
	}
}

func keepOpen(conn net.Conn) { io.Copy(io.Discard, conn); conn.Close() }

// connectProxy answers CONNECT and splices to dial, whatever the target.
func connectProxy(t *testing.T, dial string, targets chan<- string) string {
	return target(t, nil, func(conn net.Conn) {
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		targets <- req.Method + " " + req.Host
		up, err := net.Dial("tcp", dial)
		if err != nil {
			return
		}
		defer up.Close()
		io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		go io.Copy(up, conn)
		io.Copy(conn, up)
	})
}

// exchange sends request through the shim, reads until the response holds
// want, and closes.
func exchange(t *testing.T, address, request, want string) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, request)
	var got []byte
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !strings.Contains(string(got), want) {
		n, err := conn.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("reading %q: %v after %q", want, err, got)
		}
	}
}

const (
	request  = "GET / HTTP/1.1\r\nHost: target.test\r\n\r\n"
	response = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"
)

func start(t *testing.T, options Options) *Shim {
	t.Helper()
	if options.Settle == 0 {
		options.Settle = 100 * time.Millisecond
	}
	s, err := Start(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func one(t *testing.T, captures []Capture) Capture {
	t.Helper()
	if len(captures) != 1 {
		t.Fatalf("\nwanted:\n1 capture\ngot:\n%d", len(captures))
	}
	if captures[0].Err != "" {
		t.Fatalf("capture failed: %s", captures[0].Err)
	}
	return captures[0]
}

func TestShim(t *testing.T) {
	t.Run("should splice straight to the target and capture each direction", func(t *testing.T) {
		s := start(t, Options{Target: "target.test:80", Dial: target(t, nil, answer(response, keepOpen))})
		exchange(t, s.Address(), request, "OK")
		c := one(t, s.Close())
		toServer, toClient := c.Bytes()
		if string(toServer) != request || string(toClient) != response {
			t.Fatalf("\nwanted:\n%q / %q\ngot:\n%q / %q", request, response, toServer, toClient)
		}
		if c.End != EndOpen {
			t.Fatalf("\nwanted:\nend %q\ngot:\n%q", EndOpen, c.End)
		}
		if c.Tunnel != "" {
			t.Fatalf("\nwanted:\nno tunnel in the direct run\ngot:\n%q", c.Tunnel)
		}
	})

	t.Run("should record that the target closed the connection", func(t *testing.T) {
		s := start(t, Options{Target: "target.test:80", Dial: target(t, nil, answer(response, func(c net.Conn) { c.Close() }))})
		exchange(t, s.Address(), request, "OK")
		if c := one(t, s.Close()); c.End != EndClosed {
			t.Fatalf("\nwanted:\nend %q\ngot:\n%q", EndClosed, c.End)
		}
	})

	t.Run("should record that the target reset the connection", func(t *testing.T) {
		reset := func(c net.Conn) { c.(*net.TCPConn).SetLinger(0); c.Close() }
		s := start(t, Options{Target: "target.test:80", Dial: target(t, nil, answer(response, reset))})
		exchange(t, s.Address(), request, "OK")
		if c := one(t, s.Close()); c.End != EndReset {
			t.Fatalf("\nwanted:\nend %q\ngot:\n%q", EndReset, c.End)
		}
	})

	t.Run("should record a close that comes just after the tester leaves", func(t *testing.T) {
		late := func(c net.Conn) { time.Sleep(20 * time.Millisecond); c.Close() }
		s := start(t, Options{Target: "target.test:80", Dial: target(t, nil, answer(response, late))})
		exchange(t, s.Address(), request, "OK")
		if c := one(t, s.Close()); c.End != EndClosed {
			t.Fatalf("\nwanted:\nend %q\ngot:\n%q", EndClosed, c.End)
		}
	})

	t.Run("should send CONNECT for the target through a proxy", func(t *testing.T) {
		targets := make(chan string, 1)
		proxy := connectProxy(t, target(t, nil, answer(response, keepOpen)), targets)
		s := start(t, Options{Target: "target.test:8080", Proxy: proxy})
		exchange(t, s.Address(), request, "OK")
		c := one(t, s.Close())
		if got := <-targets; got != "CONNECT target.test:8080" {
			t.Fatalf("\nwanted:\nCONNECT target.test:8080\ngot:\n%q", got)
		}
		if c.Tunnel != "HTTP/1.1 200 Connection established" {
			t.Fatalf("\nwanted:\nthe proxy's answer as the tunnel\ngot:\n%q", c.Tunnel)
		}
		if toServer, toClient := c.Bytes(); string(toServer) != request || string(toClient) != response {
			t.Fatalf("\nwanted:\n%q / %q\ngot:\n%q / %q", request, response, toServer, toClient)
		}
	})

	t.Run("should add TLS for a plaintext tester and capture the plaintext", func(t *testing.T) {
		ca, err := conformance.NewCA()
		if err != nil {
			t.Fatal(err)
		}
		server, err := ca.ServerTLS("target.test")
		if err != nil {
			t.Fatal(err)
		}
		s := start(t, Options{Target: "target.test:443", Dial: target(t, server, answer(response, keepOpen)), TLS: ca.ClientTLS("target.test")})
		exchange(t, s.Address(), request, "OK")
		c := one(t, s.Close())
		if toServer, toClient := c.Bytes(); string(toServer) != request || string(toClient) != response {
			t.Fatalf("\nwanted:\n%q / %q\ngot:\n%q / %q", request, response, toServer, toClient)
		}
	})

	t.Run("should report a TLS handshake the target fails", func(t *testing.T) {
		ca, _ := conformance.NewCA()
		other, _ := conformance.NewCA()
		server, _ := other.ServerTLS("target.test")
		s := start(t, Options{Target: "target.test:443", Dial: target(t, server, keepOpen), TLS: ca.ClientTLS("target.test")})
		conn, err := net.Dial("tcp", s.Address())
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		io.Copy(io.Discard, conn)
		conn.Close()
		captures := s.Close()
		if len(captures) != 1 || !strings.Contains(captures[0].Err, "certificate") {
			t.Fatalf("\nwanted:\none capture with a certificate error\ngot:\n%+v", captures)
		}
	})

	t.Run("should list captures in the order the tester connected", func(t *testing.T) {
		s := start(t, Options{Target: "target.test:80", Dial: target(t, nil, answer(response, keepOpen))})
		for _, path := range []string{"/1", "/2", "/3"} {
			exchange(t, s.Address(), "GET "+path+" HTTP/1.1\r\n\r\n", "OK")
		}
		captures := s.Close()
		var got []string
		for _, c := range captures {
			toServer, _ := c.Bytes()
			got = append(got, strings.Fields(string(toServer))[1])
		}
		if strings.Join(got, ",") != "/1,/2,/3" {
			t.Fatalf("\nwanted:\n/1,/2,/3\ngot:\n%v", got)
		}
	})
}
