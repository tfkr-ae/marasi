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
	"github.com/tfkr-ae/marasi/domain"
)

// ErrConnectionClosed is returned when operating on a closed connection.
var ErrConnectionClosed = errors.New("websocket connection is closed")

// errForwarding marks a failure to write a relayed frame to its peer.
var errForwarding = errors.New("forwarding websocket frame")

const (
	// DirectionFromClient marks a message originating from the client.
	DirectionFromClient = "client"
	// DirectionFromServer marks a message originating from the server.
	DirectionFromServer = "server"
	// CloseHandshakeTimeout bounds the local close handshake.
	CloseHandshakeTimeout = time.Second
	// RelayedCloseTimeout bounds a close handshake between the peers. It runs
	// from the first relayed close frame; the peers normally finish it by
	// dropping TCP well before then.
	RelayedCloseTimeout = 5 * time.Second
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

	clientLeg   *leg
	upstreamLeg *leg

	closeOnce    sync.Once
	closeErr     error
	done         chan struct{}
	closedRecord chan domain.WebSocketConnection
	closing      atomic.Bool
	running      atomic.Bool
	// closeRelayed is set once a peer's close frame has been relayed. The
	// relay then keeps both directions open so the reply close reaches the
	// other peer, and tears down when a peer drops TCP.
	closeRelayed        atomic.Bool
	relayedCloseTimeout time.Duration

	mu          sync.RWMutex
	closeCode   int
	closeReason string

	process    ProcessFunc
	onShutdown func(uuid.UUID)

	maxMessageSize int64
	// inflaters decompress each peer's compressed messages, by the
	// direction they come from. They are nil without permessage-deflate.
	inflaters  map[string]*inflater
	stream     StreamFunc
	onProgress func(domain.WebSocketMessageProgress)
	onTooBig   func(MessageTooBig)
	onInflate  func(InflateFailure)
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
		clientLeg:    &leg{w: client, errPrefix: "writing websocket frame to client"},
		upstreamLeg:  &leg{w: upstream, mask: true, errPrefix: "writing websocket frame upstream"},
		process:      process,
		done:         make(chan struct{}),
		closedRecord: make(chan domain.WebSocketConnection, 1),

		relayedCloseTimeout: RelayedCloseTimeout,
		maxMessageSize:      DefaultMaxMessageSize,
	}
}

// SetMaxMessageSize sets the per-message size limit in bytes. A value of zero
// or less keeps the current limit.
func (c *Connection) SetMaxMessageSize(limit int64) {
	if limit > 0 {
		c.maxMessageSize = limit
	}
}

// SetDeflate makes the proxy the permessage-deflate endpoint on both legs
// with the negotiated params (ADR-0028). Each direction gets its own
// inflater and deflater: the client's parameters govern messages from the
// client, inflated from the client and deflated toward the upstream, and the
// server's govern messages from the server. A nil params means no deflate.
// Call it before Run.
func (c *Connection) SetDeflate(params *domain.WebSocketDeflate) {
	if params == nil {
		c.inflaters = nil
		c.upstreamLeg.deflater = nil
		c.clientLeg.deflater = nil
		return
	}
	c.inflaters = map[string]*inflater{
		DirectionFromClient: newInflater(params.ClientNoContextTakeover),
		DirectionFromServer: newInflater(params.ServerNoContextTakeover),
	}
	c.upstreamLeg.deflater = newDeflater(params.ClientNoContextTakeover, params.ClientMaxWindowBits)
	c.clientLeg.deflater = newDeflater(params.ServerNoContextTakeover, params.ServerMaxWindowBits)
}

// SetStreamFunc registers the check that decides, as each data message
// starts, whether the relay may forward its bytes as they arrive.
func (c *Connection) SetStreamFunc(stream StreamFunc) {
	c.stream = stream
}

// SetProgressHandler registers a callback for messages whose frames are still
// arriving. It runs on the relay goroutine and must not block.
func (c *Connection) SetProgressHandler(handler func(domain.WebSocketMessageProgress)) {
	c.onProgress = handler
}

// SetMessageTooBigHandler registers a callback invoked when a message goes
// over the size limit, after the close frames are sent.
func (c *Connection) SetMessageTooBigHandler(handler func(MessageTooBig)) {
	c.onTooBig = handler
}

// SetInflateFailureHandler registers a callback invoked when a compressed
// message cannot be inflated, after the message is processed and the close
// frames are sent.
func (c *Connection) SetInflateFailureHandler(handler func(InflateFailure)) {
	c.onInflate = handler
}

// ClosedRecord receives the final in-memory connection state after the relay ends.
func (c *Connection) ClosedRecord() <-chan domain.WebSocketConnection {
	return c.closedRecord
}

// PublishClosedRecord makes the final state available before persistence starts.
func (c *Connection) PublishClosedRecord(record domain.WebSocketConnection) {
	c.closedRecord <- record
}

func (c *Connection) writeToClient(frame Frame) error {
	return c.clientLeg.writeFrames(frame)
}

func (c *Connection) writeToUpstream(frame Frame) error {
	return c.upstreamLeg.writeFrames(frame)
}

func (c *Connection) handleFrame(
	frame Frame,
	source string,
	out frameSink,
) (stop bool, err error) {
	message, err := NewMessage(c.ID, c.RequestID, source, frame)
	if err != nil {
		return false, fmt.Errorf("creating websocket message: %w", err)
	}

	if frame.Opcode == OpClose {
		// An invalid close payload is relayed unchanged so the receiving
		// endpoint judges it; the relay only records what it saw.
		if _, _, err := ParseClosePayload(frame.Payload); err != nil {
			message.Metadata["close_error"] = err.Error()
		}
	}

	return c.handleMessage(message, out)
}

// handleMessage processes a whole message and forwards it unless it was
// dropped or already streamed. It reports whether the message is a close.
func (c *Connection) handleMessage(message *Message, out frameSink) (isClose bool, err error) {
	received := message.Opcode

	if c.process != nil {
		if err := c.process(message); err != nil {
			return false, fmt.Errorf("processing websocket message: %w", err)
		}
	}

	isClose = received == OpClose || message.Opcode == OpClose
	if isClose {
		code, reason := closeDetailsSeen(message.Payload)
		c.setCloseDetails(code, reason)
	}

	if message.Passthrough || message.Dropped {
		return isClose, nil
	}
	if isClose && !c.closing.Load() {
		// Mark the handshake before the close goes out: the receiving peer
		// may drop TCP as soon as it reads it, and the other direction must
		// then see a clean end, not a read error.
		c.noteCloseRelayed()
	}
	if err := out.writeFrames(message.outgoing()...); err != nil {
		return false, fmt.Errorf("%w: %w", errForwarding, err)
	}

	return isClose, nil
}

// relayFrames relays frames from reader to writeFrame. It never streams.
func (c *Connection) relayFrames(
	reader io.Reader,
	writeFrame func(Frame) error,
	source string,
) error {
	return c.relay(reader, frameFunc(writeFrame), source)
}

// noteCloseRelayed marks the peers' close handshake as started and, while the
// relay runs, bounds how long it may take.
func (c *Connection) noteCloseRelayed() {
	if !c.closeRelayed.CompareAndSwap(false, true) || !c.running.Load() {
		return
	}
	go func() {
		timer := time.NewTimer(c.relayedCloseTimeout)
		defer timer.Stop()
		select {
		case <-c.done:
		case <-timer.C:
			_ = c.shutdown()
		}
	}()
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
		results <- c.relay(
			c.clientReader,
			c.upstreamLeg,
			DirectionFromClient,
		)
	}()

	go func() {
		results <- c.relay(
			c.upstream,
			c.clientLeg,
			DirectionFromServer,
		)
	}()

	firstErr := <-results
	closeHandshake := c.closing.Load() || c.closeRelayed.Load()

	shutdownErr := c.shutdown()

	<-results
	if closeHandshake {
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
		c.clientLeg,
	)
	if err != nil {
		return fmt.Errorf("sending websocket frame to client: %w", err)
	}

	return nil
}

// InjectOptions controls how an injected message goes out.
type InjectOptions struct {
	// NeverCompress sends a text or binary message uncompressed even when
	// the connection negotiated permessage-deflate.
	NeverCompress bool
}

// Inject sends a message as if it originated from direction.
// Injected messages are marked with metadata["injected"] = true. On a
// connection with permessage-deflate, a text or binary message is compressed
// by the leg's compressor unless options.NeverCompress is set.
func (c *Connection) Inject(direction string, opcode int, payload []byte, options InjectOptions) (domain.WebSocketMessage, error) {
	if c.isClosed() {
		return domain.WebSocketMessage{}, ErrConnectionClosed
	}

	var out *leg
	switch direction {
	case DirectionFromClient:
		out = c.upstreamLeg
	case DirectionFromServer:
		out = c.clientLeg
	default:
		return domain.WebSocketMessage{}, fmt.Errorf("invalid websocket inject direction: %q", direction)
	}

	message, err := NewMessage(c.ID, c.RequestID, direction, Frame{
		Fin:     true,
		Opcode:  opcode,
		Payload: append([]byte(nil), payload...),
	})
	if err != nil {
		return domain.WebSocketMessage{}, fmt.Errorf("creating injected websocket message: %w", err)
	}
	message.Metadata["injected"] = true
	message.Compressed = out.deflater != nil && !options.NeverCompress && (opcode == OpText || opcode == OpBinary)

	if c.process != nil {
		if err := c.process(message); err != nil {
			return domain.WebSocketMessage{}, fmt.Errorf("processing injected websocket message: %w", err)
		}
	}
	stored := message.ToDomain()
	if message.Dropped {
		return stored, nil
	}

	if err := out.writeMessage(message.outgoing()...); err != nil {
		return domain.WebSocketMessage{}, fmt.Errorf("writing injected websocket frame: %w", err)
	}

	return stored, nil
}

// Close sends close frames to both peers and shuts the connection down.
func (c *Connection) Close(code int, reason string) error {
	frame, err := c.beginClose(code, reason)
	if err != nil {
		return err
	}
	return c.sendClose(frame, false)
}

// StartClose claims the close and sends its frames without blocking the caller.
func (c *Connection) StartClose(code int, reason string) error {
	frame, err := c.beginClose(code, reason)
	if err != nil {
		return err
	}
	go func() { _ = c.sendClose(frame, true) }()
	return nil
}

func (c *Connection) beginClose(code int, reason string) (Frame, error) {
	if c.isClosed() {
		return Frame{}, ErrConnectionClosed
	}

	payload, err := EncodeClosePayload(code, reason)
	if err != nil {
		return Frame{}, fmt.Errorf("encoding websocket close payload: %w", err)
	}

	frame := Frame{
		Fin:     true,
		Opcode:  OpClose,
		Payload: payload,
	}

	if !c.closing.CompareAndSwap(false, true) {
		return Frame{}, ErrConnectionClosed
	}
	c.setCloseDetails(code, reason)
	return frame, nil
}

func (c *Connection) sendClose(frame Frame, ensureTimeout bool) error {
	return c.sendCloseFrames(frame, frame, ensureTimeout)
}

// sendCloseFrames sends toClient to the client and toUpstream to the upstream,
// bounding the handshake that follows.
func (c *Connection) sendCloseFrames(toClient, toUpstream Frame, ensureTimeout bool) error {
	if c.running.Load() || ensureTimeout {
		go func() {
			timer := time.NewTimer(CloseHandshakeTimeout)
			defer timer.Stop()
			select {
			case <-c.done:
			case <-timer.C:
				_ = c.shutdown()
			}
		}()
	}
	clientErr := c.writeToClient(toClient)
	upstreamErr := c.sendGeneratedClose(toUpstream, DirectionFromClient, c.writeToUpstream)
	if c.running.Load() {
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

	for _, frame := range message.outgoing() {
		if err := writeFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

// SendToUpstream sends a frame toward the upstream server through the process path.
func (c *Connection) SendToUpstream(frame Frame) error {
	if c.isClosed() {
		return ErrConnectionClosed
	}

	_, err := c.handleFrame(
		frame,
		DirectionFromClient,
		c.upstreamLeg,
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
