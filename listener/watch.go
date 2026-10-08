package listener

import (
	"errors"
	"net"
	"os"
	"sync"
	"time"
)

// maxWatchedBytes bounds what a watch holds for the next Read. A client that sends
// more while its request is in flight is pipelining; the watch stops and does not report it.
const maxWatchedBytes = 4096

// aLongTimeAgo is a read deadline that unblocks a watch immediately.
var aLongTimeAgo = time.Unix(1, 0)

// WatchedConn reports a client that closes its connection while nobody is reading it.
// Martian stops reading a client connection while modifiers run, so a closed client
// is not otherwise noticed until the next request read.
type WatchedConn struct {
	net.Conn

	mu           sync.Mutex
	readDeadline time.Time
	// done is closed when the running watch returns. Nil when no watch is pending.
	done chan struct{}
	// pending holds bytes the watch read. Read returns them before reading the socket.
	pending []byte
	// err is the read error that ended a watch. Read returns it after pending.
	err    error
	closed bool
}

// NewWatchedConnection wraps conn. Wrap the raw socket, beneath protocol inspection and TLS.
func NewWatchedConnection(conn net.Conn) *WatchedConn {
	return &WatchedConn{Conn: conn}
}

// Watch reads conn in the background until the next Read and calls onClosed once if the
// client closes the connection or it fails. It calls onClosed at once when an earlier
// watch already saw the close. A deadline that expires while watched is not a close.
// A watch that starts during a Read is safe: net.Conn serializes reads on a socket,
// and every Read ends the watch and returns its bytes before reading the socket.
func (c *WatchedConn) Watch(onClosed func()) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		onClosed()
		return
	}
	if c.done != nil || c.closed {
		c.mu.Unlock()
		return
	}
	done := make(chan struct{})
	c.done = done
	c.mu.Unlock()
	go c.watch(done, onClosed)
}

func (c *WatchedConn) watch(done chan struct{}, onClosed func()) {
	defer close(done)
	buf := make([]byte, 512)
	for {
		n, err := c.Conn.Read(buf)
		c.mu.Lock()
		c.pending = append(c.pending, buf[:n]...)
		full := len(c.pending) >= maxWatchedBytes
		reportClose := err != nil && !errors.Is(err, os.ErrDeadlineExceeded) && !c.closed
		if reportClose {
			c.err = err
		}
		c.mu.Unlock()
		if reportClose {
			onClosed()
		}
		if err != nil || full {
			return
		}
	}
}

// Read ends a pending watch, then returns watched bytes, the watch's error, or a socket read.
func (c *WatchedConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	if done := c.done; done != nil {
		c.done = nil
		deadline := c.readDeadline
		c.mu.Unlock()
		c.Conn.SetReadDeadline(aLongTimeAgo)
		<-done
		c.Conn.SetReadDeadline(deadline)
		c.mu.Lock()
	}
	if len(c.pending) > 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		c.mu.Unlock()
		return n, nil
	}
	if err := c.err; err != nil {
		c.mu.Unlock()
		return 0, err
	}
	c.mu.Unlock()
	return c.Conn.Read(b)
}

// SetDeadline records the read deadline so Read can restore it after ending a watch.
func (c *WatchedConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

// SetReadDeadline records the read deadline so Read can restore it after ending a watch.
func (c *WatchedConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

// Close closes the connection. A watch it ends does not report a closed client.
func (c *WatchedConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.Conn.Close()
}
