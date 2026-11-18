package marasi

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// shortTimeouts gives the proxy's engine short header and idle timeouts, so a
// test can see them expire.
func shortTimeouts(header, idle time.Duration) func(proxy *Proxy) {
	return func(proxy *Proxy) {
		engine := proxy.engine
		engine.headerTimeout, engine.idleTimeout = header, idle
	}
}

// expectClosed waits for the proxy to close the client connection with
// nothing more sent.
func expectClosed(t *testing.T, conn net.Conn, br *bufio.Reader, within time.Duration) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(within))
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("wanted the proxy to close the client connection within %v: %v (got %q)", within, err, rest)
	}
	if len(rest) != 0 {
		t.Fatalf("wanted nothing more before the close, got %q", rest)
	}
}

// expectOpen checks that the proxy has not closed the client connection.
func expectOpen(t *testing.T, conn net.Conn, br *bufio.Reader, wait time.Duration) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(wait))
	if _, err := br.Peek(1); !isTimeout(err) {
		t.Fatalf("wanted the client connection still open, got %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
}

func isTimeout(err error) bool {
	netErr, ok := err.(net.Error)
	return ok && netErr.Timeout()
}

// serveDefaultH1EngineProxy serves the new engine with the default modifier
// pipeline and the default extensions, as the app runs it.
func serveDefaultH1EngineProxy(t *testing.T) string {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(database)
	defaults, err := repository.GetExtensions()
	if err != nil {
		repository.Close()
		t.Fatalf("getting default extensions: %v", err)
	}
	proxy, err := New(
		WithLogger(logger),
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithExtensions(defaults),
		WithRequestHandler(func(domain.ProxyRequest) error { return nil }),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithBasePipeline(),
		WithDefaultModifierPipeline(),
	)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- proxy.Serve(listener) }()
	t.Cleanup(func() {
		proxy.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})
	return net.JoinHostPort(proxy.Addr, proxy.Port)
}

func TestH1Engine_OriginEndsTheConnection(t *testing.T) {
	t.Run("should forward what arrived of a Content-Length body the origin cut short and then close the client connection", func(t *testing.T) {
		const cut = "HTTP/1.1 200 OK\r\nContent-Length: 20\r\n\r\nonly ten b"
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, cut) })
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		readExactly(t, br, cut)
		expectClosed(t, conn, br, 5*time.Second)
	})

	t.Run("should forward what arrived of a chunked body the origin cut short and then close the client connection", func(t *testing.T) {
		const cut = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n"
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, cut) })
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		readExactly(t, br, cut)
		expectClosed(t, conn, br, 5*time.Second)
	})

	for name, serve := range map[string]func(t *testing.T) string{
		"base pipeline":    func(t *testing.T) string { addr, _ := serveH1EngineProxy(t); return addr },
		"default pipeline": serveDefaultH1EngineProxy,
	} {
		t.Run("should forward what arrived of a body the origin reset and then close the client connection, with the "+name, func(t *testing.T) {
			const cut = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n"
			origin := newScriptedOrigin(t, func(conn net.Conn) {
				io.WriteString(conn, cut)
				time.Sleep(100 * time.Millisecond)
				conn.(*net.TCPConn).SetLinger(0)
			})
			conn, br := dialProxy(t, serve(t))

			fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

			readExactly(t, br, cut)
			expectClosed(t, conn, br, 5*time.Second)
		})
	}

	t.Run("should close the client connection when the origin closes its connection between responses", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) { io.WriteString(conn, finalOK) })
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		readExactly(t, br, finalOK)
		expectClosed(t, conn, br, 5*time.Second)
	})

	t.Run("should keep the client connection while the origin keeps its own", func(t *testing.T) {
		origin := newBodyOrigin(t, finalOK)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		readExactly(t, br, finalOK)
		expectOpen(t, conn, br, 300*time.Millisecond)
	})
}

func TestH1Engine_ClientEndsTheConnection(t *testing.T) {
	t.Run("should close the origin connection when the client leaves mid-request", func(t *testing.T) {
		origin := newClosingOrigin(t)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)
		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		readExactly(t, br, routedOK)

		fmt.Fprintf(conn, "POST http://%s/ HTTP/1.1\r\nHost: %s\r\nContent-Length: 10\r\n\r\nabc", origin.addr, origin.addr)
		time.Sleep(100 * time.Millisecond)
		conn.Close()

		select {
		case <-origin.closed:
		case <-time.After(5 * time.Second):
			t.Fatal("wanted the origin connection closed once the client left")
		}
	})
}

func TestH1Engine_AnotherOriginReplacesTheConnection(t *testing.T) {
	t.Run("should close the first origin's connection when a request on the same client connection names another origin", func(t *testing.T) {
		first, second := newClosingOrigin(t), newClosingOrigin(t)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", first.addr, first.addr)
		readExactly(t, br, routedOK)
		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", second.addr, second.addr)
		readExactly(t, br, routedOK)

		select {
		case <-first.closed:
		case <-time.After(5 * time.Second):
			t.Fatal("wanted the first origin's connection closed")
		}
		expectOpen(t, conn, br, 200*time.Millisecond)
	})
}

func TestH1Engine_Pipelining(t *testing.T) {
	t.Run("should send pipelined requests to the origin in order and answer them in order", func(t *testing.T) {
		origin := newBodyOrigin(t, finalOK)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		var pipelined strings.Builder
		for _, path := range []string{"/first", "/second", "/third"} {
			fmt.Fprintf(&pipelined, "GET http://%s%s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, path, origin.addr)
		}
		io.WriteString(conn, pipelined.String())

		readExactly(t, br, finalOK+finalOK+finalOK)
		origin.mu.Lock()
		defer origin.mu.Unlock()
		var paths []string
		for _, request := range origin.requests {
			paths = append(paths, strings.Fields(request)[1])
		}
		if strings.Join(paths, " ") != "/first /second /third" {
			t.Fatalf("wanted the origin to receive /first /second /third in order, got %q", paths)
		}
	})
}

func TestH1Engine_Timeouts(t *testing.T) {
	t.Run("should wait 60 seconds for request headers and 5 minutes on an idle keep-alive connection", func(t *testing.T) {
		engine := newH1Engine(nil)
		if engine.headerTimeout != 60*time.Second || engine.idleTimeout != 5*time.Minute {
			t.Fatalf("wanted a 60s header and a 5m idle timeout, got %v and %v", engine.headerTimeout, engine.idleTimeout)
		}
	})

	t.Run("should disconnect a client that never finishes its headers at the header timeout", func(t *testing.T) {
		_, addr, _ := serveFramingProxy(t, shortTimeouts(200*time.Millisecond, time.Minute))
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, "GET http://example.test/ HTTP/1.1\r\nHost: exa")

		expectClosed(t, conn, br, 3*time.Second)
	})

	t.Run("should close an idle keep-alive client connection at the idle timeout", func(t *testing.T) {
		origin := newBodyOrigin(t, finalOK)
		_, addr, _ := serveFramingProxy(t, shortTimeouts(time.Minute, 200*time.Millisecond))
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		readExactly(t, br, finalOK)

		expectClosed(t, conn, br, 3*time.Second)
	})

	t.Run("should not cut off a response body that takes longer than both timeouts", func(t *testing.T) {
		origin := newScriptedOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\n")
			for _, b := range "slow!" {
				time.Sleep(150 * time.Millisecond)
				io.WriteString(conn, string(b))
			}
			io.Copy(io.Discard, conn)
		})
		_, addr, _ := serveFramingProxy(t, shortTimeouts(100*time.Millisecond, 100*time.Millisecond))
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		readExactly(t, br, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nslow!")
	})

	t.Run("should not cut off a request body that takes longer than both timeouts", func(t *testing.T) {
		origin := newBodyOrigin(t, finalOK)
		_, addr, _ := serveFramingProxy(t, shortTimeouts(100*time.Millisecond, 100*time.Millisecond))
		conn, br := dialProxy(t, addr)

		fmt.Fprintf(conn, "POST http://%s/ HTTP/1.1\r\nHost: %s\r\nContent-Length: 5\r\n\r\n", origin.addr, origin.addr)
		for _, b := range "slow!" {
			time.Sleep(150 * time.Millisecond)
			io.WriteString(conn, string(b))
		}

		readExactly(t, br, finalOK)
		if want := "POST / HTTP/1.1\r\nHost: " + origin.addr + "\r\nContent-Length: 5\r\n\r\nslow!"; origin.request(t) != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, origin.request(t))
		}
	})
}

func TestH1Engine_CheckpointHoldsAChunkedRequest(t *testing.T) {
	t.Run("should send a held chunked request to the origin whole after a hold longer than both timeouts", func(t *testing.T) {
		const response = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
		origin := newBodyOrigin(t, response)
		proxy, addr, _ := serveCheckpointProxy(t, shortTimeouts(100*time.Millisecond, 100*time.Millisecond))
		conn, br := dialProxy(t, addr)

		request := "POST / HTTP/1.1\r\nHost: " + origin.addr + "\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"
		io.WriteString(conn, strings.Replace(request, "POST /", "POST http://"+origin.addr+"/", 1))

		shown := "POST / HTTP/1.1\r\nHost: " + origin.addr + "\r\nTransfer-Encoding: chunked\r\n\r\nhello world"
		time.Sleep(400 * time.Millisecond)
		forwardHeld(t, proxy, shown, shown)
		forwardHeld(t, proxy, response, response)

		readExactly(t, br, response)
		if got := origin.request(t); got != request {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", request, got)
		}
	})
}

func TestH1Engine_MalformedHead(t *testing.T) {
	t.Run("should answer a request head it cannot read with a 400 and close the connection", func(t *testing.T) {
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, "GET http://example.test/ HTTP/1.1\r\nHost : example.test\r\n\r\n")

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, err := io.ReadAll(br)
		if err != nil {
			t.Fatalf("wanted the proxy to answer and close: %v (got %q)", err, got)
		}
		if !strings.HasPrefix(string(got), "HTTP/1.1 400 Bad Request\r\n") {
			t.Fatalf("wanted a 400, got %q", got)
		}
	})

	t.Run("should answer an HTTP major version other than 1 with a 505 and close the connection", func(t *testing.T) {
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)

		io.WriteString(conn, "GET http://example.test/ HTTP/9.9\r\nHost: example.test\r\n\r\n")

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, err := io.ReadAll(br)
		if err != nil {
			t.Fatalf("wanted the proxy to answer and close: %v (got %q)", err, got)
		}
		if !strings.HasPrefix(string(got), "HTTP/1.1 505 HTTP Version Not Supported\r\n") {
			t.Fatalf("wanted a 505, got %q", got)
		}
	})
}
