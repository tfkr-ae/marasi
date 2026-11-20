package marasi

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
	"github.com/tfkr-ae/marasi/stream"
)

// The HTTP/1.1 engine's streamed carry (stream_carry.go). A direction whose
// head names a message framing is not read whole: its head goes out once
// its chain ran, and its chunked, length-framed or read-until-close body is
// carried a stream message at a time by the direction's one reader
// (streamedPair.carry). The reader takes bytes off the connection only as it
// forwards them, so a receiver that stops reading stops the sender through
// TCP. The request is carried on a goroutine of its own, so the response can
// come while it is sent; the response is carried on the connection's
// goroutine, which waits for both before the connection goes on.
//
// HTTP/1.1 has no reset: a reset, a cancel or a message over the limit
// closes the connection on that leg, and the other leg's reader sees the
// close as a cancel.

// h1StreamedFraming is the message framing a head's Content-Type names, when
// the engine carries it streamed, and FramingNone otherwise: the same rule
// as the HTTP/2 engine's.
func h1StreamedFraming(header *h1.Header, response bool) stream.Framing {
	fields := h2Fields{{Name: "content-type", Value: header.Get("Content-Type")}}
	if encoding := header.Get("Content-Encoding"); encoding != "" {
		fields = append(fields, h2Field{Name: "content-encoding", Value: encoding})
	}
	return h2StreamedFraming(fields, response)
}

// h1Streams is the streamed state of the pair a client connection carries:
// the streamed directions' sources, the request's carry, and what to release
// once both directions ended.
type h1Streams struct {
	// request and response are the streamed directions' bodies, nil for a
	// direction read whole.
	request, response *h1StreamedBody
	// cancelled is set once Marasi closed a leg to reset or cancel the
	// pair, so a reader that then fails was cancelled, not cut short.
	cancelled atomic.Bool
	// gone is closed with cancelled set: a held stream message of either
	// direction is abandoned then.
	gone     chan struct{}
	goneOnce sync.Once
	// requestDirection is the streamed request's record once it is sent.
	requestDirection *streamedDirection
	// carrying is the request's carry.
	carrying sync.WaitGroup
	release  []func()
}

// cancel records that Marasi closed a leg of the pair.
func (s *h1Streams) cancel() {
	s.cancelled.Store(true)
	s.goneOnce.Do(func() { close(s.gone) })
}

// h1StreamedBody is one streamed direction's body: its framing, and its
// source on the sender's leg.
type h1StreamedBody struct {
	framing stream.Framing
	src     *h1Source
}

// h1Source is a streamed direction as it arrives on an HTTP/1.1 leg.
type h1Source struct {
	body    *h1.BodyReader
	conn    net.Conn
	streams *h1Streams
	// client is set on the client's leg, whose close is its cancel. The
	// origin closing partway through a body cuts it short.
	client bool
}

// peek is streamSource.peek. A read that fails once Marasi closed a leg, or
// the client's close, is a cancel.
func (s *h1Source) peek() ([]byte, error) {
	data, err := s.body.Peek()
	if len(data) > 0 || errors.Is(err, io.EOF) {
		return data, err
	}
	if s.streams.cancelled.Load() || (s.client && errors.Is(err, h1.ErrBodyCutShort)) {
		return nil, &streamResetError{code: http2.ErrCodeCancel}
	}
	return nil, err
}

func (s *h1Source) discard(n int) { s.body.Discard(n) }

// cutOff is streamSource.cutOff. HTTP/1.1 has no reset to read while
// nothing is read, so it closes once Marasi closed a leg of the pair.
func (s *h1Source) cutOff() <-chan struct{} { return s.streams.gone }

// trailers are the chunked body's trailer fields, names as written.
func (s *h1Source) trailers() h2Fields {
	trailer := s.body.Trailer()
	var fields h2Fields
	for _, f := range trailer.Fields {
		fields = append(fields, h2Field{Name: f.Name(), Value: f.Value()})
	}
	return fields
}

// reset closes the sender's connection.
func (s *h1Source) reset(http2.ErrCode) {
	s.streams.cancel()
	s.conn.Close()
}

// h1Sink is a streamed direction going out on an HTTP/1.1 leg, each stream
// message in a chunk of its own when the head that went out is chunked, and
// as it is otherwise.
type h1Sink struct {
	bw      *bufio.Writer
	conn    net.Conn
	chunked bool
	streams *h1Streams
}

func (s *h1Sink) forward(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	if s.chunked {
		fmt.Fprintf(s.bw, "%x\r\n", len(raw))
	}
	s.bw.Write(raw)
	if s.chunked {
		s.bw.WriteString("\r\n")
	}
	return s.flush()
}

// end writes the last chunk with the trailers; a body framed otherwise has
// nothing to end it.
func (s *h1Sink) end(trailers h2Fields) error {
	if !s.chunked {
		return s.flush()
	}
	s.bw.WriteString("0\r\n")
	for _, f := range trailers {
		s.bw.WriteString(f.Name + ": " + f.Value + "\r\n")
	}
	s.bw.WriteString("\r\n")
	return s.flush()
}

func (s *h1Sink) flush() error {
	if err := s.bw.Flush(); err != nil {
		if s.streams.cancelled.Load() {
			return &streamResetError{code: http2.ErrCodeCancel}
		}
		return err
	}
	return nil
}

// reset closes the receiver's connection.
func (s *h1Sink) reset(http2.ErrCode) {
	s.streams.cancel()
	s.conn.Close()
}

// isChunked reports whether header's Transfer-Encoding ends in chunked.
func isChunked(header *h1.Header) bool {
	codings := header.Values("Transfer-Encoding")
	if len(codings) == 0 {
		return false
	}
	last := codings[len(codings)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	return strings.EqualFold(strings.TrimSpace(last), "chunked")
}

// streamedRequest makes a request whose head names a message framing a
// streamed one: its body is left on the client connection for the carry.
// It reports false for a request read whole.
func (c *h1Conn) streamedRequest(head *h1.RequestHead, framing h1.Framing) bool {
	if framing.Kind == h1.NoBody || (framing.Kind == h1.LengthFramed && framing.Length == 0) {
		return false
	}
	f := h1StreamedFraming(&head.Header, false)
	if f == stream.FramingNone {
		return false
	}
	streams := c.pairStreams()
	streams.request = &h1StreamedBody{framing: f, src: &h1Source{body: h1.NewBodyReader(c.br, framing), conn: c.conn, streams: streams, client: true}}
	return true
}

// carryStreamedRequest carries the client's stream messages to origin, after
// the head that went out, on a goroutine of its own, so the response can
// come while the request is sent.
func (c *h1Conn) carryStreamedRequest(pair *Pair, origin *originConn, head *h1.RequestHead) {
	streams := c.streams
	body := streams.request
	sp := c.engine.proxy.streamed(pair)
	d := sp.direction(domain.StreamDirectionRequest, body.framing)
	streams.requestDirection = d
	chunked := isChunked(&head.Header)
	if head.Header.Has("Content-Length") && !chunked {
		sp.declareLength(d)
	}
	sink := &h1Sink{bw: origin.bw, conn: origin.conn, chunked: chunked, streams: streams}
	streams.carrying.Add(1)
	go func() {
		defer streams.carrying.Done()
		sp.carry(domain.StreamDirectionRequest, d, body.src, sink)
	}()
}

// streamedResponse makes a response whose head names a message framing a
// streamed one: its body is left on origin's connection for the carry, and
// the view's body is empty. It reports false for a response read whole.
func (c *h1Conn) streamedResponse(origin *originConn, head *h1.ResponseHead, framing h1.Framing, res *http.Response) bool {
	if head.StatusCode < 200 || framing.Kind == h1.NoBody || (framing.Kind == h1.LengthFramed && framing.Length == 0) {
		return false
	}
	f := h1StreamedFraming(&head.Header, true)
	if f == stream.FramingNone {
		return false
	}
	streams := c.pairStreams()
	streams.response = &h1StreamedBody{framing: f, src: &h1Source{body: h1.NewBodyReader(origin.br, framing), conn: origin.conn, streams: streams}}
	res.Body = http.NoBody
	return true
}

// pairStreams is the streamed state of the pair being carried, made the
// first time.
func (c *h1Conn) pairStreams() *h1Streams {
	if c.streams == nil {
		c.streams = &h1Streams{gone: make(chan struct{})}
	}
	return c.streams
}

// carryStreamedResponse writes a streamed response's head, with the chain's
// edits, and carries the origin's stream messages after it. keep is whether
// the client's head keeps the connection open; it reports whether the
// connection carries another request.
func (c *h1Conn) carryStreamedResponse(pair *Pair, res *http.Response, wire *wireResponse, d *streamedDirection, keep bool) bool {
	streams := c.streams
	body := streams.response
	if pair.FromMarasi() {
		// The chain answered with a response of Marasi's own.
		body.src.reset(http2.ErrCodeCancel)
		c.closeOrigin()
		res.Write(c.bw)
		c.bw.Flush()
		return false
	}
	// The origin's own head says whether its connection goes on.
	originKeeps := wire.body.Framing.Kind != h1.UntilClose && keepAlive(wire.head.Proto, &wire.head.Header)
	if pair.responseWire != nil {
		wire = pair.responseWire
	}
	// The stream messages follow the head: a body a modifier gave the view
	// is not sent.
	res.Body = http.NoBody
	out, err := outboundResponse(wire, res)
	if err != nil {
		body.src.reset(http2.ErrCodeCancel)
		c.closeOrigin()
		return c.failClientLeg(pair, err)
	}
	out.head.WriteTo(c.bw)
	if err := c.bw.Flush(); err != nil {
		body.src.reset(http2.ErrCodeCancel)
		return false
	}
	chunked := isChunked(&out.head.Header)
	if out.head.Header.Has("Content-Length") && !chunked {
		pair.stream.declareLength(d)
	}
	sink := &h1Sink{bw: c.bw, conn: c.conn, chunked: chunked, streams: streams}
	pair.stream.carry(domain.StreamDirectionResponse, d, body.src, sink)
	complete := pair.stream.ending(d) == streamComplete
	if !originKeeps || !complete {
		c.closeOrigin()
	}
	return keep && c.origin != nil && wire.body.Framing.Kind != h1.UntilClose && complete
}

// endStreams ends the streamed pair the connection carried, if any, and
// reports whether the connection carries another request: another, unless
// a streamed request did not end complete. A connection that does not go on
// has both legs closed first, so the request's carry ends too. It then waits
// for the request's carry, releases what the directions held, and stores
// the pair with how each direction ended.
func (c *h1Conn) endStreams(pair *Pair, another bool) bool {
	streams := c.streams
	if streams == nil {
		return another
	}
	c.streams = nil
	if streams.request != nil && (pair.stream == nil || streams.requestDirection == nil) {
		// The request's body was never carried, so it is still unread.
		another = false
	}
	if !another {
		streams.cancel()
		c.closeOrigin()
		if !c.hijacked {
			c.conn.Close()
		}
	}
	streams.carrying.Wait()
	if streams.requestDirection != nil && pair.stream.ending(streams.requestDirection) != streamComplete {
		another = false
	}
	for _, release := range streams.release {
		release()
	}
	if pair.stream != nil {
		pair.stream.finish()
	}
	return another
}
