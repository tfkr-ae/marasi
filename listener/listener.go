package listener

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// connWrapper wraps a net.Conn and uses io.MultiReader to prepend the first byte
type connWrapper struct {
	net.Conn
	io.Reader
	onClose   func()
	closeOnce sync.Once
}

// connWrapper.Read method will read from the io.Reader instead of the net.Conn
func (cw *connWrapper) Read(b []byte) (int, error) {
	return cw.Reader.Read(b)
}

func (cw *connWrapper) Close() error {
	err := cw.Conn.Close()
	if cw.onClose != nil {
		cw.closeOnce.Do(cw.onClose)
	}
	return err
}

// NewTrackedConnection uses the listener's connection wrapper to notify the
// owner when its handler closes the socket. Raw socket closure does not notify.
func NewTrackedConnection(conn net.Conn, onClose func()) net.Conn {
	return &connWrapper{Conn: conn, Reader: conn, onClose: onClose}
}

// ProtocolMuxListener wraps net.Listener and inspects the incoming connection to determine the protocol.
// Each connection is inspected on its own goroutine, so a client that connects
// and sends nothing does not hold up the connections accepted after it.
type ProtocolMuxListener struct {
	net.Listener
	TLSConfig *tls.Config
	// WrapConn wraps each accepted connection before protocol inspection and TLS.
	// When non-nil, it must return a non-nil connection.
	WrapConn func(net.Conn) net.Conn

	startOnce sync.Once
	closeOnce sync.Once
	// inspected carries each inspected connection, or the error that refused it, to Accept.
	inspected chan acceptResult
	// closed is closed by Close, and stops the accept loop and pending inspections.
	closed chan struct{}
	// stopped is closed when the accept loop ends; stopErr is why.
	stopped chan struct{}
	stopErr error
}

type acceptResult struct {
	conn net.Conn
	err  error
}

func NewProtocolMuxListener(listener net.Listener, tlsConfig *tls.Config) *ProtocolMuxListener {
	return &ProtocolMuxListener{
		Listener:  listener,
		TLSConfig: tlsConfig,
		inspected: make(chan acceptResult),
		closed:    make(chan struct{}),
		stopped:   make(chan struct{}),
	}
}

// Accept returns the next inspected connection, or the error that refused one.
// Once the underlying listener fails for good, every call returns that error.
func (l *ProtocolMuxListener) Accept() (net.Conn, error) {
	l.startOnce.Do(func() { go l.acceptLoop() })
	select {
	case result := <-l.inspected:
		return result.conn, result.err
	case <-l.stopped:
		return nil, l.stopErr
	}
}

// Close stops accepting, and closes connections still being inspected.
func (l *ProtocolMuxListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

func (l *ProtocolMuxListener) acceptLoop() {
	for {
		rawConnection, err := l.Listener.Accept()
		if err != nil {
			err = fmt.Errorf("accepting connection: %w", err)
			if errors.Is(err, net.ErrClosed) {
				l.stopErr = err
				close(l.stopped)
				return
			}
			if !l.deliver(acceptResult{err: err}) {
				return
			}
			continue
		}
		if l.WrapConn != nil {
			rawConnection = l.WrapConn(rawConnection)
		}
		go func() {
			conn, err := l.inspect(rawConnection)
			if !l.deliver(acceptResult{conn: conn, err: err}) && conn != nil {
				conn.Close()
			}
		}()
	}
}

// deliver hands result to Accept, and reports false when the listener closed
// or stopped accepting first.
func (l *ProtocolMuxListener) deliver(result acceptResult) bool {
	select {
	case l.inspected <- result:
		return true
	case <-l.closed:
		return false
	case <-l.stopped:
		return false
	}
}

// inspect peeks at a new connection's first bytes and completes the TLS
// handshake when the client speaks TLS.
func (l *ProtocolMuxListener) inspect(rawConnection net.Conn) (net.Conn, error) {
	bufferedReader := bufio.NewReader(rawConnection)

	err := rawConnection.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		rawConnection.Close()
		return nil, fmt.Errorf("setting read deadline for peak: %w", err)
	}

	peekedBytes, err := bufferedReader.Peek(5)

	if err := rawConnection.SetReadDeadline(time.Time{}); err != nil {
		rawConnection.Close()
		return nil, fmt.Errorf("clearing read deadline after peek: %w", err)
	}
	if err != nil {
		if err != bufio.ErrBufferFull {
			rawConnection.Close()
			return nil, fmt.Errorf("peaking initial bytes: %w", err)
		}
	}

	isTLS := len(peekedBytes) >= 2 && peekedBytes[0] == 0x16 && peekedBytes[1] == 0x03

	if isTLS {
		tlsConn := tls.Server(&connWrapper{
			Conn:   rawConnection,
			Reader: bufferedReader,
		}, l.TLSConfig)

		err := rawConnection.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err != nil {
			tlsConn.Close()
			return nil, fmt.Errorf("setting read deadline for handshake: %w", err)
		}

		err = tlsConn.Handshake()
		if err != nil {
			rawConnection.SetReadDeadline(time.Time{})
			tlsConn.Close()
			return nil, fmt.Errorf("performing tls handshake: %w", err)
		}
		err = rawConnection.SetReadDeadline(time.Time{})
		if err != nil {
			tlsConn.Close()
			return nil, fmt.Errorf("clearing read deadline after handshake: %w", err)
		}
		return tlsConn, nil
	}
	return &connWrapper{
		Conn:   rawConnection,
		Reader: bufferedReader,
	}, nil
}

// MarasiListener wraps net.Listener to be resilient, recoverable errors are handled gracefully
// Use case is for it to wrap ProtocolMuxListener for Marasi usage
type MarasiListener struct {
	net.Listener
}

func NewMarasiListener(listenerToWrap net.Listener) *MarasiListener {
	return &MarasiListener{Listener: listenerToWrap}
}

// MarasiListnener Accept will gracefully handle recoverable errors and continue without crashing the server
func (l *MarasiListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			// If the listener was closed, this is a fatal error. Propagate it.
			if errors.Is(err, net.ErrClosed) {
				return nil, err
			}

			// For any other error, log it and continue to the next connection attempt.
			// TODO this will need a clean mechanism to log to the DB for applications to consume
			log.Printf("Recoverable listener error, connection rejected: %v", err)
			continue
		}
		return conn, nil
	}
}

// ClosingListener makes listener errors temporary while its owner is shutting down.
type ClosingListener struct {
	net.Listener
	isClosing func() bool
}

func NewClosingListener(listener net.Listener, isClosing func() bool) *ClosingListener {
	return &ClosingListener{
		Listener:  listener,
		isClosing: isClosing,
	}
}

func (l *ClosingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil && l.isClosing() {
		return nil, temporaryListenerError{err: err}
	}
	return conn, err
}

type temporaryListenerError struct {
	err error
}

func (e temporaryListenerError) Error() string   { return e.err.Error() }
func (e temporaryListenerError) Unwrap() error   { return e.err }
func (e temporaryListenerError) Timeout() bool   { return false }
func (e temporaryListenerError) Temporary() bool { return true }
