package listener

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

// connWrapper wraps a net.Conn and uses io.MultiReader to prepend the first byte
type connWrapper struct {
	net.Conn
	io.Reader
}

// connWrapper.Read method will read from the io.Reader instead of the net.Conn
func (cw *connWrapper) Read(b []byte) (int, error) {
	return cw.Reader.Read(b)
}

// ProtocolMuxListener wraps net.Listener and inspects the incoming connection to determine the protocol
type ProtocolMuxListener struct {
	net.Listener
	TLSConfig *tls.Config
}

func NewProtocolMuxListener(listener net.Listener, tlsConfig *tls.Config) *ProtocolMuxListener {
	return &ProtocolMuxListener{
		Listener:  listener,
		TLSConfig: tlsConfig,
	}
}

func (l *ProtocolMuxListener) Accept() (net.Conn, error) {
	rawConnection, err := l.Listener.Accept()
	if err != nil {
		return nil, fmt.Errorf("accepting connection: %w", err)
	}

	bufferedReader := bufio.NewReader(rawConnection)

	err = rawConnection.SetReadDeadline(time.Now().Add(10 * time.Second))
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
