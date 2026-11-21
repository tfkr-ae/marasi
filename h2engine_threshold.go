package marasi

import (
	"errors"
	"io"
	"strconv"
	"sync"

	"golang.org/x/net/http2"
)

// An HTTP/2 body read up to the body threshold (body_threshold.go). A
// message's body is read whole, unless its content-length declares more
// than the threshold, the body read passes it, or it stalls for
// bodyStallWait after some arrived. Then the message goes on with what
// arrived, cut to the threshold, and the rest follows on an h2Passing as it
// arrives: DATA, then trailers or END_STREAM, or the reset that cut it.
//
// The rest is held in an h2Inbound, as a streamed direction's body is
// (h2engine_streamed.go): the stream's receive window opens again only by
// what went on, so a receiver that stops stops the sender, and memory per
// direction is bounded by the receive window. It goes on through the
// streamed carry's sinks, h2ClientSink and h2OriginSink.

// h2Passing carries the rest of a passed body from the leg that reads it to
// the leg that sends it.
type h2Passing struct {
	in *h2Inbound
	mu sync.Mutex
	// forwarding is set once forward started; forwarded is closed once
	// it returned.
	forwarding bool
	forwarded  chan struct{}
}

// newH2Passing is the passing body of a stream whose receive window is
// window, refunded by refund. rest is what arrived past the threshold
// before the body passed, whose window was given back already.
func newH2Passing(window int64, refund func(uint32), rest []byte) *h2Passing {
	p := &h2Passing{in: newH2Inbound(window, refund), forwarded: make(chan struct{})}
	p.in.preload(rest)
	return p
}

// push adds a DATA frame's data, of a frame frameLength long, the last one
// when end is set. It reports false for a frame past the receive window, a
// FLOW_CONTROL_ERROR.
func (p *h2Passing) push(data []byte, frameLength uint32, end bool) bool {
	if !p.in.put(data, frameLength) {
		return false
	}
	if end {
		p.in.finish(nil)
	}
	return true
}

// end ends the body with trailers.
func (p *h2Passing) end(trailers h2Fields) { p.in.finish(trailers) }

// cut ends the body short, unless it ended: with the sender's reset code,
// or err. With neither it does nothing.
func (p *h2Passing) cut(code *http2.ErrCode, err error) {
	switch {
	case code != nil:
		p.in.closeUnlessEnded(&streamResetError{code: *code})
	case err != nil:
		p.in.closeUnlessEnded(err)
	}
}

// errPassStopped cuts a passed body whose stream ended on the other side.
var errPassStopped = errors.New("the stream the body goes on ended")

// take marks the body as being forwarded, before forward runs on another
// goroutine.
func (p *h2Passing) take() {
	p.mu.Lock()
	p.forwarding = true
	p.mu.Unlock()
}

// taken reports whether the body is being forwarded.
func (p *h2Passing) taken() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forwarding
}

// forward sends the rest of the body to sink as it arrives, taking from
// the inbound only what went, then ends the direction as the sender did.
func (p *h2Passing) forward(sink streamSink) {
	p.take()
	defer close(p.forwarded)
	for {
		data, err := p.in.peek()
		if len(data) > 0 {
			if sink.forward(data) != nil {
				sink.reset(http2.ErrCodeCancel)
				return
			}
			p.in.discard(len(data))
			continue
		}
		var reset *streamResetError
		switch {
		case errors.Is(err, io.EOF):
			sink.end(p.in.endTrailers())
		case errors.As(err, &reset):
			sink.reset(reset.code)
		default:
			sink.reset(http2.ErrCodeCancel)
		}
		return
	}
}

// h2DeclaresOver reports whether fields declare a content-length over
// threshold.
func h2DeclaresOver(fields h2Fields, threshold int64) bool {
	value, ok := fields.get("content-length")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && lengthOver(n, threshold)
}

// startPass makes st's request body pass, with what arrived so far; the
// caller holds h.mu and dispatches st.
func (h *h2Conn) startPass(st *h2Stream) {
	var rest []byte
	st.request.body, rest = cutAtThreshold(st.request.body, h.engine.proxy.bodyThreshold())
	id := st.id
	st.pass = newH2Passing(h2DefaultWindow, func(n uint32) {
		h.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(id, n) })
	}, rest)
	st.request.rest = st.pass
	st.stall.stop()
}

// armStall starts or restarts the wait after which st's request body,
// which has started, passes. The caller holds h.mu.
func (h *h2Conn) armStall(st *h2Stream) {
	st.stall.arrived(func() {
		h.mu.Lock()
		if st.ended || st.gone || st.pass != nil {
			h.mu.Unlock()
			return
		}
		h.startPass(st)
		h.mu.Unlock()
		h.dispatch(st)
	})
}

// awaitRequestPass waits, once st's response went, for the rest of its
// passed request body to go to the origin, as a stream stays open going
// direct until both sides end it.
func (h *h2Conn) awaitRequestPass(st *h2Stream) {
	h.mu.Lock()
	pass := st.pass
	h.mu.Unlock()
	if pass == nil || !pass.taken() {
		return
	}
	select {
	case <-pass.forwarded:
	case <-st.ctx.Done():
	}
}

// forwardRequestPass sends the rest of a passed request body on origin
// stream s, after what arrived.
func (o *h2Origin) forwardRequestPass(s *h2OriginStream, request *h2Request) {
	defer o.requestSent(s)
	sink := h2OriginSink{o: o, s: s}
	if err := sink.forward(request.body); err != nil {
		request.rest.cut(nil, errPassStopped)
		close(request.rest.forwarded)
		return
	}
	request.rest.forward(sink)
}

// receive files a DATA payload, of a frame frameLength long, from the
// origin on s. Until the response body passes it is read whole, and
// receive reports onPass false: the caller gives the window back. Once the
// body passed, the payload goes on the stream's h2Passing, which gives the
// window back as it forwards; ok is false for a frame past that window.
func (s *h2OriginStream) receive(data []byte, frameLength uint32, end bool, threshold int64, refund func(uint32)) (onPass, ok bool) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	if s.pass != nil {
		return true, s.pass.push(data, frameLength, end)
	}
	if s.response == nil {
		s.response = &h2Response{}
	}
	s.response.body = append(s.response.body, data...)
	switch {
	case end:
		s.stall.stop()
	case s.final && int64(len(s.response.body)) > threshold:
		s.startPassLocked(threshold, refund)
	case s.final && len(data) > 0:
		s.stall.arrived(func() {
			s.passMu.Lock()
			defer s.passMu.Unlock()
			if s.pass == nil {
				s.startPassLocked(threshold, refund)
			}
		})
	}
	return false, true
}

// startPassLocked makes the response body pass with what arrived so far,
// and wakes the stream's wait for its response. The caller holds passMu.
// A stream that finished passes nothing: finish ended any passing body it
// found, and one made after it would never end.
func (s *h2OriginStream) startPassLocked(threshold int64, refund func(uint32)) {
	select {
	case <-s.done:
		return
	default:
	}
	var rest []byte
	s.response.body, rest = cutAtThreshold(s.response.body, threshold)
	s.pass = newH2Passing(h2OriginStreamWindow, refund, rest)
	s.response.rest = s.pass
	s.stall.stop()
	close(s.passed)
}

// passing returns the stream's h2Passing, nil while its response body is
// read whole.
func (s *h2OriginStream) passing() *h2Passing {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	return s.pass
}
