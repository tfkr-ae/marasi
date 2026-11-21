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

	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// A streamed pair (GLOSSARY) is carried a stream message at a time in each
// direction whose head names a message framing
// (docs/adr/2026-11-20-streamed-pairs-are-recognised-from-the-head-and-other-pairs-stay-buffered.md).
// This file is what every engine shares: the carry of one streamed
// direction from a streamSource on one leg to a streamSink on the other,
// the storage of its stream messages, and the pair's record of how each
// direction ended. An engine supplies the source and the sink: the HTTP/2
// engine's are in h2engine_streamed.go, the HTTP/1.1 engine's in
// h1engine_streamed.go.

// streamedFraming is the message framing a head's content-type names, when
// the engines carry it streamed, and FramingNone otherwise: every message
// framing, but for these. Both engines read a head's fields as an h2Fields
// list (h1FramingFields gives an HTTP/1.1 head's). A gRPC-Web body
// in base64 (application/grpc-web-text) is read whole, since its messages
// cannot be cut before it is decoded. A server-sent event or NDJSON body
// with a Content-Encoding is carried streamed when the coding is gzip or
// deflate and the head declares no length, since the proxy compresses it
// again toward the receiver (stream_body_compression.go); otherwise it is
// read whole.
func streamedFraming(fields h2Fields, response bool) stream.Framing {
	contentType, _ := fields.get("content-type")
	if stream.IsGRPCWebText(contentType) {
		return stream.FramingNone
	}
	framing := stream.FramingOf(contentType, response)
	if !framing.Enveloped() {
		contentEncoding, _ := fields.get("content-encoding")
		encoding, carried := streamedBodyEncoding(contentEncoding)
		if !carried {
			return stream.FramingNone
		}
		if _, declared := fields.get("content-length"); encoding != "" && declared {
			return stream.FramingNone
		}
	}
	return framing
}

// streamedHeadEncoding is the whole-body coding of a streamed head whose framing
// has no per-message compression, "" for none.
func streamedHeadEncoding(fields h2Fields, framing stream.Framing) string {
	if framing.Enveloped() {
		return ""
	}
	contentEncoding, _ := fields.get("content-encoding")
	encoding, _ := streamedBodyEncoding(contentEncoding)
	return encoding
}

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
	// cutOff is closed once the direction is reset or cut off, whether or
	// not its reader has read that far: a held stream message is abandoned
	// then.
	cutOff() <-chan struct{}
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
	// id, extensionID and requestName are the pair's, taken when its
	// streamed state is made, after the request chain: the carry reads them
	// while the response chain loads the pair's fields again.
	id          uuid.UUID
	extensionID uuid.UUID
	requestName string
	// limit is the per-message limit in bytes.
	limit int64

	mu         sync.Mutex
	directions map[string]*streamedDirection
	// started reports that the pair's start was announced, so its end
	// is too.
	started bool
	// response is the response capture WriteResponseModifier deferred.
	response *domain.ProxyResponse
}

// streamedDirection is one streamed direction's record.
type streamedDirection struct {
	name    string
	framing stream.Framing
	// contentType is the content type of the direction's head as it went
	// out, which names the codec its text form is read with.
	contentType string
	messages    int
	ending      string
	resetCode   string
	trailers    h2Fields
	// declared is set when the direction's head declared a length.
	declared bool
	// encoding is the per-message encoding the direction's head names
	// (stream.EncodingHeader) as it went out (headWentOut).
	encoding string
	// bodyIn and bodyOut are the whole-body codings of the sender's head
	// and of the head that went out (stream_body_compression.go), "" for
	// none.
	bodyIn, bodyOut string
	// src and sink are the direction's legs once its carry started, nil
	// before.
	src  streamSource
	sink streamSink
	// turn is the direction's one place in line on its sink: the carry
	// holds it while a message is partly read, handled or forwarded, and
	// an injection while it is handled and forwarded, so an injected
	// message lands between two messages, never inside one. It is a
	// channel of one, so an injection can stop waiting for it.
	turn chan struct{}
	// stopped is set, holding turn, once the carry stopped: nothing more
	// goes to the sink.
	stopped bool
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
		requestName := "request"
		if req := pair.Request; req != nil && req.URL != nil {
			requestName = fmt.Sprintf("request %s %s", req.Method, req.URL.Redacted())
		}
		pair.stream = &streamedPair{
			proxy: proxy, pair: pair, id: pair.ID, extensionID: pair.ExtensionID, requestName: requestName,
			limit: limit, directions: make(map[string]*streamedDirection),
		}
	}
	return pair.stream
}

// direction records that the pair's direction is streamed with framing.
// The first direction of a stored pair announces the pair started.
func (sp *streamedPair) direction(name string, framing stream.Framing) *streamedDirection {
	sp.mu.Lock()
	d := &streamedDirection{name: name, framing: framing, turn: make(chan struct{}, 1)}
	sp.directions[name] = d
	if len(sp.directions) == 1 {
		sp.proxy.streamedPairs.Store(sp.id, sp)
	}
	var started *domain.StreamedPairStarted
	if !sp.started && sp.announced() {
		sp.started = true
	}
	if sp.started && len(sp.directions) == 1 && sp.proxy.OnStreamedPairStarted != nil {
		started = &domain.StreamedPairStarted{PairID: sp.id, Framings: make(map[string]string, len(sp.directions))}
		for name, d := range sp.directions {
			started.Framings[name] = string(d.framing)
		}
	}
	sp.mu.Unlock()
	if started != nil {
		sp.proxy.OnStreamedPairStarted(*started)
	}
	return d
}

// announced reports whether the pair's streaming is announced: only a
// stored pair's is.
func (sp *streamedPair) announced() bool {
	return sp.pair.requestCaptured
}

// streamedHead is a streamed direction's head as it went out: its fields
// as headWentOut reads them, whether it declares a length, and the
// whole-body codings of the sender's head and of this one, "" for none.
type streamedHead struct {
	fields          h2Fields
	declared        bool
	bodyIn, bodyOut string
}

// start records the head direction d went out with and carries d from src
// to sink: on a goroutine counted by carrying when that is set, and before
// start returns otherwise.
func (sp *streamedPair) start(name string, d *streamedDirection, head streamedHead, src streamSource, sink streamSink, carrying *sync.WaitGroup) {
	sp.headWentOut(d, head.fields)
	if head.declared {
		sp.declareLength(d)
	}
	sp.bodyEncodings(d, head.bodyIn, head.bodyOut)
	if carrying == nil {
		sp.carry(name, d, src, sink)
		return
	}
	carrying.Add(1)
	go func() {
		defer carrying.Done()
		sp.carry(name, d, src, sink)
	}()
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
	// An injected message goes to the receiver through the same
	// compressor as the carried ones.
	src, sink, inflater := sp.compressedBody(d, src, sink)
	d.turn <- struct{}{}
	sp.mu.Lock()
	d.src, d.sink = src, sink
	sp.mu.Unlock()
	<-d.turn
	// The carry holds d.turn from the first byte of a message until it
	// went out, and from the end of the direction on.
	holding := false
	defer func() {
		if !holding {
			d.turn <- struct{}{}
		}
		d.stopped = true
		<-d.turn
	}()
	// pass handles a whole message and forwards what comes of it. It
	// reports false once the carry stops.
	pass := func(message *stream.Message) bool {
		if inflater != nil {
			inflater.mark()
		}
		raw, err := sp.handle(name, d, message, src.cutOff())
		var undecodable *messageDecodeError
		if errors.As(err, &undecodable) {
			code := http2.ErrCodeInternal
			if undecodable.tooLarge() {
				code = http2.ErrCodeEnhanceYourCalm
			}
			sink.reset(code)
			src.reset(code)
			return false
		}
		if raw == nil {
			return true
		}
		if err := sink.forward(raw); err != nil {
			sp.ended(d, err)
			return false
		}
		return true
	}
	var lastProgress time.Time
	for {
		if holding && splitter.Buffered() == 0 {
			<-d.turn
			holding = false
		}
		data, readErr := src.peek()
		if !holding {
			d.turn <- struct{}{}
			holding = true
		}
		if len(data) > 0 {
			n, message, err := splitter.Feed(data)
			src.discard(n)
			if message == nil && err == nil {
				sp.progress(name, d, splitter.Buffered(), &lastProgress)
			} else {
				lastProgress = time.Time{}
			}
			var tooLarge *stream.TooLargeError
			switch {
			case errors.As(err, &tooLarge):
				if inflater != nil {
					// A decompression bomb stops at the limit.
					sp.inflateFailed(name, d, inflater.compressed(), inflatedTooLarge(tooLarge), http2.ErrCodeEnhanceYourCalm,
						map[string]any{"too_large": true, "size_seen": tooLarge.Size})
				} else {
					sp.tooLarge(name, d, tooLarge)
				}
				sink.reset(http2.ErrCodeEnhanceYourCalm)
				src.reset(http2.ErrCodeEnhanceYourCalm)
				return
			case message != nil:
				if !pass(message) {
					return
				}
			}
			continue
		}
		// A body that cannot be inflated cannot be forwarded: the
		// receiver's decompressor was fed Marasi's output.
		var notInflated *inflateError
		if errors.As(readErr, &notInflated) {
			sp.inflateFailed(name, d, inflater.compressed(), notInflated.err, http2.ErrCodeInternal, nil)
			sink.reset(http2.ErrCodeInternal)
			src.reset(http2.ErrCodeInternal)
			return
		}
		// The direction ended: a message its end makes whole goes on, and
		// what arrived of a message it ended inside goes on as it came,
		// and is stored cut short.
		if errors.Is(readErr, io.EOF) {
			if last := splitter.Flush(); last != nil && !pass(last) {
				return
			}
		}
		if partial := splitter.Partial(); partial != nil {
			sp.record(name, d, partial, map[string]any{"cut_short": true})
			sink.forward(partial.Raw)
		}
		var reset *streamResetError
		switch {
		case errors.Is(readErr, io.EOF):
			trailers := src.trailers()
			if len(trailers) > 0 && shouldInterceptTrailers(sp.proxy) {
				var abandoned bool
				if trailers, abandoned = sp.holdTrailers(name, d, trailers, src.cutOff()); abandoned {
					sp.end(d, streamCancelled, 0)
					sink.reset(http2.ErrCodeCancel)
					return
				}
			}
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

// streamProgressInterval is the least time between two progress
// announcements of one direction's message, as for WebSocket messages.
const streamProgressInterval = 100 * time.Millisecond

// progress announces a message still arriving, bytes of it so far, at most
// once per streamProgressInterval since *last.
func (sp *streamedPair) progress(name string, d *streamedDirection, bytes int, last *time.Time) {
	if bytes == 0 || sp.proxy.OnStreamMessageProgress == nil || !sp.announced() {
		return
	}
	now := time.Now()
	if !last.IsZero() && now.Sub(*last) < streamProgressInterval {
		return
	}
	*last = now
	sp.proxy.OnStreamMessageProgress(domain.StreamMessageProgress{
		PairID: sp.id, Direction: name, Framing: string(d.framing), Bytes: int64(bytes),
	})
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

// ending is how d ended, "" while it goes on.
func (sp *streamedPair) ending(d *streamedDirection) string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return d.ending
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
	var flags *uint8
	if d.framing.Enveloped() {
		flags = &message.Flags
	}
	// A compressed message cut short is stored as the compressed bytes
	// that arrived.
	compressed := stream.Compressed(d.framing, message.Flags)
	if compressed {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["encoding"] = d.encoding
	}
	id, err := uuid.NewV7()
	if err != nil {
		return
	}
	row := sp.message(id, name, d, flags, message.Payload, metadata)
	row.Compressed = compressed
	sp.queue(row)
}

// tooLarge records a message over the limit, which is never forwarded, and
// writes a proxy log.
func (sp *streamedPair) tooLarge(name string, d *streamedDirection, err *stream.TooLargeError) {
	sp.mu.Lock()
	d.messages++
	sp.mu.Unlock()
	sp.store(name, d, nil, nil, map[string]any{"too_large": true, "size_seen": err.Size})
	sp.end(d, streamReset, http2.ErrCodeEnhanceYourCalm)
	entry := fmt.Sprintf("Stream message too large (%s): a %s message of %d bytes is over the %d-byte limit (stream_max_message_size); the stream was reset", sp.requestName, name, err.Size, err.Limit)
	_ = sp.proxy.WriteLog("WARN", entry, sp.logOptions()...)
}

// store queues one stream message for storage and announces it.
func (sp *streamedPair) store(name string, d *streamedDirection, flags *uint8, payload []byte, metadata map[string]any) {
	id, err := uuid.NewV7()
	if err != nil {
		return
	}
	sp.queue(sp.message(id, name, d, flags, payload, metadata))
}

// message is a stream message row of the pair's direction d.
func (sp *streamedPair) message(id uuid.UUID, name string, d *streamedDirection, flags *uint8, payload []byte, metadata map[string]any) domain.StreamMessage {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if payload == nil {
		payload = []byte{}
	}
	return domain.StreamMessage{
		ID: id, PairID: sp.id, Direction: name, Framing: string(d.framing), Flags: flags,
		Payload: payload, CreatedAt: time.Now(), Metadata: metadata,
	}
}

// queue queues message for storage and announces it. A pair that is not
// stored, out of scope or skipped, has its messages carried and neither
// stored nor announced.
func (sp *streamedPair) queue(message domain.StreamMessage) {
	if !sp.announced() {
		return
	}
	if sp.proxy.DBWriteChannel != nil {
		stored := message
		sp.proxy.DBWriteChannel <- &stored
	}
	if sp.proxy.OnStreamMessage != nil {
		sp.proxy.OnStreamMessage(message)
	}
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
// metadata.streamed, then announces the pair ended. The engine calls it
// once both directions ended.
func (sp *streamedPair) finish() {
	sp.mu.Lock()
	response := sp.response
	sp.response = nil
	started := sp.started
	ended := domain.StreamedPairEnded{PairID: sp.id, Directions: make(map[string]domain.StreamedDirection, len(sp.directions))}
	streamed := make(map[string]any, len(sp.directions))
	for name, d := range sp.directions {
		direction := domain.StreamedDirection{Framing: string(d.framing), Messages: d.messages, Ending: d.ending, ResetCode: d.resetCode}
		if direction.Ending == "" {
			direction.Ending = streamCancelled
		}
		entry := map[string]any{"framing": direction.Framing, "messages": direction.Messages, "ending": direction.Ending}
		if d.resetCode != "" {
			entry["reset_code"] = d.resetCode
		}
		if len(d.trailers) > 0 {
			trailers := make([]map[string]string, len(d.trailers))
			for i, f := range d.trailers {
				trailers[i] = map[string]string{"name": f.Name, "value": f.Value}
				direction.Trailers = append(direction.Trailers, domain.StreamTrailer{Name: f.Name, Value: f.Value})
			}
			entry["trailers"] = trailers
		}
		streamed[name] = entry
		ended.Directions[name] = direction
	}
	sp.mu.Unlock()
	proxy := sp.proxy
	proxy.streamedPairs.CompareAndDelete(sp.id, sp)
	if started && proxy.OnStreamedPairEnded != nil {
		defer proxy.OnStreamedPairEnded(ended)
	}
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
	proxy.DBWriteChannel <- response
	if proxy.OnResponse != nil {
		proxy.OnResponse(*response)
	}
}

// logOptions ties a proxy log to the pair.
func (sp *streamedPair) logOptions() []func(*domain.Log) error {
	if sp.id != uuid.Nil {
		return []func(*domain.Log) error{core.LogWithReqResID(sp.id)}
	}
	return nil
}
