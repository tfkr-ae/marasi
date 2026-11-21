package marasi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// Injecting a stream message and cancelling a streamed pair (spec: Control
// API, CLI and events). An injected message takes the direction's turn
// (streamedDirection.turn), so it goes out between two of the sender's
// messages, never inside one, and while it is handled nothing more is read
// from that sender. It is handled as a read one is: the processStreamMessage
// hooks and Checkpoint see it, and it is stored with injected metadata.

var (
	// ErrStreamedPairNotFound is returned for a pair that is not a
	// streamed pair being carried.
	ErrStreamedPairNotFound = errors.New("streamed pair not found or ended")
	// ErrStreamDirection is returned for a direction that is not request
	// or response.
	ErrStreamDirection = errors.New("stream direction must be request or response")
	// ErrStreamDirectionNotStreamed is returned for an injection into a
	// direction of the pair that is not carried as stream messages, or
	// not yet.
	ErrStreamDirectionNotStreamed = errors.New("the direction is not carried as stream messages")
	// ErrStreamDirectionEnded is returned for an injection into a
	// direction that ended.
	ErrStreamDirectionEnded = errors.New("the direction ended")
)

// StreamInjectOptions controls how an injected stream message goes out.
type StreamInjectOptions struct {
	// NeverCompress sends the message uncompressed even when the direction
	// compresses its messages. Otherwise a message of a framing with
	// per-message compression is compressed with the direction's encoding
	// when the proxy encodes it (gzip, deflate).
	NeverCompress bool
}

// InjectStreamMessage puts a new stream message with payload into direction
// (request or response) of the streamed pair pairID, and returns it as it
// was stored. payload is the message without its envelope; for server-sent
// events and NDJSON it goes out as given, so it should end as their messages
// do. A hook or Checkpoint may drop it: it is returned with dropped metadata
// and not sent. Waiting for the direction's turn ends with ctx.
func (proxy *Proxy) InjectStreamMessage(ctx context.Context, pairID uuid.UUID, direction string, payload []byte, options StreamInjectOptions) (domain.StreamMessage, error) {
	if direction != domain.StreamDirectionRequest && direction != domain.StreamDirectionResponse {
		return domain.StreamMessage{}, fmt.Errorf("%w: %q", ErrStreamDirection, direction)
	}
	sp, ok := proxy.liveStreamedPair(pairID)
	if !ok {
		return domain.StreamMessage{}, fmt.Errorf("%w: %s", ErrStreamedPairNotFound, pairID)
	}
	return sp.inject(ctx, direction, func(string) ([]byte, error) { return payload, nil }, options)
}

// InjectStreamText is InjectStreamMessage with the payload given in its
// text form, encoded with the codec the direction's head names
// (stream.FromText). Text that cannot be encoded is refused with
// ErrStreamText, naming the line and column of a syntax error, and nothing
// is sent.
func (proxy *Proxy) InjectStreamText(ctx context.Context, pairID uuid.UUID, direction, text string, options StreamInjectOptions) (domain.StreamMessage, error) {
	if direction != domain.StreamDirectionRequest && direction != domain.StreamDirectionResponse {
		return domain.StreamMessage{}, fmt.Errorf("%w: %q", ErrStreamDirection, direction)
	}
	sp, ok := proxy.liveStreamedPair(pairID)
	if !ok {
		return domain.StreamMessage{}, fmt.Errorf("%w: %s", ErrStreamedPairNotFound, pairID)
	}
	return sp.inject(ctx, direction, func(contentType string) ([]byte, error) { return encodeStreamText(contentType, text) }, options)
}

// CancelStreamedPair cancels the streamed pair pairID on both legs:
// RST_STREAM CANCEL toward the client and toward the origin on HTTP/2, and
// both connections closed on HTTP/1.1. Each direction
// still going is recorded cancelled, and the pair's held stream messages
// are abandoned.
func (proxy *Proxy) CancelStreamedPair(pairID uuid.UUID) error {
	sp, ok := proxy.liveStreamedPair(pairID)
	if !ok {
		return fmt.Errorf("%w: %s", ErrStreamedPairNotFound, pairID)
	}
	sp.cancel()
	return nil
}

// streamedDrainInterval is how often cancelStreamedPairs looks again for
// streamed pairs that have not finished.
const streamedDrainInterval = 5 * time.Millisecond

// cancelStreamedPairs cancels every streamed pair being carried, and any
// that starts meanwhile, and returns once each finished
// (streamedPair.finish): its directions ended and its response capture was
// queued. Streamed pairs hold no project admission after their heads, so a
// project switch or a shutdown ends them here, as it closes WebSockets.
func (proxy *Proxy) cancelStreamedPairs() {
	for {
		live := false
		proxy.streamedPairs.Range(func(_, value any) bool {
			live = true
			sp := value.(*streamedPair)
			sp.mu.Lock()
			cancelled := sp.cancelled
			sp.mu.Unlock()
			if !cancelled {
				sp.cancel()
			}
			return true
		})
		if !live {
			return
		}
		time.Sleep(streamedDrainInterval)
	}
}

// CloseLiveTrafficAndFlush cancels the streamed pairs being carried and
// closes live WebSockets, waits until they ended, and flushes what they
// queued for storage, so none of it reaches the project opened next.
func (proxy *Proxy) CloseLiveTrafficAndFlush() error {
	proxy.cancelStreamedPairs()
	return proxy.CloseWebSocketsAndFlush()
}

func (proxy *Proxy) liveStreamedPair(pairID uuid.UUID) (*streamedPair, bool) {
	value, ok := proxy.streamedPairs.Load(pairID)
	if !ok {
		return nil, false
	}
	return value.(*streamedPair), true
}

// inject puts a message into direction name. payloadOf gives its payload
// from the content type of the direction's head as it went out.
func (sp *streamedPair) inject(ctx context.Context, name string, payloadOf func(contentType string) ([]byte, error), options StreamInjectOptions) (domain.StreamMessage, error) {
	sp.mu.Lock()
	d := sp.directions[name]
	sp.mu.Unlock()
	if d == nil {
		return domain.StreamMessage{}, fmt.Errorf("%w: %s", ErrStreamDirectionNotStreamed, name)
	}
	select {
	case d.turn <- struct{}{}:
	case <-ctx.Done():
		return domain.StreamMessage{}, ctx.Err()
	}
	defer func() { <-d.turn }()

	sp.mu.Lock()
	src, sink, ended := d.src, d.sink, d.ending != "" || d.stopped
	sp.mu.Unlock()
	switch {
	case src == nil:
		return domain.StreamMessage{}, fmt.Errorf("%w: %s", ErrStreamDirectionNotStreamed, name)
	case ended:
		return domain.StreamMessage{}, fmt.Errorf("%w: %s", ErrStreamDirectionEnded, name)
	case sp.declaresLength(d):
		return domain.StreamMessage{}, fmt.Errorf("%w: the %s head declares a length, so an injected message cannot be carried", ErrStreamLengthDeclared, name)
	}
	payload, err := payloadOf(sp.contentType(d))
	if err != nil {
		return domain.StreamMessage{}, err
	}
	message, err := injectedMessage(d, payload, options)
	if err != nil {
		return domain.StreamMessage{}, err
	}
	raw, row, err := sp.process(name, d, message, src.cutOff(), map[string]any{"injected": true})
	if err != nil {
		return domain.StreamMessage{}, err
	}
	if raw != nil {
		if err := sink.forward(raw); err != nil {
			return row, fmt.Errorf("%w: %s: %v", ErrStreamDirectionEnded, name, err)
		}
	}
	return row, nil
}

// injectedMessage is payload as a message of d on the wire: in an envelope
// for the enveloped framings, compressed with d's encoding unless options
// or the encoding say otherwise.
func injectedMessage(d *streamedDirection, payload []byte, options StreamInjectOptions) (*stream.Message, error) {
	if !d.framing.Enveloped() {
		return &stream.Message{Raw: payload, Payload: payload}, nil
	}
	var flags byte
	wire := payload
	if !options.NeverCompress && stream.EncodingHeader(d.framing) != "" && stream.Decodable(d.encoding) {
		compressed, err := stream.Compress(d.encoding, payload)
		if err != nil {
			return nil, fmt.Errorf("compressing the injected message: %w", err)
		}
		flags, wire = stream.CompressedFlags(flags), compressed
	}
	raw := stream.Envelope(flags, wire)
	return &stream.Message{Raw: raw, Flags: flags, Payload: raw[len(raw)-len(wire):]}, nil
}

// cancel resets both legs of the pair with CANCEL: each direction's
// receiver, then the sender of each direction still going, or of the only
// direction, so no leg is reset twice. HTTP/1.1 legs are closed.
func (sp *streamedPair) cancel() {
	sp.mu.Lock()
	if !sp.cancelled {
		sp.cancelled = true
		close(sp.cancelledCh)
	}
	directions := make([]*streamedDirection, 0, len(sp.directions))
	for _, d := range sp.directions {
		directions = append(directions, d)
	}
	sp.mu.Unlock()
	var senders []streamSource
	for _, d := range directions {
		sp.mu.Lock()
		src, sink, going := d.src, d.sink, d.ending == ""
		sp.mu.Unlock()
		sp.end(d, streamCancelled, 0)
		if sink != nil {
			sink.reset(http2.ErrCodeCancel)
		}
		// The sender of a direction that ended is reset through the
		// other direction's sink, unless there is none.
		if src != nil && (going || len(directions) == 1) {
			senders = append(senders, src)
		}
	}
	for _, src := range senders {
		src.reset(http2.ErrCodeCancel)
	}
}
