package websocket

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// ErrConnectionClosed is returned when operating on a closed connection.
var ErrConnectionClosed = errors.New("websocket connection is closed")

const (
	// DirectionFromClient marks a message originating from the client.
	DirectionFromClient = "client"
	// DirectionFromServer marks a message originating from the server.
	DirectionFromServer   = "server"
	closeHandshakeTimeout = time.Second
)

// ProcessFunc processes a live message before it is forwarded.
type ProcessFunc func(*Message) error

// HijackedConn adapts a hijacked net.Conn and its buffered reader/writer so
// WebSocket frame I/O uses the TLS-aware buffered stream.
type HijackedConn struct {
	net.Conn
	// ReadWriter is the buffered stream returned by session hijacking.
	ReadWriter *bufio.ReadWriter
}

// Read reads from the buffered reader.
func (c *HijackedConn) Read(p []byte) (int, error) {
	return c.ReadWriter.Read(p)
}

// Write writes to the buffered writer and flushes.
func (c *HijackedConn) Write(p []byte) (int, error) {
	n, err := c.ReadWriter.Write(p)
	if err != nil {
		return n, err
	}
	if err := c.ReadWriter.Flush(); err != nil {
		return n, err
	}
	return n, nil
}

// Connection is a live bidirectional WebSocket relay between client and upstream.
type Connection struct {
	// ID uniquely identifies the live connection.
	ID uuid.UUID
	// RequestID is the HTTP upgrade request that opened the connection.
	RequestID uuid.UUID

	clientReader io.Reader
	client       io.ReadWriteCloser
	upstream     io.ReadWriteCloser

	clientWriteMu   sync.Mutex
	upstreamWriteMu sync.Mutex

	closeOnce sync.Once
	closeErr  error
	done      chan struct{}
	closing   atomic.Bool
	running   atomic.Bool

	mu          sync.RWMutex
	closeCode   int
	closeReason string

	process    ProcessFunc
	onShutdown func(uuid.UUID)
}

// NewConnection creates a connection that relays between client and upstream.
// clientReader may differ from client when buffered data must be consumed first.
func NewConnection(
	id uuid.UUID,
	requestID uuid.UUID,
	clientReader io.Reader,
	client io.ReadWriteCloser,
	upstream io.ReadWriteCloser,
	process ProcessFunc,
) *Connection {
	return &Connection{
		ID:           id,
		RequestID:    requestID,
		clientReader: clientReader,
		client:       client,
		upstream:     upstream,
		process:      process,
		done:         make(chan struct{}),
	}
}

func (c *Connection) writeToClient(frame Frame) error {
	c.clientWriteMu.Lock()
	defer c.clientWriteMu.Unlock()

	if err := WriteFrame(c.client, frame, false); err != nil {
		return fmt.Errorf("writing websocket frame to client: %w", err)
	}

	return nil
}

func (c *Connection) writeToUpstream(frame Frame) error {
	c.upstreamWriteMu.Lock()
	defer c.upstreamWriteMu.Unlock()

	if err := WriteFrame(c.upstream, frame, true); err != nil {
		return fmt.Errorf("writing websocket frame upstream: %w", err)
	}

	return nil
}

func (c *Connection) handleFrame(
	frame Frame,
	source string,
	writeFrame func(Frame) error,
) (stop bool, err error) {
	message, err := NewMessage(c.ID, c.RequestID, source, frame)
	if err != nil {
		return false, fmt.Errorf("creating websocket message: %w", err)
	}

	if c.process != nil {
		if err := c.process(message); err != nil {
			return false, fmt.Errorf("processing websocket message: %w", err)
		}
	}

	isClose := frame.Opcode == OpClose ||
		message.Frame.Opcode == OpClose

	if isClose {
		code, reason, err := ParseClosePayload(message.Frame.Payload)
		if err != nil {
			return false, fmt.Errorf(
				"parsing websocket close payload: %w",
				err,
			)
		}

		c.setCloseDetails(code, reason)
	}

	if !message.Dropped {
		if err := writeFrame(message.Frame); err != nil {
			return false, fmt.Errorf(
				"forwarding websocket frame: %w",
				err,
			)
		}
	}

	return isClose, nil
}

func (c *Connection) relayFrames(
	reader io.Reader,
	writeFrame func(Frame) error,
	source string,
) error {
	for {
		select {
		case <-c.done:
			return nil
		default:
		}

		frame, err := ReadFrame(reader)
		if err != nil {
			if c.closing.Load() {
				return nil
			}
			return fmt.Errorf("reading websocket frame: %w", err)
		}
		if c.closing.Load() && frame.Opcode == OpClose {
			if source == DirectionFromClient {
				continue
			}
			stop, err := c.handleFrame(frame, source, func(Frame) error { return nil })
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			continue
		}

		stop, err := c.handleFrame(frame, source, writeFrame)
		if err != nil {
			return err
		}

		if stop {
			return nil
		}
	}
}

func (c *Connection) shutdown() error {
	c.closeOnce.Do(func() {
		if c.onShutdown != nil {
			c.onShutdown(c.ID)
		}
		close(c.done)

		clientErr := c.client.Close()
		upstreamErr := c.upstream.Close()

		c.closeErr = errors.Join(clientErr, upstreamErr)
	})

	return c.closeErr
}

// SetShutdownHandler registers a callback invoked once when the connection shuts down.
func (c *Connection) SetShutdownHandler(handler func(uuid.UUID)) {
	c.onShutdown = handler
}

// Run starts bidirectional frame relay and blocks until the connection closes.
func (c *Connection) Run() error {
	c.running.Store(true)
	defer c.running.Store(false)

	results := make(chan error, 2)

	go func() {
		results <- c.relayFrames(
			c.clientReader,
			c.writeToUpstream,
			DirectionFromClient,
		)
	}()

	go func() {
		results <- c.relayFrames(
			c.upstream,
			c.writeToClient,
			DirectionFromServer,
		)
	}()

	firstErr := <-results
	locallyClosing := c.closing.Load()

	shutdownErr := c.shutdown()

	<-results
	if locallyClosing {
		return nil
	}

	return errors.Join(firstErr, shutdownErr)
}

// SendToClient sends a frame toward the client through the process path.
func (c *Connection) SendToClient(frame Frame) error {
	if c.isClosed() {
		return ErrConnectionClosed
	}

	_, err := c.handleFrame(
		frame,
		DirectionFromServer,
		c.writeToClient,
	)
	if err != nil {
		return fmt.Errorf("sending websocket frame to client: %w", err)
	}

	return nil
}

// Inject sends a frame as if it originated from direction.
// Injected messages are marked with metadata["injected"] = true.
func (c *Connection) Inject(direction string, opcode int, payload []byte) error {
	if c.isClosed() {
		return ErrConnectionClosed
	}

	var writeFrame func(Frame) error
	switch direction {
	case DirectionFromClient:
		writeFrame = c.writeToUpstream
	case DirectionFromServer:
		writeFrame = c.writeToClient
	default:
		return fmt.Errorf("invalid websocket inject direction: %q", direction)
	}

	message, err := NewMessage(c.ID, c.RequestID, direction, Frame{
		Fin:     true,
		Opcode:  opcode,
		Payload: append([]byte(nil), payload...),
	})
	if err != nil {
		return fmt.Errorf("creating injected websocket message: %w", err)
	}
	message.Metadata["injected"] = true

	if c.process != nil {
		if err := c.process(message); err != nil {
			return fmt.Errorf("processing injected websocket message: %w", err)
		}
	}
	if message.Dropped {
		return nil
	}

	if err := writeFrame(message.Frame); err != nil {
		return fmt.Errorf("writing injected websocket frame: %w", err)
	}

	return nil
}

// Close sends close frames to both peers and shuts the connection down.
func (c *Connection) Close(code int, reason string) error {
	if c.isClosed() {
		return ErrConnectionClosed
	}

	payload, err := EncodeClosePayload(code, reason)
	if err != nil {
		return fmt.Errorf("encoding websocket close payload: %w", err)
	}

	frame := Frame{
		Fin:     true,
		Opcode:  OpClose,
		Payload: payload,
	}

	c.closing.Store(true)
	c.setCloseDetails(code, reason)

	clientErr := c.writeToClient(frame)
	upstreamErr := c.sendGeneratedClose(frame, DirectionFromClient, c.writeToUpstream)
	if c.running.Load() {
		go func() {
			timer := time.NewTimer(closeHandshakeTimeout)
			defer timer.Stop()
			select {
			case <-c.done:
			case <-timer.C:
				_ = c.shutdown()
			}
		}()
		return errors.Join(clientErr, upstreamErr)
	}
	shutdownErr := c.shutdown()

	return errors.Join(
		clientErr,
		upstreamErr,
		shutdownErr,
	)
}

func (c *Connection) sendGeneratedClose(
	frame Frame,
	direction string,
	writeFrame func(Frame) error,
) error {
	message, err := NewMessage(c.ID, c.RequestID, direction, frame)
	if err != nil {
		return fmt.Errorf("creating generated websocket close message: %w", err)
	}
	message.Metadata["generated"] = true

	if c.process != nil {
		if err := c.process(message); err != nil {
			return fmt.Errorf("processing generated websocket close message: %w", err)
		}
	}

	return writeFrame(message.Frame)
}

// SendToUpstream sends a frame toward the upstream server through the process path.
func (c *Connection) SendToUpstream(frame Frame) error {
	if c.isClosed() {
		return ErrConnectionClosed
	}

	_, err := c.handleFrame(
		frame,
		DirectionFromClient,
		c.writeToUpstream,
	)
	if err != nil {
		return fmt.Errorf("sending websocket frame upstream: %w", err)
	}

	return nil
}

// CloseDetails returns the most recent close code and reason observed on the connection.
func (c *Connection) CloseDetails() (code int, reason string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.closeCode, c.closeReason
}

func (c *Connection) setCloseDetails(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closeCode = code
	c.closeReason = reason
}

func (c *Connection) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
