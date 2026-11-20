package h1origin

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance/h1"
)

// Case is one origin fixture case: the requests the harness client sends
// on one connection and the script the origin follows on each connection it
// accepts.
type Case struct {
	ID  string // case ID before the client approach
	RFC string // the RFC rule the case exercises
	// Requests are raw origin-form requests, sent in order, each after the
	// response to the one before. "{authority}" is replaced by the origin's
	// host:port. The client rewrites the request target to absolute form
	// for the absolute approach through a proxy. "{proxy}" is replaced by
	// the proxy listener's host:port; a request that names it is sent to
	// that address in absolute form, and inside a tunnel by its Host.
	Requests []string
	Script   []Step
}

// StepKind names what a script step does.
type StepKind string

// Step kinds.
const (
	StepAwait     StepKind = "await"      // wait for the next whole request
	StepSend      StepKind = "send"       // write bytes
	StepDelay     StepKind = "delay"      // pause
	StepHalfClose StepKind = "half-close" // close the write side
	StepClose     StepKind = "close"      // close the connection
	StepReset     StepKind = "reset"      // reset the connection
)

// Step is one step of an origin's response script.
type Step struct {
	Kind  StepKind
	Bytes string        // StepSend
	Delay time.Duration // StepDelay
}

// Await waits for the next whole request.
func Await() Step { return Step{Kind: StepAwait} }

// Send writes bytes as one write.
func Send(bytes string) Step { return Step{Kind: StepSend, Bytes: bytes} }

// Delay pauses the script.
func Delay(d time.Duration) Step { return Step{Kind: StepDelay, Delay: d} }

// HalfClose closes the origin's write side and keeps reading.
func HalfClose() Step { return Step{Kind: StepHalfClose} }

// Close closes the connection.
func Close() Step { return Step{Kind: StepClose} }

// Reset resets the connection.
func Reset() Step { return Step{Kind: StepReset} }

// origin is a running origin fixture: one listener for one case, recording
// every connection it accepts.
type origin struct {
	listener net.Listener
	script   []Step
	timeout  time.Duration // how long Await waits

	mu          sync.Mutex
	connections []*originConn
	wg          sync.WaitGroup
}

type originConn struct {
	conn     net.Conn
	received []byte
	sent     []byte
	end      h1.End // empty while open
}

// startOrigin starts an origin; with config set it serves TLS.
func startOrigin(listen string, script []Step, timeout time.Duration, config *tls.Config) (*origin, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(listen, "0"))
	if err != nil {
		return nil, err
	}
	if config != nil {
		listener = tls.NewListener(listener, config)
	}
	o := &origin{listener: listener, script: script, timeout: timeout}
	o.wg.Go(o.accept)
	return o, nil
}

func (o *origin) port() string {
	_, port, _ := net.SplitHostPort(o.listener.Addr().String())
	return port
}

func (o *origin) accept() {
	for {
		conn, err := o.listener.Accept()
		if err != nil {
			return
		}
		c := &originConn{conn: conn}
		o.mu.Lock()
		o.connections = append(o.connections, c)
		o.mu.Unlock()
		o.wg.Go(func() { o.serve(c) })
	}
}

// serve runs the script on one connection, then reads until it ends.
func (o *origin) serve(c *originConn) {
	requests := 0
	buf := make([]byte, 32<<10)
	read := func(deadline time.Time) error {
		_ = c.conn.SetReadDeadline(deadline)
		n, err := c.conn.Read(buf)
		o.mu.Lock()
		c.received = append(c.received, buf[:n]...)
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) && c.end == "" {
			c.end = readEnd(err)
		}
		o.mu.Unlock()
		return err
	}
	for _, step := range o.script {
		switch step.Kind {
		case StepAwait:
			requests++
			deadline := time.Now().Add(o.timeout)
			for {
				o.mu.Lock()
				parsed, _ := h1.ParseRequests(c.received, false)
				o.mu.Unlock()
				if len(parsed) >= requests {
					break
				}
				if err := read(deadline); err != nil {
					// The request never came whole; stop the script and
					// record whatever follows.
					goto drain
				}
			}
		case StepSend:
			n, _ := io.WriteString(c.conn, step.Bytes)
			o.mu.Lock()
			c.sent = append(c.sent, step.Bytes[:n]...)
			o.mu.Unlock()
		case StepDelay:
			time.Sleep(step.Delay)
		case StepHalfClose:
			// Over TLS this sends close_notify first.
			if conn, ok := c.conn.(interface{ CloseWrite() error }); ok {
				_ = conn.CloseWrite()
			}
		case StepClose:
			o.finish(c, h1.EndClosedBy)
			_ = c.conn.Close()
			return
		case StepReset:
			o.finish(c, h1.EndResetBy)
			raw := c.conn
			if conn, ok := raw.(*tls.Conn); ok {
				raw = conn.NetConn()
			}
			if tcp, ok := raw.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
			_ = raw.Close()
			return
		}
	}
drain:
	for {
		if err := read(time.Time{}); err != nil {
			return
		}
	}
}

// finish records the end of a connection the script ended.
func (o *origin) finish(c *originConn, end h1.End) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if c.end == "" {
		c.end = end
	}
}

// awaitEnds waits up to d for every connection to end.
func (o *origin) awaitEnds(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		open := slices.ContainsFunc(o.connections, func(c *originConn) bool { return c.end == "" })
		o.mu.Unlock()
		if !open {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// snapshot records every connection as it is now; one still open ends
// "open". normalise rewrites the raw bytes before they are parsed.
func (o *origin) snapshot(normalise func(h1.Raw) h1.Raw) []h1.Connection {
	o.mu.Lock()
	defer o.mu.Unlock()
	connections := make([]h1.Connection, 0, len(o.connections))
	for _, c := range o.connections {
		end := c.end
		if end == "" {
			end = h1.EndOpen
		}
		connections = append(connections, h1.NewConnection(normalise(h1.Raw{
			ToServer: append([]byte(nil), c.received...),
			ToClient: append([]byte(nil), c.sent...),
		}), end))
	}
	return connections
}

// stop closes the listener and every connection.
func (o *origin) stop() {
	_ = o.listener.Close()
	o.mu.Lock()
	for _, c := range o.connections {
		_ = c.conn.Close()
	}
	o.mu.Unlock()
	o.wg.Wait()
}

// readEnd names how a read error ended a connection.
func readEnd(err error) h1.End {
	if errors.Is(err, syscall.ECONNRESET) {
		return h1.EndReset
	}
	return h1.EndClosed
}
