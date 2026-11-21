package marasi

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// The HTTP/2 engine's streamed carry (stream_carry.go). A direction whose
// head names a message framing that h2CarriesFraming takes has its DATA put
// in an h2Inbound instead of its message's body. The direction's one reader
// (streamedPair.carry) takes bytes out as it reads each stream message whole,
// and only then is the stream's receive window opened again by what it took:
// a message it holds unforwarded, because the other leg's window is shut,
// leaves the sender's window shut too. Memory per direction is bounded by
// one message and the receive window. The connection's window is opened
// again at once, as for any stream, so one streamed stream never stalls the
// others.

// h2CarriesFraming reports whether the HTTP/2 engine carries a direction
// with framing as stream messages: every message framing. An ordinary body
// is read whole, as before.
func h2CarriesFraming(framing stream.Framing) bool {
	return framing != stream.FramingNone
}

// h2StreamedFraming is the message framing a head's content-type names, when
// the engine carries it streamed, and FramingNone otherwise. A gRPC-Web body
// in base64 (application/grpc-web-text) is read whole, since its messages
// cannot be cut before it is decoded. A server-sent event or NDJSON body
// with a Content-Encoding is carried streamed when the coding is gzip or
// deflate and the head declares no length, since the proxy compresses it
// again toward the receiver (stream_body_compression.go); otherwise it is
// read whole.
func h2StreamedFraming(fields h2Fields, response bool) stream.Framing {
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
	if h2CarriesFraming(framing) {
		return framing
	}
	return stream.FramingNone
}

// h2BodyEncoding is the whole-body coding of a streamed head whose framing
// has no per-message compression, "" for none.
func h2BodyEncoding(fields h2Fields, framing stream.Framing) string {
	if framing.Enveloped() {
		return ""
	}
	contentEncoding, _ := fields.get("content-encoding")
	encoding, _ := streamedBodyEncoding(contentEncoding)
	return encoding
}

// h2OriginStreamWindow is the receive window Marasi gives each origin
// stream (SETTINGS_INITIAL_WINDOW_SIZE), so a streamed response whose reader
// stopped stops the origin. Bodies read whole open it again with each DATA
// frame.
const h2OriginStreamWindow = 1 << 20

// h2Inbound is one streamed direction's body as its leg's frame reader
// receives it, until the direction's reader takes it.
type h2Inbound struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
	// window is how much the sender may still send: the stream's receive
	// window.
	window int64
	// refund opens the stream's receive window by n.
	refund func(n uint32)
	// end is how the direction ended, nil while it goes on.
	end      error
	trailers h2Fields
	// gone is closed when the direction is reset or cut off.
	gone chan struct{}
	// given is how many bytes at the front of buf the sender's window was
	// opened by already (preload).
	given int
}

func newH2Inbound(window int64, refund func(uint32)) *h2Inbound {
	in := &h2Inbound{window: window, refund: refund, gone: make(chan struct{})}
	in.cond = sync.NewCond(&in.mu)
	return in
}

// put adds a DATA frame's data, of a frame frameLength long, padding
// included. It reports false for a frame past the receive window, a
// FLOW_CONTROL_ERROR (RFC 9113 §6.9.1). Padding is given back at once.
func (in *h2Inbound) put(data []byte, frameLength uint32) bool {
	in.mu.Lock()
	if int64(frameLength) > in.window {
		in.mu.Unlock()
		return false
	}
	in.window -= int64(frameLength)
	padding := frameLength - uint32(len(data))
	if in.end == nil {
		in.buf = append(in.buf, data...)
	}
	in.mu.Unlock()
	in.cond.Broadcast()
	if padding > 0 {
		in.give(padding)
	}
	return true
}

// preload puts data ahead of what arrives, data whose window was opened
// again already: what a passed body read past the threshold while it was
// read whole.
func (in *h2Inbound) preload(data []byte) {
	if len(data) == 0 {
		return
	}
	in.mu.Lock()
	in.buf = append(data[:len(data):len(data)], in.buf...)
	in.given += len(data)
	in.mu.Unlock()
	in.cond.Broadcast()
}

// give opens the receive window by n.
func (in *h2Inbound) give(n uint32) {
	in.mu.Lock()
	in.window += int64(n)
	ended := in.end != nil
	in.mu.Unlock()
	if !ended && n > 0 {
		in.refund(n)
	}
}

// finish ends the direction as its sender ended it, with trailers when it
// sent them.
func (in *h2Inbound) finish(trailers h2Fields) {
	in.mu.Lock()
	if in.end == nil {
		in.end, in.trailers = io.EOF, trailers
	}
	in.mu.Unlock()
	in.cond.Broadcast()
}

// close ends the direction with err, unless it already ended. What is
// buffered is still read, except after a reset.
func (in *h2Inbound) close(err error) {
	in.mu.Lock()
	if in.end == nil {
		in.end = err
	}
	select {
	case <-in.gone:
	default:
		close(in.gone)
	}
	in.mu.Unlock()
	in.cond.Broadcast()
}

// closeUnlessEnded is close for the origin's stream ending, by its reset
// or the connection, after the direction may have ended. Once the sender
// ended it cleanly, as a gRPC origin does with its trailers before it
// resets the stream to stop the request, the direction stays as it ended,
// and a hold of its trailers goes on.
func (in *h2Inbound) closeUnlessEnded(err error) {
	in.mu.Lock()
	ended := in.end == io.EOF
	in.mu.Unlock()
	if !ended {
		in.close(err)
	}
}

// peek is streamSource.peek. Bytes that arrived before the end are read
// first, so what arrived of a message cut short still goes on.
func (in *h2Inbound) peek() ([]byte, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for len(in.buf) == 0 && in.end == nil {
		in.cond.Wait()
	}
	if len(in.buf) > 0 {
		return in.buf, nil
	}
	return nil, in.end
}

// discard is streamSource.discard: the window opens by what was taken.
func (in *h2Inbound) discard(n int) {
	in.mu.Lock()
	in.buf = in.buf[n:]
	if len(in.buf) == 0 {
		in.buf = nil
	}
	given := min(n, in.given)
	in.given -= given
	in.mu.Unlock()
	in.give(uint32(n - given))
}

// endTrailers are the trailers the direction ended with, once peek
// returned io.EOF.
func (in *h2Inbound) endTrailers() h2Fields {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.trailers
}

// h2StreamedBody is a streamed direction's body on a wire message: its
// framing and its source, the inbound and how to reset the sender.
type h2StreamedBody struct {
	framing stream.Framing
	// encoding is the whole-body coding of a response's head, "" for
	// none: only server-sent events and NDJSON, response framings, have
	// one.
	encoding string
	in       *h2Inbound
	resetBy  func(http2.ErrCode)
}

func (b *h2StreamedBody) peek() ([]byte, error)    { return b.in.peek() }
func (b *h2StreamedBody) discard(n int)            { b.in.discard(n) }
func (b *h2StreamedBody) reset(code http2.ErrCode) { b.resetBy(code) }
func (b *h2StreamedBody) cutOff() <-chan struct{}  { return b.in.gone }

func (b *h2StreamedBody) trailers() h2Fields { return b.in.endTrailers() }

// h2ClientSink is a streamed response going to the client on st.
type h2ClientSink struct {
	h  *h2Conn
	st *h2Stream
}

func (s h2ClientSink) forward(raw []byte) error {
	if err := sendData(s.h.w, s.h.flow, &s.st.window, s.st.id, raw, false); err != nil {
		return s.h.endedBy(s.st, err)
	}
	return nil
}

func (s h2ClientSink) end(trailers h2Fields) error {
	if s.h.gone(s.st) {
		return errStreamGone
	}
	if len(trailers) > 0 {
		_, err := s.h.w.headers(fixedID(s.st.id), trailers, true, s.h.flow.frameSize())
		return err
	}
	return sendData(s.h.w, s.h.flow, &s.st.window, s.st.id, nil, true)
}

func (s h2ClientSink) reset(code http2.ErrCode) {
	if !s.h.gone(s.st) {
		s.h.resetStream(s.st.id, code)
	}
}

// endedBy is why a send on st failed: the client's reset, when it reset
// the stream.
func (h *h2Conn) endedBy(st *h2Stream, err error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st.clientReset != nil {
		return &streamResetError{code: *st.clientReset}
	}
	return err
}

// h2OriginSink is a streamed request going to the origin on stream s.
type h2OriginSink struct {
	o *h2Origin
	s *h2OriginStream
}

func (k h2OriginSink) forward(raw []byte) error {
	if err := sendData(k.o.w, k.o.flow, &k.s.window, k.s.id, raw, false); err != nil {
		return k.s.endedBy(err)
	}
	return nil
}

func (k h2OriginSink) end(trailers h2Fields) error {
	if len(trailers) > 0 {
		if !k.o.open(k.s) {
			return k.s.endedBy(errStreamGone)
		}
		_, err := k.o.w.headers(fixedID(k.s.id), trailers, true, k.o.flow.frameSize())
		return err
	}
	if err := sendData(k.o.w, k.o.flow, &k.s.window, k.s.id, nil, true); err != nil {
		return k.s.endedBy(err)
	}
	return nil
}

func (k h2OriginSink) reset(code http2.ErrCode) { k.o.cancel(k.s, code) }

// endedBy is why a send on s failed: the origin's reset, when it reset the
// stream.
func (s *h2OriginStream) endedBy(err error) error {
	if s.reset != nil {
		return &streamResetError{code: *s.reset}
	}
	return err
}

// sendOn sends the pair's request on a new stream of origin. A streamed
// request sends its head, and its body is carried from st's inbound on a
// goroutine of its own, so the response can come while it is sent.
func (h *h2Conn) sendOn(origin *h2Origin, st *h2Stream, pair *Pair, out *h2Request, opened func(originID uint32)) (*h2OriginStream, error) {
	body := st.request.streamed
	if body == nil {
		return origin.send(out, opened)
	}
	s, err := origin.sendHead(out.fields, opened)
	if err != nil {
		return nil, err
	}
	sp := h.engine.proxy.streamed(pair)
	d := sp.direction(domain.StreamDirectionRequest, body.framing)
	sp.headWentOut(d, out.fields)
	if _, ok := out.fields.get("content-length"); ok {
		sp.declareLength(d)
	}
	st.carrying.Add(1)
	go func() {
		defer st.carrying.Done()
		sp.carry(domain.StreamDirectionRequest, d, body, h2OriginSink{o: origin, s: s})
	}()
	return s, nil
}

// carryStreamedResponse writes the response head on st, with the chain's
// edits, and carries the origin's stream messages after it.
func (h *h2Conn) carryStreamedResponse(st *h2Stream, pair *Pair, res *http.Response, wire *wireResponse, body *h2StreamedBody, d *streamedDirection) {
	out, err := h2OutboundResponse(wire.h2, res)
	if err == nil && !h.gone(st) {
		_, err = h.w.headers(fixedID(st.id), out.fields, false, h.flow.frameSize())
	}
	if err != nil || h.gone(st) {
		body.reset(http2.ErrCodeCancel)
		h.resetStream(st.id, http2.ErrCodeCancel)
		return
	}
	pair.stream.headWentOut(d, out.fields)
	encoding, _ := out.fields.get("content-encoding")
	pair.stream.bodyEncodings(d, body.encoding, encoding)
	if _, ok := out.fields.get("content-length"); ok {
		pair.stream.declareLength(d)
	}
	pair.stream.carry(domain.StreamDirectionResponse, d, body, h2ClientSink{h: h, st: st})
}

// readStreamedBody reads a streamed request's body whole, for a request
// translated to HTTP/1.1, which is carried read whole.
func readStreamedBody(body *h2StreamedBody) ([]byte, h2Fields, error) {
	var data []byte
	for {
		chunk, err := body.peek()
		if len(chunk) > 0 {
			data = append(data, chunk...)
			body.discard(len(chunk))
			continue
		}
		if errors.Is(err, io.EOF) {
			return data, body.trailers(), nil
		}
		return nil, nil, err
	}
}
