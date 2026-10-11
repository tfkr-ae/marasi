package websocket

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

const (
	// DefaultMaxMessageSize is the per-message size limit when none is set.
	DefaultMaxMessageSize = 64 * 1024 * 1024
	// ProgressInterval is the least time between two progress events for one
	// message.
	ProgressInterval = 100 * time.Millisecond

	// readChunk bounds one read of a frame payload, so a streamed frame
	// moves on in pieces as it arrives.
	readChunk = 32 * 1024
	// initialPayloadCap bounds the buffer allocated up front for a payload,
	// so a header that claims a huge length does not allocate it.
	initialPayloadCap = 1024 * 1024
)

// StreamFunc reports whether a data message that starts now, in direction
// with opcode, may be forwarded as its bytes arrive. It returns true only
// when no hook can hold or change the message.
type StreamFunc func(direction string, opcode int) bool

// MessageTooBig describes a message that went over the size limit.
type MessageTooBig struct {
	// Direction is the side that sent the message.
	Direction string
	// Size is the message size in bytes once its current frame is counted.
	// For an inflated message it is the bytes inflated before inflating
	// stopped: one more than the limit.
	Size int64
	// Limit is the size limit in bytes.
	Limit int64
	// Inflated reports that the message is compressed and went over the
	// limit as it was inflated.
	Inflated bool
}

// InflateFailure describes a compressed message the proxy could not inflate.
type InflateFailure struct {
	// Direction is the side that sent the message.
	Direction string
	// Err is the inflate error.
	Err error
}

// frameSink receives the frames relayed toward one peer.
type frameSink interface {
	writeFrames(frames ...Frame) error
}

// frameFunc adapts a function to a frameSink. It cannot stream.
type frameFunc func(Frame) error

func (f frameFunc) writeFrames(frames ...Frame) error {
	for _, frame := range frames {
		if err := f(frame); err != nil {
			return err
		}
	}
	return nil
}

// leg writes frames to one peer. Frames from different writers never
// interleave, and a data message streamed frame by frame is not interleaved
// with another data message.
type leg struct {
	w         io.Writer
	mask      bool
	errPrefix string
	// deflater compresses messages for the peer when the connection
	// negotiated permessage-deflate. It is used with mu held, so messages
	// are compressed in the order they go out.
	deflater *deflater

	// mu is held while a frame, or an unchanged message's frames, go out.
	mu sync.Mutex
	// message is held from the first to the last frame of a streamed data
	// message, and while an injected message goes out.
	message sync.Mutex
	// closed is set, with mu held, once the proxy sent its own close frame
	// to the peer. No frame goes out after it (RFC 6455 §5.5.1).
	closed bool
}

// writeFrames writes frames to the peer. After the proxy's own close frame
// went out, frames are dropped.
func (l *leg) writeFrames(frames ...Frame) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writeLocked(frames...)
}

// writeClose writes the proxy's own close frame. It is the last frame the
// peer gets.
func (l *leg) writeClose(frames ...Frame) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.writeLocked(frames...)
	l.closed = true
	return err
}

func (l *leg) writeLocked(frames ...Frame) error {
	if l.closed {
		return nil
	}
	for _, frame := range frames {
		if frame.deflate {
			frame.deflate = false
			if l.deflater != nil {
				frame.Payload, frame.Rsv1 = l.deflater.deflate(frame.Payload)
			}
		}
		if err := WriteFrame(l.w, frame, l.mask); err != nil {
			return fmt.Errorf("%s: %w", l.errPrefix, err)
		}
	}
	return nil
}

// writeMessage writes a message's frames, waiting for any streamed message to
// finish first.
func (l *leg) writeMessage(frames ...Frame) error {
	l.message.Lock()
	defer l.message.Unlock()
	return l.writeFrames(frames...)
}

// frameStream forwards one frame whose payload is written as it arrives.
type frameStream struct {
	leg     *leg
	header  []byte
	key     [4]byte
	offset  int64
	scratch []byte
	err     error
	// drop is set when the leg is closed: nothing of the frame goes out.
	drop bool
}

// beginFrame starts streaming frame, with a payload of length bytes. It holds
// the leg until end is called.
func (l *leg) beginFrame(frame Frame, length int64) *frameStream {
	l.mu.Lock()
	stream := &frameStream{leg: l, drop: l.closed}
	if l.mask {
		stream.key, stream.err = newMaskKey()
	}
	stream.header = appendFrameHeader(nil, frame, length, l.mask, stream.key)
	return stream
}

// write forwards the next payload bytes. The header goes out with the first
// bytes. After a failed write the rest of the frame is not sent.
func (s *frameStream) write(p []byte) {
	if s.drop || s.err != nil || len(p) == 0 {
		return
	}
	out := append(s.scratch[:0], s.header...)
	start := len(out)
	out = append(out, p...)
	if s.leg.mask {
		maskBytes(out[start:], s.key, s.offset)
	}
	s.scratch = out
	s.header = nil
	s.offset += int64(len(p))
	if _, err := s.leg.w.Write(out); err != nil {
		s.err = fmt.Errorf("%s: %w", s.leg.errPrefix, err)
	}
}

// end finishes the frame and releases the leg.
func (s *frameStream) end() error {
	defer s.leg.mu.Unlock()
	if !s.drop && s.err == nil && s.header != nil {
		if _, err := s.leg.w.Write(s.header); err != nil {
			s.err = fmt.Errorf("%s: %w", s.leg.errPrefix, err)
		}
	}
	return s.err
}

// inbound is the state of one direction's relay.
type inbound struct {
	c      *Connection
	reader io.Reader
	out    frameSink
	source string

	// pending is the data message whose frames are arriving.
	pending *Message
	// streamed marks pending as forwarded as its bytes arrive.
	streamed     bool
	lastProgress time.Time
	// discarding is set once the source went over the size limit. Its data
	// is read and dropped while the close handshake runs.
	discarding bool
}

// relay reads frames from reader, reassembles data messages, and forwards
// them to out. Control frames go through at once, also between fragments.
func (c *Connection) relay(reader io.Reader, out frameSink, source string) error {
	in := &inbound{c: c, reader: reader, out: out, source: source}
	defer in.releaseStream()

	for {
		select {
		case <-c.done:
			return nil
		default:
		}

		frame, length, err := readFrameHeader(reader)
		if err != nil {
			in.recordUnfinished()
			if c.closing.Load() || c.closeRelayed.Load() {
				return nil
			}
			return fmt.Errorf("reading websocket frame: %w", err)
		}

		var stop bool
		if frame.IsControl() {
			stop, err = in.control(frame, length)
		} else {
			err = in.data(frame, length)
		}
		if err != nil {
			// Once a close is relayed, a peer may drop TCP while the other
			// still sends. Drop what can't be delivered and keep reading, so
			// the reply close still reaches its peer.
			if c.closeRelayed.Load() && errors.Is(err, errForwarding) {
				continue
			}
			return err
		}
		if stop {
			return nil
		}
	}
}

// control relays one control frame. It reports whether the relay should stop.
func (in *inbound) control(frame Frame, length int64) (stop bool, err error) {
	c := in.c
	if in.overLimit(frame, length, nil) {
		return false, in.discard(length)
	}
	if frame.Payload, err = in.readPayload(frame, length, nil); err != nil {
		return false, err
	}

	if c.closing.Load() {
		// The proxy sent its own close, so it relays nothing more. A peer's
		// close reply finishes the handshake.
		if frame.Opcode != OpClose || in.source == DirectionFromClient {
			return false, nil
		}
		return c.handleFrame(frame, in.source, frameFunc(func(Frame) error { return nil }))
	}

	isClose, err := c.handleFrame(frame, in.source, in.out)
	if err != nil {
		return false, err
	}
	if isClose {
		if c.closing.Load() {
			return true, nil
		}
		c.noteCloseRelayed()
	}
	return false, nil
}

// data adds one data frame to the message in progress, forwarding the message
// once its final frame is in.
func (in *inbound) data(frame Frame, length int64) error {
	if in.discarding || in.c.closing.Load() {
		// The proxy sent its own close: nothing more goes to either peer.
		in.dropPending()
		return in.discard(length)
	}

	if in.pending != nil && frame.Opcode != OpContinuation {
		// A new message started before the last one finished. Forward what
		// arrived so the receiver sees the violation.
		if err := in.finish(true); err != nil {
			return err
		}
	}

	if in.overLimit(frame, length, in.pending) {
		in.dropPending()
		return in.discard(length)
	}

	if in.pending == nil {
		if err := in.start(frame); err != nil {
			return err
		}
	}

	var stream *frameStream
	if in.streamed {
		stream = in.out.(*leg).beginFrame(frame, length)
	}
	payload, err := in.readPayload(frame, length, stream)
	var forwardErr error
	if stream != nil {
		forwardErr = stream.end()
	}
	if err != nil {
		return err
	}
	frame.Payload = payload
	in.pending.addFrame(frame)

	if frame.Fin {
		if err := in.finish(false); err != nil {
			return err
		}
	} else {
		in.progress(0)
	}
	if forwardErr != nil {
		return fmt.Errorf("%w: %w", errForwarding, forwardErr)
	}
	return nil
}

// start begins a data message and decides whether it is streamed.
func (in *inbound) start(frame Frame) error {
	message, err := newMessage(in.c.ID, in.c.RequestID, in.source)
	if err != nil {
		return fmt.Errorf("creating websocket message: %w", err)
	}
	message.Opcode = frame.Opcode
	in.pending = message
	in.lastProgress = time.Time{}

	// A compressed message is buffered whole: it is inflated and compressed
	// again for the receiver (ADR-0028). Without deflate, RSV1 is a protocol
	// violation and the message goes through unchanged, but unstreamed.
	if frame.Rsv1 && in.c.inflaters != nil {
		message.Compressed = true
	}
	out, canStream := in.out.(*leg)
	if canStream && !frame.Rsv1 && in.c.stream != nil && in.c.stream(in.source, frame.Opcode) {
		out.message.Lock()
		in.streamed = true
	}
	return nil
}

// finish completes the pending message: a streamed one is recorded, any other
// is processed and forwarded.
func (in *inbound) finish(incomplete bool) error {
	message := in.pending
	in.pending = nil
	if incomplete {
		message.incomplete = true
		message.Metadata["incomplete"] = true
	}
	if in.streamed {
		message.Passthrough = true
		in.releaseStream()
	}
	if message.Compressed {
		if incomplete {
			// What arrived cannot be inflated as a message. Forward its
			// frames unchanged so the receiver sees the violation.
			message.Compressed = false
		} else {
			payload, err := in.c.inflaters[in.source].inflate(message.Payload, in.c.maxMessageSize)
			if err != nil {
				return in.failInflate(message, err)
			}
			message.Payload = payload
		}
	}

	_, err := in.c.handleMessage(message, in.out)
	return err
}

// failInflate handles a compressed message that cannot be inflated
// (ADR-0028). The proxy cannot forward it, so it records the message as the
// compressed bytes received, with the error, and closes the connection: 1007
// to the sender, or 1009 when it inflates to over the size limit, and 1001
// to the receiver.
func (in *inbound) failInflate(message *Message, inflateErr error) error {
	c := in.c
	message.Metadata["inflate_error"] = inflateErr.Error()
	// The message is only recorded: no hook may hold or change it, and it is
	// not forwarded.
	message.Passthrough = true
	if _, err := c.handleMessage(message, in.out); err != nil {
		return err
	}

	if errors.Is(inflateErr, errInflatedTooLarge) {
		in.closeLegs(CloseMessageTooBig)
		if c.onTooBig != nil {
			c.onTooBig(MessageTooBig{Direction: in.source, Size: c.maxMessageSize + 1, Limit: c.maxMessageSize, Inflated: true})
		}
		return nil
	}
	in.closeLegs(CloseInvalidPayload)
	if c.onInflate != nil {
		c.onInflate(InflateFailure{Direction: in.source, Err: inflateErr})
	}
	return nil
}

// recordUnfinished records a message whose source stopped before its final
// frame. Buffered frames are forwarded unchanged first.
func (in *inbound) recordUnfinished() {
	message := in.pending
	if message == nil {
		return
	}
	in.pending = nil
	message.incomplete = true
	message.Metadata["incomplete"] = true
	message.Compressed = false
	if !in.streamed {
		_ = in.out.writeFrames(message.frames...)
	}
	in.releaseStream()
	message.Passthrough = true
	if in.c.process != nil {
		_ = in.c.process(message)
	}
}

// dropPending abandons the pending message.
func (in *inbound) dropPending() {
	in.pending = nil
	in.releaseStream()
}

func (in *inbound) releaseStream() {
	if in.streamed {
		in.streamed = false
		in.out.(*leg).message.Unlock()
	}
}

// readPayload reads a frame payload of length bytes in chunks, unmasking it.
// When stream is set, each chunk is forwarded as it arrives.
func (in *inbound) readPayload(frame Frame, length int64, stream *frameStream) ([]byte, error) {
	payload := make([]byte, 0, min(length, initialPayloadCap))
	var read int64
	for read < length {
		chunk := min(length-read, readChunk)
		payload = slices.Grow(payload, int(chunk))
		n, err := in.reader.Read(payload[len(payload) : int64(len(payload))+chunk])
		if n > 0 {
			piece := payload[len(payload) : len(payload)+n]
			if frame.Masked {
				maskBytes(piece, frame.Key, read)
			}
			payload = payload[:len(payload)+n]
			read += int64(n)
			if stream != nil {
				stream.write(piece)
			}
			if read < length && !frame.IsControl() {
				in.progress(read)
			}
		}
		if err != nil {
			if read == length && errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading websocket frame: reading payload : %w", err)
		}
	}
	return payload, nil
}

// progress announces the pending message, at most once per ProgressInterval.
// partial is how much of the current frame is in and not yet added.
func (in *inbound) progress(partial int64) {
	c := in.c
	if c.onProgress == nil || in.pending == nil {
		return
	}
	now := time.Now()
	if !in.lastProgress.IsZero() && now.Sub(in.lastProgress) < ProgressInterval {
		return
	}
	in.lastProgress = now

	frames := in.pending.FrameCount
	if partial > 0 {
		frames++
	}
	c.onProgress(domain.WebSocketMessageProgress{
		MessageID:    in.pending.ID,
		ConnectionID: c.ID,
		RequestID:    c.RequestID,
		Direction:    in.source,
		Opcode:       in.pending.Opcode,
		Bytes:        int64(len(in.pending.Payload)) + partial,
		Frames:       frames,
		Streamed:     in.streamed,
	})
}

// overLimit reports whether frame, of length bytes, goes over the size limit
// on the message it belongs to: pending, or a new message when pending is
// nil. If so it records that message and closes the connection: 1009 to the
// sender and 1001 to the receiver.
func (in *inbound) overLimit(frame Frame, length int64, pending *Message) bool {
	c := in.c
	var size int64
	if pending != nil {
		size = int64(len(pending.Payload))
	}
	if c.maxMessageSize <= 0 || length <= c.maxMessageSize-size {
		return false
	}
	in.recordTooLarge(frame, pending, size+length)
	in.closeLegs(CloseMessageTooBig)
	if c.onTooBig != nil {
		c.onTooBig(MessageTooBig{Direction: in.source, Size: size + length, Limit: c.maxMessageSize})
	}
	return true
}

// recordTooLarge records a message that went over the size limit, as the
// bytes buffered so far: none for a new or streamed message. Like a message
// that cannot be inflated, it is only recorded, never held or forwarded.
func (in *inbound) recordTooLarge(frame Frame, pending *Message, size int64) {
	c := in.c
	message := pending
	if message == nil {
		var err error
		if message, err = newMessage(c.ID, c.RequestID, in.source); err != nil {
			return
		}
		message.Opcode = frame.Opcode
	} else if in.streamed {
		message.Payload = nil
	}
	message.Metadata["too_large"] = true
	message.Metadata["size_seen"] = size
	message.Passthrough = true
	if c.process != nil {
		_ = c.process(message)
	}
}

// closeLegs closes the connection because of what the source sent: code to
// the sender and 1001 to the receiver. The source's later data is read and
// dropped while the close handshake runs.
func (in *inbound) closeLegs(code int) {
	c := in.c
	in.discarding = true
	if !c.closing.CompareAndSwap(false, true) {
		return
	}
	c.setCloseDetails(code, "")
	toSender := Frame{Fin: true, Opcode: OpClose, Payload: closePayload(code)}
	toReceiver := Frame{Fin: true, Opcode: OpClose, Payload: closePayload(CloseGoingAway)}
	if in.source == DirectionFromClient {
		_ = c.sendCloseFrames(toSender, toReceiver, false)
	} else {
		_ = c.sendCloseFrames(toReceiver, toSender, false)
	}
}

// discard reads and drops a payload of length bytes.
func (in *inbound) discard(length int64) error {
	if _, err := io.CopyN(io.Discard, in.reader, length); err != nil {
		return fmt.Errorf("reading websocket frame: discarding payload : %w", err)
	}
	return nil
}

func closePayload(code int) []byte {
	payload, _ := EncodeClosePayload(code, "")
	return payload
}
