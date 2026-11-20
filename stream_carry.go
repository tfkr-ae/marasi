package marasi

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// A streamed pair (GLOSSARY) is carried a stream message at a time in each
// direction whose head names a message framing
// (docs/adr/2026-11-20-streamed-pairs-are-recognised-from-the-head-and-other-pairs-stay-buffered.md).
// This file is what every engine shares: the carry of one streamed
// direction from a streamSource on one leg to a streamSink on the other,
// the storage of its stream messages, and the pair's record of how each
// direction ended. An engine supplies the source and the sink; the HTTP/2
// engine's are in h2engine_streamed.go.

// streamSource is one streamed direction as it arrives from its sender. It
// has one reader, which consumes only what it forwards, so a reader that
// stops stops the sender (flow control on HTTP/2, TCP on HTTP/1.1).
type streamSource interface {
	// peek waits until bytes are buffered or the direction ended, and
	// returns the buffered bytes without consuming them. Once none remain
	// after the end it returns the end: io.EOF when the sender ended the
	// direction, a *streamResetError when either side reset it, any other
	// error when it was cut off.
	peek() ([]byte, error)
	// discard consumes n of the bytes peek returned.
	discard(n int)
	// trailers are the trailers the direction ended with, once peek
	// returned io.EOF.
	trailers() h2Fields
	// reset resets the direction toward its sender.
	reset(code http2.ErrCode)
}

// streamSink is the other leg, where a streamed direction goes.
type streamSink interface {
	// forward sends a stream message's bytes as they came, waiting for
	// the receiver's flow control.
	forward(raw []byte) error
	// end ends the direction, with trailers when there are any.
	end(trailers h2Fields) error
	// reset resets the direction toward the receiver.
	reset(code http2.ErrCode)
}

// streamResetError is a streamed direction reset by its sender, or by the
// receiver through the stream they share.
type streamResetError struct{ code http2.ErrCode }

func (e *streamResetError) Error() string { return fmt.Sprintf("the stream was reset (%s)", e.code) }

// Stream endings recorded on the pair, per direction.
const (
	streamComplete  = "complete"
	streamCancelled = "cancelled"
	streamReset     = "reset"
	streamCutShort  = "cut short"
)

// streamedPair is the streamed state of one pair: each streamed direction's
// framing, message count and ending, and the response capture, which waits
// until both directions ended so it can carry them.
type streamedPair struct {
	proxy *Proxy
	pair  *Pair
	// limit is the per-message limit in bytes.
	limit int64

	mu         sync.Mutex
	directions map[string]*streamedDirection
	// response is the response capture WriteResponseModifier deferred.
	response *domain.ProxyResponse
}

// streamedDirection is one streamed direction's record.
type streamedDirection struct {
	framing   stream.Framing
	messages  int
	ending    string
	resetCode string
	trailers  h2Fields
	// declared is set when the direction's head declared a length.
	declared bool
}

// streamed returns the pair's streamed state, made the first time.
func (proxy *Proxy) streamed(pair *Pair) *streamedPair {
	if pair.stream == nil {
		var limit int64
		if proxy.Config != nil {
			limit = proxy.Config.StreamMaxMessageSize
		}
		if limit <= 0 {
			limit = stream.DefaultMaxMessageSize
		}
		pair.stream = &streamedPair{proxy: proxy, pair: pair, limit: limit, directions: make(map[string]*streamedDirection)}
	}
	return pair.stream
}

// direction records that the pair's direction is streamed with framing.
func (sp *streamedPair) direction(name string, framing stream.Framing) *streamedDirection {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	d := &streamedDirection{framing: framing}
	sp.directions[name] = d
	return d
}

// carry carries direction name from src to sink, a stream message at a
// time: each is read whole, up to the limit, then forwarded as it came and
// stored. It returns once the direction ended, and records how.
func (sp *streamedPair) carry(name string, d *streamedDirection, src streamSource, sink streamSink) {
	splitter, err := stream.NewSplitter(d.framing, sp.limit)
	if err != nil {
		sp.end(d, streamReset, http2.ErrCodeInternal)
		src.reset(http2.ErrCodeInternal)
		sink.reset(http2.ErrCodeInternal)
		return
	}
	for {
		data, readErr := src.peek()
		if len(data) > 0 {
			n, message, err := splitter.Feed(data)
			src.discard(n)
			var tooLarge *stream.TooLargeError
			switch {
			case errors.As(err, &tooLarge):
				sp.tooLarge(name, d, tooLarge)
				sink.reset(http2.ErrCodeEnhanceYourCalm)
				src.reset(http2.ErrCodeEnhanceYourCalm)
				return
			case message != nil:
				raw := sp.handle(name, d, message)
				if raw == nil {
					continue
				}
				if err := sink.forward(raw); err != nil {
					sp.ended(d, err)
					return
				}
			}
			continue
		}
		// The direction ended: what arrived of a message it ended inside
		// goes on as it came, and is stored cut short.
		if partial := splitter.Partial(); partial != nil {
			sp.record(name, d, partial, map[string]any{"cut_short": true})
			sink.forward(partial.Raw)
		}
		var reset *streamResetError
		switch {
		case errors.Is(readErr, io.EOF):
			trailers := src.trailers()
			sp.mu.Lock()
			d.trailers = trailers
			sp.mu.Unlock()
			if splitter.Partial() != nil {
				sp.end(d, streamCutShort, 0)
			} else {
				sp.end(d, streamComplete, 0)
			}
			sink.end(trailers)
		case errors.As(readErr, &reset):
			sp.ended(d, readErr)
			sink.reset(reset.code)
		default:
			sp.end(d, streamCutShort, 0)
			sink.reset(http2.ErrCodeInternal)
		}
		return
	}
}

// ended records a direction that ended because of err: a reset with its
// code, CANCEL being a cancel, or anything else a cancel.
func (sp *streamedPair) ended(d *streamedDirection, err error) {
	var reset *streamResetError
	if errors.As(err, &reset) && reset.code != http2.ErrCodeCancel {
		sp.end(d, streamReset, reset.code)
		return
	}
	sp.end(d, streamCancelled, 0)
}

// end records d's ending once; a code goes with a reset.
func (sp *streamedPair) end(d *streamedDirection, ending string, code http2.ErrCode) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if d.ending != "" {
		return
	}
	d.ending = ending
	if ending == streamReset {
		d.resetCode = code.String()
	}
}

// record stores a stream message, when the pair is stored.
func (sp *streamedPair) record(name string, d *streamedDirection, message *stream.Message, metadata map[string]any) {
	sp.mu.Lock()
	d.messages++
	sp.mu.Unlock()
	flags := message.Flags
	sp.store(name, d, &flags, message.Payload, metadata)
}

// tooLarge records a message over the limit, which is never forwarded, and
// writes a proxy log.
func (sp *streamedPair) tooLarge(name string, d *streamedDirection, err *stream.TooLargeError) {
	sp.mu.Lock()
	d.messages++
	sp.mu.Unlock()
	sp.store(name, d, nil, nil, map[string]any{"too_large": true, "size_seen": err.Size})
	sp.end(d, streamReset, http2.ErrCodeEnhanceYourCalm)
	request := "request"
	if req := sp.pair.Request; req != nil && req.URL != nil {
		request = fmt.Sprintf("request %s %s", req.Method, req.URL.Redacted())
	}
	entry := fmt.Sprintf("Stream message too large (%s): a %s message of %d bytes is over the %d-byte limit (stream_max_message_size); the stream was reset", request, name, err.Size, err.Limit)
	_ = sp.proxy.WriteLog("WARN", entry, pairLogOptions(sp.pair)...)
}

// store queues one stream message for storage. A pair that is not stored,
// out of scope or skipped, has its messages carried and not stored.
func (sp *streamedPair) store(name string, d *streamedDirection, flags *uint8, payload []byte, metadata map[string]any) {
	id, err := uuid.NewV7()
	if err != nil {
		return
	}
	sp.queue(sp.message(id, name, d, flags, payload, metadata))
}

// message is a stream message row of the pair's direction d.
func (sp *streamedPair) message(id uuid.UUID, name string, d *streamedDirection, flags *uint8, payload []byte, metadata map[string]any) *domain.StreamMessage {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if payload == nil {
		payload = []byte{}
	}
	return &domain.StreamMessage{
		ID: id, PairID: sp.pair.ID, Direction: name, Framing: string(d.framing), Flags: flags,
		Payload: payload, CreatedAt: time.Now(), Metadata: metadata,
	}
}

// queue queues message for storage, when the pair is stored.
func (sp *streamedPair) queue(message *domain.StreamMessage) {
	if !sp.pair.requestCaptured || sp.proxy.DBWriteChannel == nil {
		return
	}
	sp.proxy.DBWriteChannel <- message
}

// deferResponse keeps the response capture until the pair's directions end.
// It reports false for a pair that is not streamed.
func (pair *Pair) deferResponse(response *domain.ProxyResponse) bool {
	sp := pair.stream
	if sp == nil {
		return false
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.response = response
	return true
}

// finish stores the deferred response capture with each streamed
// direction's framing, message count, ending and trailers in
// metadata.streamed. The engine calls it once both directions ended.
func (sp *streamedPair) finish() {
	sp.mu.Lock()
	response := sp.response
	sp.response = nil
	streamed := make(map[string]any, len(sp.directions))
	for name, d := range sp.directions {
		entry := map[string]any{"framing": string(d.framing), "messages": d.messages, "ending": d.ending}
		if d.ending == "" {
			entry["ending"] = streamCancelled
		}
		if d.resetCode != "" {
			entry["reset_code"] = d.resetCode
		}
		if len(d.trailers) > 0 {
			trailers := make([]map[string]string, len(d.trailers))
			for i, f := range d.trailers {
				trailers[i] = map[string]string{"name": f.Name, "value": f.Value}
			}
			entry["trailers"] = trailers
		}
		streamed[name] = entry
	}
	sp.mu.Unlock()
	if response == nil {
		return
	}
	// The map the chain left is shared with the request capture: the
	// response gets a copy.
	metadata := maps.Clone(response.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["streamed"] = streamed
	response.Metadata = metadata
	proxy := sp.proxy
	proxy.DBWriteChannel <- response
	if proxy.OnResponse != nil {
		proxy.OnResponse(*response)
	}
}
