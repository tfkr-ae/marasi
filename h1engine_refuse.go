package marasi

import (
	"bufio"
	"errors"
	"io"
	"time"

	"github.com/tfkr-ae/marasi/internal/h1"
	"github.com/tfkr-ae/marasi/listener"
)

// A request whose length cannot be framed safely is refused with a 400 proxy
// error response, its pair is stored, and the connection closes
// (docs/adr/2026-11-17-ambiguous-request-framing-is-refused-with-a-400.md).

// refusalLinger bounds how long a refused connection waits for the client to
// stop sending before it closes, and refusalDrain how much it reads meanwhile.
const (
	refusalLinger = 500 * time.Millisecond
	refusalDrain  = 256 << 10
)

// readWireBody reads a request's body as framed. A framing the head already
// refused reads nothing, and a chunked body that turns out to be ambiguous
// (a bad chunk size or chunk end) is refused too: *refused is set to its
// *h1.FramingError, and the body is empty, because the bytes after the head
// cannot be trusted to be this request's.
func readWireBody(br *bufio.Reader, framing h1.Framing, refused *error, threshold int64) (*h1.Body, *h1Passing, error) {
	if *refused != nil {
		return &h1.Body{}, nil, nil
	}
	body, passing, err := readBodyOrPass(br, framing, threshold)
	var framingErr *h1.FramingError
	if errors.As(err, &framingErr) {
		*refused = err
		return &h1.Body{}, nil, nil
	}
	return body, passing, err
}

// drain half-closes a refused connection and discards what the client still
// sends, for a short while, so the close that follows does not reset the
// connection before the client has read the 400 (RFC 9112 §9.6).
func (c *h1Conn) drain() {
	if c.hijacked {
		return
	}
	// Every wrapper beneath the engine passes the half-close through
	// (listener.CloseWriter); an in-memory connection cannot half-close.
	listener.CloseWrite(c.conn)
	c.conn.SetReadDeadline(time.Now().Add(refusalLinger))
	io.CopyN(io.Discard, c.br, refusalDrain)
}
