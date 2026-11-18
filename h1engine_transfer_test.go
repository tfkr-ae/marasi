package marasi

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// newUntilCloseOrigin is an origin that reads one request head, answers with
// response and closes the connection, so a body framed until close ends.
func newUntilCloseOrigin(t *testing.T, response string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := h1.ReadRequestHead(bufio.NewReader(conn)); err != nil {
					return
				}
				io.WriteString(conn, response)
			}()
		}
	}()
	return l.Addr().String()
}

// readAllUntilClose reads what the proxy sends until it closes the client
// connection and compares it with want.
func readAllUntilClose(t *testing.T, conn net.Conn, br *bufio.Reader, want string) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("wanted the proxy to close the client connection: %v (got %q)", err, got)
	}
	if string(got) != want {
		t.Fatalf("wanted the client to receive:\n%q\ngot:\n%q", want, got)
	}
}

// gzipCoded stands for a body in the gzip transfer coding; Marasi never
// decodes a transfer coding, so the bytes need not be real gzip.
const gzipCoded = "\x1f\x8b\x08\x00coded"

func TestH1Engine_UnknownTransferCodings(t *testing.T) {
	t.Run("should forward gzip, chunked from the origin unchanged and store the coded bytes with Transfer-Encoding", func(t *testing.T) {
		const head = "HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip, chunked\r\nX-After: a\r\n\r\n"
		const response = head + "4;ext\r\n\x1f\x8b\x08\x00\r\n5\r\ncoded\r\n0\r\nX-Sum: 1\r\n\r\n"
		origin := newBodyOrigin(t, response)
		_, addr, captured := serveFramingProxy(t, nil)

		roundTripRaw(t, addr, "GET http://"+origin.addr+"/ HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n", response)

		_, stored := storedPair(t, captured)
		if want := head + gzipCoded; stored != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, stored)
		}
	})

	t.Run("should read a coding that does not end in chunked until the origin closes and forward it", func(t *testing.T) {
		const head = "HTTP/1.1 200 OK\r\ntransfer-encoding: gzip\r\nX-After: a\r\n\r\n"
		origin := newUntilCloseOrigin(t, head+gzipCoded)
		_, addr, captured := serveFramingProxy(t, nil)

		conn, br := dialProxy(t, addr)
		io.WriteString(conn, "GET http://"+origin+"/ HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
		readAllUntilClose(t, conn, br, head+gzipCoded)

		_, stored := storedPair(t, captured)
		if want := head + gzipCoded; stored != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, stored)
		}
	})

	t.Run("should send an edited gzip, chunked body chunked without gzip, keeping the trailers", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\ntransfer-encoding: gzip, chunked\r\nX-After: a\r\n\r\n9\r\n"+gzipCoded+"\r\n0\r\nX-Sum: 1\r\n\r\n")
		_, addr, captured := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddResponsePairModifier(setResponseBody("plain"))
		})

		const head = "HTTP/1.1 200 OK\r\ntransfer-encoding: chunked\r\nX-After: a\r\n\r\n"
		roundTripRaw(t, addr, "GET http://"+origin.addr+"/ HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n",
			head+"5\r\nplain\r\n0\r\nX-Sum: 1\r\n\r\n")

		_, stored := storedPair(t, captured)
		if want := head + "plain"; stored != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, stored)
		}
	})

	t.Run("should send an edited body read until close chunked without its coding", func(t *testing.T) {
		origin := newUntilCloseOrigin(t, "HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip\r\nX-After: a\r\n\r\n"+gzipCoded)
		_, addr, captured := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddResponsePairModifier(setResponseBody("plain"))
		})

		const head = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nX-After: a\r\n\r\n"
		conn, br := dialProxy(t, addr)
		io.WriteString(conn, "GET http://"+origin+"/ HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
		readAllUntilClose(t, conn, br, head+"5\r\nplain\r\n0\r\n\r\n")

		_, stored := storedPair(t, captured)
		if want := head + "plain"; stored != want {
			t.Fatalf("wanted the stored response:\n%q\ngot:\n%q", want, stored)
		}
	})

	t.Run("should drop only the codings it cannot redo from an edited request", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		_, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(setRequestBody("plain"))
		})

		host := "Host: " + origin.addr + "\r\n"
		roundTripRaw(t, addr, "POST http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"Transfer-Encoding: gzip\r\nTransfer-Encoding: chunked\r\nX-After: a\r\n\r\n9\r\n"+gzipCoded+"\r\n0\r\n\r\n",
			"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")

		want := "POST / HTTP/1.1\r\n" + host + "Transfer-Encoding: chunked\r\nX-After: a\r\n\r\n5\r\nplain\r\n0\r\n\r\n"
		if got := origin.request(t); got != want {
			t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
		}
	})
}
