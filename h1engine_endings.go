package marasi

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// How a client connection ends on the new engine
// (docs/adr/2026-11-17-one-origin-connection-per-client-connection.md):
//
//   - a request's head must arrive within headerTimeout, and the next
//     request on a kept-alive connection must start within idleTimeout. The
//     body and the response have no deadline, so a long transfer or a long
//     Checkpoint hold is never cut off;
//   - an origin that closes its connection while the client connection waits
//     for its next request closes the client connection too. The response in
//     flight when an origin closes is finished first (carryPair);
//   - a client that leaves closes the origin connection (end, and sendUpstream
//     while the origin answers);
//   - a body the origin cut short is forwarded as far as it got, then the
//     client connection closes (a reset is passed on as a close, so no byte
//     already forwarded is lost);
//   - a head the codec cannot read is answered 400 and the connection ends.
const (
	// headerTimeout bounds reading a request's head.
	headerTimeout = 60 * time.Second
	// idleTimeout bounds the wait for the next request on a kept-alive
	// client connection.
	idleTimeout = 5 * time.Minute
)

// aLongTimeAgo is a read deadline that ends a blocked read at once.
var aLongTimeAgo = time.Unix(1, 0)

// readHead waits for the connection's next request and reads its head: up to
// idleTimeout for its first byte on a kept-alive connection, then up to
// headerTimeout for the whole head (headerTimeout from the start for a
// connection's first request). The origin connection is watched meanwhile.
// It reports false when the connection should end.
func (c *h1Conn) readHead() (*h1.RequestHead, bool) {
	wait := c.engine.headerTimeout
	if c.kept {
		wait = c.engine.idleTimeout
	}
	if !c.engine.idle(c, wait) {
		return nil, false
	}
	_, err := c.br.Peek(1)
	c.stopWatchingOrigin()
	if err != nil {
		// The client left, went idle too long, the engine is closing,
		// or the origin closed its connection.
		return nil, false
	}
	if c.kept {
		c.engine.setReadDeadline(c, c.engine.headerTimeout)
	}
	head, err := h1.ReadRequestHead(c.br)
	c.engine.busy(c)
	if err != nil {
		switch {
		case errors.Is(err, h1.ErrUnsupportedVersion):
			c.refuse(http.StatusHTTPVersionNotSupported, err)
		case errors.Is(err, h1.ErrMalformedHead), errors.Is(err, h1.ErrHeadTooLarge):
			c.refuse(http.StatusBadRequest, err)
		}
		return nil, false
	}
	// Reading the body and the response has no deadline.
	c.conn.SetReadDeadline(time.Time{})
	return head, true
}

// setReadDeadline gives conn's read d from now, or ends it at once when the
// engine is closing. It holds the lock startClose expires idle reads under, so
// a deadline set here never outlives a close.
func (engine *h1Engine) setReadDeadline(conn *h1Conn, d time.Duration) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closing.Load() {
		conn.conn.SetReadDeadline(time.Now())
		return
	}
	conn.conn.SetReadDeadline(time.Now().Add(d))
}

// watchOrigin reads the origin connection while the client connection waits
// for its next request. An origin that closes its connection, or sends
// anything, ends it: the origin connection is closed and so is the client
// connection, as a direct client would see. An origin a route swap retired is
// exempt; the client connection stays open and its next request redials.
// Called under engine.mu.
func (c *h1Conn) watchOrigin() {
	origin := c.origin
	if origin == nil || origin.retired {
		return
	}
	done := make(chan struct{})
	origin.watch = done
	go func() {
		defer close(done)
		_, err := origin.br.Peek(1)
		c.engine.mu.Lock()
		defer c.engine.mu.Unlock()
		if origin.stopping && errors.Is(err, os.ErrDeadlineExceeded) {
			return
		}
		origin.ended = true
		if !origin.stopping && !origin.retired {
			c.conn.SetReadDeadline(time.Now())
		}
	}()
}

// stopWatchingOrigin ends watchOrigin's read. An origin connection that ended
// meanwhile is closed, so a request racing the origin's close gets a new one.
func (c *h1Conn) stopWatchingOrigin() {
	origin := c.origin
	if origin == nil || origin.watch == nil {
		return
	}
	c.engine.mu.Lock()
	origin.stopping = true
	c.engine.mu.Unlock()
	origin.conn.SetReadDeadline(aLongTimeAgo)
	<-origin.watch
	origin.conn.SetReadDeadline(time.Time{})
	// Still marked idle, so a route swap may read c.origin: change it under
	// the lock.
	c.engine.mu.Lock()
	defer c.engine.mu.Unlock()
	origin.watch, origin.stopping = nil, false
	if origin.ended || origin.retired {
		c.closeOrigin()
	}
}
