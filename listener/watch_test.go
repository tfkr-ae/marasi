package listener

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// tcpPair returns the accepted and dialed ends of a loopback TCP connection.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer l.Close()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	server, err := l.Accept()
	if err != nil {
		t.Fatalf("accepting: %v", err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return server, client
}

func waitClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not report the closed client")
	}
}

func TestWatchedConn(t *testing.T) {
	t.Run("should report a client that closes while watched", func(t *testing.T) {
		server, client := tcpPair(t)
		conn := NewWatchedConnection(server)
		closed := make(chan struct{})
		conn.Watch(func() { close(closed) })

		client.Close()

		waitClosed(t, closed)
	})

	t.Run("should return bytes read while watching before the close", func(t *testing.T) {
		server, client := tcpPair(t)
		conn := NewWatchedConnection(server)
		closed := make(chan struct{})
		conn.Watch(func() { close(closed) })

		if _, err := client.Write([]byte("close-notify")); err != nil {
			t.Fatalf("writing: %v", err)
		}
		client.Close()
		waitClosed(t, closed)

		got, err := io.ReadAll(conn)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if string(got) != "close-notify" {
			t.Fatalf("wanted: %q\ngot: %q", "close-notify", got)
		}
	})

	t.Run("should report a client that already closed when the next watch starts", func(t *testing.T) {
		server, client := tcpPair(t)
		conn := NewWatchedConnection(server)
		first := make(chan struct{})
		conn.Watch(func() { close(first) })
		client.Close()
		waitClosed(t, first)

		second := make(chan struct{})
		conn.Watch(func() { close(second) })

		waitClosed(t, second)
	})

	t.Run("should stop watching on read and keep the read deadline", func(t *testing.T) {
		server, _ := tcpPair(t)
		conn := NewWatchedConnection(server)
		if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			t.Fatalf("setting read deadline: %v", err)
		}
		conn.Watch(func() { t.Error("watch reported a deadline as a closed client") })

		start := time.Now()
		_, err := conn.Read(make([]byte, 1))

		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("wanted: %v\ngot: %v", os.ErrDeadlineExceeded, err)
		}
		if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
			t.Fatalf("read returned after %s, before the caller's deadline", elapsed)
		}
	})

	t.Run("should not report an expired deadline as a closed client", func(t *testing.T) {
		server, client := tcpPair(t)
		conn := NewWatchedConnection(server)
		if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			t.Fatalf("setting read deadline: %v", err)
		}
		conn.Watch(func() { t.Error("watch reported a deadline as a closed client") })
		time.Sleep(200 * time.Millisecond)

		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			t.Fatalf("clearing read deadline: %v", err)
		}
		if _, err := client.Write([]byte("x")); err != nil {
			t.Fatalf("writing: %v", err)
		}
		got := make([]byte, 1)
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		if string(got) != "x" {
			t.Fatalf("wanted: %q\ngot: %q", "x", got)
		}
	})
}
