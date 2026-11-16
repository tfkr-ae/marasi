// Package shim is the conformance shim: the harness component a
// third-party tester connects to when it has no proxy option. For every
// connection it accepts, it connects to the target, straight away in the
// direct run or through the proxy with CONNECT in a proxied run, splices
// the two, and captures the bytes in each direction with timestamps and how
// the connection ended. It can add TLS to the target for a plaintext
// tester, or only tunnel for a tester that does its own TLS. See
// docs/adr/2026-11-16-conformance-testers-are-independent-and-reach-marasi-through-a-shim.md.
package shim

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

// End is how a connection ended, seen from the tester's side.
type End string

// Connection ends. The values are the h1 recording's.
const (
	// EndOpen: the target kept the connection open until the tester left
	// and the shim's settle time ran out.
	EndOpen End = "open"
	// EndClosed: the target, or the proxy, closed it before that.
	EndClosed End = "closed"
	// EndReset: the target, or the proxy, reset it before that.
	EndReset End = "reset"
)

// Options says where the shim sends each connection.
type Options struct {
	// Target is the target's authority, host:port: what a proxied run
	// sends CONNECT for.
	Target string
	// Dial is the address the direct run dials; empty means Target.
	Dial string
	// Proxy is the proxy listener, host:port; empty means the direct run.
	Proxy string
	// TLS, when set, makes the shim the TLS client to the target, inside
	// the tunnel in a proxied run, so a plaintext tester tests a TLS
	// target. Nil means the shim only tunnels bytes.
	TLS *tls.Config
	// Listen is the address the shim listens on; empty means 127.0.0.1:0.
	Listen string
	// Listener, when set, is used instead of Listen, for a caller that
	// needs the shim's port before it knows the target.
	Listener net.Listener
	// Settle is how long the shim keeps watching the target after the
	// tester leaves, so a close that comes just after the last response
	// counts as the target's; zero means 250ms.
	Settle time.Duration
	// Timeout bounds the dial, CONNECT and TLS handshake; zero means 5s.
	Timeout time.Duration
}

// Chunk is one read in one direction.
type Chunk struct {
	At       time.Duration `json:"at"` // since the tester connected
	ToServer bool          `json:"to_server"`
	Data     []byte        `json:"data"`
}

// Capture is one connection the shim accepted.
type Capture struct {
	Accepted time.Time `json:"accepted"`
	// Tunnel is the start line of the proxy's answer to CONNECT; empty in
	// the direct run. The chunks start after the answer, unless it refused.
	Tunnel string  `json:"tunnel,omitempty"`
	Chunks []Chunk `json:"chunks"`
	End    End     `json:"end"`
	// Err is why the shim could not reach the target: a failed dial,
	// CONNECT or TLS handshake. The tester's connection was closed.
	Err string `json:"error,omitempty"`
}

// Bytes returns the capture's bytes in each direction: tester to target
// and target to tester. With TLS they are the plaintext.
func (c Capture) Bytes() (toServer, toClient []byte) {
	for _, chunk := range c.Chunks {
		if chunk.ToServer {
			toServer = append(toServer, chunk.Data...)
		} else {
			toClient = append(toClient, chunk.Data...)
		}
	}
	return toServer, toClient
}

// Shim accepts testers' connections until Close.
type Shim struct {
	options  Options
	listener net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    []*connection
}

// Option defaults.
const (
	// DefaultSettle is how long the shim watches the target after the
	// tester leaves, to see whether the target closes the connection.
	DefaultSettle = 250 * time.Millisecond
	// DefaultTimeout bounds the dial, CONNECT and TLS handshake.
	DefaultTimeout = 5 * time.Second
)

// Start listens and serves every connection in the background.
func Start(options Options) (*Shim, error) {
	if options.Listen == "" {
		options.Listen = "127.0.0.1:0"
	}
	if options.Settle == 0 {
		options.Settle = DefaultSettle
	}
	if options.Timeout == 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Dial == "" {
		options.Dial = options.Target
	}
	listener := options.Listener
	if listener == nil {
		var err error
		if listener, err = net.Listen("tcp", options.Listen); err != nil {
			return nil, err
		}
	}
	s := &Shim{options: options, listener: listener}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Address is the shim's listener, host:port.
func (s *Shim) Address() string { return s.listener.Addr().String() }

// Port is the port of the shim's listener.
func (s *Shim) Port() string {
	_, port, _ := net.SplitHostPort(s.Address())
	return port
}

// Close stops accepting, waits for every connection to end and returns
// the captures in the order the tester connected. A connection the tester
// still holds after a few seconds is closed and ends open.
func (s *Shim) Close() []Capture {
	s.listener.Close()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.options.Timeout + s.options.Settle):
		s.mu.Lock()
		for _, c := range s.conns {
			c.tester.Close()
		}
		s.mu.Unlock()
		<-done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	captures := make([]Capture, len(s.conns))
	for i, c := range s.conns {
		captures[i] = c.capture
	}
	return captures
}

func (s *Shim) accept() {
	defer s.wg.Done()
	for {
		tester, err := s.listener.Accept()
		if err != nil {
			return
		}
		c := &connection{tester: tester, capture: Capture{Accepted: time.Now(), Chunks: []Chunk{}}}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(c)
		}()
	}
}

type connection struct {
	tester  net.Conn
	mu      sync.Mutex
	capture Capture
}

func (c *connection) add(toServer bool, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.capture.Chunks = append(c.capture.Chunks, Chunk{At: time.Since(c.capture.Accepted), ToServer: toServer, Data: bytes.Clone(data)})
}

func (s *Shim) serve(c *connection) {
	defer c.tester.Close()
	up, early, err := s.reach(c)
	if err != nil {
		c.capture.Err = err.Error()
		c.capture.End = EndClosed
		return
	}
	defer up.Close()
	if len(early) > 0 {
		c.add(false, early)
		_, _ = c.tester.Write(early)
	}

	// Target to tester. Its end is the target's close or reset, passed on
	// to the tester as the same.
	ended := make(chan End, 1)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := up.Read(buf)
			if n > 0 {
				c.add(false, buf[:n])
				_, _ = c.tester.Write(buf[:n])
			}
			if err != nil {
				end := endOf(err)
				if end == EndReset {
					reset(c.tester)
				} else if tcp, ok := c.tester.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
				ended <- end
				return
			}
		}
	}()

	// Tester to target, until the tester leaves.
	buf := make([]byte, 32<<10)
	for {
		n, err := c.tester.Read(buf)
		if n > 0 {
			c.add(true, buf[:n])
			_, _ = up.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	select {
	case end := <-ended:
		c.capture.End = end
	case <-time.After(s.options.Settle):
		c.capture.End = EndOpen
		up.Close()
		<-ended
	}
}

// reach connects to the target, through the proxy with CONNECT in a
// proxied run, and adds TLS when the options ask. early is what the
// tester must see before anything the target sends: a refused CONNECT's
// answer, or bytes the proxy sent after its answer.
func (s *Shim) reach(c *connection) (up net.Conn, early []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.Timeout)
	defer cancel()
	address := s.options.Dial
	if s.options.Proxy != "" {
		address = s.options.Proxy
	}
	var dialer net.Dialer
	if up, err = dialer.DialContext(ctx, "tcp", address); err != nil {
		return nil, nil, fmt.Errorf("dialing %s: %w", address, err)
	}
	if s.options.Proxy != "" {
		var answer []byte
		if answer, early, err = connect(up, s.options.Target, s.options.Timeout); err != nil {
			up.Close()
			return nil, nil, err
		}
		line, _, _ := bytes.Cut(answer, []byte("\r\n"))
		c.capture.Tunnel = string(line)
		if !bytes.Contains(line, []byte(" 200")) {
			// The tester sees the refusal as the target's answer.
			return up, append(answer, early...), nil
		}
	}
	if s.options.TLS == nil {
		return up, early, nil
	}
	if len(early) > 0 {
		up.Close()
		return nil, nil, fmt.Errorf("the proxy sent %q after its answer to CONNECT, before TLS", early)
	}
	conn := tls.Client(up, s.options.TLS)
	if err := conn.HandshakeContext(ctx); err != nil {
		up.Close()
		return nil, nil, fmt.Errorf("shaking hands over TLS as %s: %w", s.options.TLS.ServerName, err)
	}
	return conn, nil, nil
}

// connect sends CONNECT for target and reads the answer head. rest is
// anything after it.
func connect(conn net.Conn, target string, timeout time.Duration) (answer, rest []byte, err error) {
	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return nil, nil, fmt.Errorf("sending CONNECT: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})
	buf := make([]byte, 4<<10)
	for {
		if i := bytes.Index(answer, []byte("\r\n\r\n")); i >= 0 {
			return answer[:i+4], bytes.Clone(answer[i+4:]), nil
		}
		n, err := conn.Read(buf)
		answer = append(answer, buf[:n]...)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the answer to CONNECT %q: %w", answer, err)
		}
	}
}

// endOf maps the error that ended reading from the target to an End.
func endOf(err error) End {
	if errors.Is(err, syscall.ECONNRESET) {
		return EndReset
	}
	return EndClosed
}

// reset closes conn with a TCP reset.
func reset(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	conn.Close()
}
