package marasi

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// An HTTP/2 body read up to the body threshold (body_threshold.go). A
// message's body is read whole, unless its content-length declares more
// than the threshold, the body read passes it, or it stalls for
// bodyStallWait after some arrived. Then the message goes on with what
// arrived, cut to the threshold, and the rest follows on an h2Pass as it
// arrives: DATA, then trailers or END_STREAM, or the reset that cut it.

// h2Pass carries the rest of a passed body from the leg that reads it to
// the leg that sends it.
type h2Pass struct {
	mu   sync.Mutex
	more chan struct{}
	data [][]byte
	// ended is set once the sender ended the body, with trailers when it
	// sent them; reset when it reset the stream instead, err when the
	// stream ended otherwise.
	ended    bool
	trailers h2Fields
	reset    *http2.ErrCode
	err      error
	// forwarding is set once forward started; forwarded is closed once
	// it returned.
	forwarding bool
	forwarded  chan struct{}
}

func newH2Pass() *h2Pass {
	return &h2Pass{more: make(chan struct{}, 1), forwarded: make(chan struct{})}
}

func (p *h2Pass) signal() {
	select {
	case p.more <- struct{}{}:
	default:
	}
}

// push adds a DATA payload, the last one when end is set.
func (p *h2Pass) push(data []byte, end bool) {
	p.mu.Lock()
	if len(data) > 0 {
		p.data = append(p.data, append([]byte(nil), data...))
	}
	p.ended = p.ended || end
	p.mu.Unlock()
	p.signal()
}

// end ends the body with trailers.
func (p *h2Pass) end(trailers h2Fields) {
	p.mu.Lock()
	p.ended, p.trailers = true, trailers
	p.mu.Unlock()
	p.signal()
}

// cut ends the body short: with the sender's reset code, or err.
func (p *h2Pass) cut(code *http2.ErrCode, err error) {
	p.mu.Lock()
	if !p.ended && p.reset == nil && p.err == nil {
		p.reset, p.err = code, err
	}
	p.mu.Unlock()
	p.signal()
}

// errPassStopped cuts a passed body whose stream ended on the other side.
var errPassStopped = errors.New("the stream the body goes on ended")

// h2PassSink is where a passed body goes: DATA, then trailers or the end,
// or a reset.
type h2PassSink struct {
	data     func(data []byte, end bool) error
	trailers func(h2Fields) error
	reset    func(code *http2.ErrCode)
}

// take marks the body as being forwarded, before forward runs on another
// goroutine.
func (p *h2Pass) take() {
	p.mu.Lock()
	p.forwarding = true
	p.mu.Unlock()
}

// taken reports whether the body is being forwarded.
func (p *h2Pass) taken() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forwarding
}

// forward sends the rest of the body to sink as it arrives.
func (p *h2Pass) forward(sink h2PassSink) {
	p.take()
	defer close(p.forwarded)
	for {
		p.mu.Lock()
		data, ended, trailers, reset, err := p.data, p.ended, p.trailers, p.reset, p.err
		p.data = nil
		p.mu.Unlock()
		for _, d := range data {
			if sink.data(d, false) != nil {
				sink.reset(nil)
				return
			}
		}
		switch {
		case reset != nil || err != nil:
			sink.reset(reset)
			return
		case ended && len(trailers) > 0:
			sink.trailers(trailers)
			return
		case ended:
			sink.data(nil, true)
			return
		}
		<-p.more
	}
}

// declaredOver reports whether fields declare a content-length over
// threshold.
func declaredOver(fields h2Fields, threshold int64) bool {
	value, ok := fields.get("content-length")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > threshold
}

// splitPassed cuts a body that passed threshold: what arrived up to the
// threshold stays, the rest starts the h2Pass.
func splitPassed(body []byte, threshold int64) ([]byte, *h2Pass) {
	pass := newH2Pass()
	if int64(len(body)) > threshold {
		pass.push(body[threshold:], false)
		body = body[:threshold:threshold]
	}
	return body, pass
}

// startPass makes st's request body pass, with what arrived so far; the
// caller holds h.mu and dispatches st.
func (h *h2Conn) startPass(st *h2Stream) {
	st.request.body, st.pass = splitPassed(st.request.body, h.engine.proxy.bodyThreshold())
	st.request.rest = st.pass
	if st.stall != nil {
		st.stall.Stop()
	}
}

// armStall starts or restarts the wait after which st's request body,
// which has started, passes. The caller holds h.mu.
func (h *h2Conn) armStall(st *h2Stream) {
	if st.stall != nil {
		st.stall.Reset(bodyStallWait)
		return
	}
	st.stall = time.AfterFunc(bodyStallWait, func() {
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
	if err := sendData(o.w, o.flow, &s.window, s.id, request.body, false); err != nil {
		request.rest.cut(nil, errPassStopped)
		close(request.rest.forwarded)
		return
	}
	request.rest.forward(h2PassSink{
		data: func(data []byte, end bool) error {
			return sendData(o.w, o.flow, &s.window, s.id, data, end)
		},
		trailers: func(trailers h2Fields) error {
			_, err := o.w.headers(fixedID(s.id), trailers, true, o.flow.frameSize())
			return err
		},
		reset: func(*http2.ErrCode) { o.cancel(s, http2.ErrCodeCancel) },
	})
}

// forwardResponsePass sends the rest of a passed response body on st, and
// resets st with the origin's code when the origin reset its stream.
func (h *h2Conn) forwardResponsePass(st *h2Stream, rest *h2Pass) {
	rest.forward(h2PassSink{
		data: func(data []byte, end bool) error {
			return sendData(h.w, h.flow, &st.window, st.id, data, end)
		},
		trailers: func(trailers h2Fields) error {
			_, err := h.w.headers(fixedID(st.id), trailers, true, h.flow.frameSize())
			return err
		},
		reset: func(code *http2.ErrCode) {
			if code == nil {
				cancel := http2.ErrCodeCancel
				code = &cancel
			}
			h.resetStream(st.id, *code)
		},
	})
}

// receive files a DATA payload from the origin on s. Until the response
// body passes it is read whole; then, and from the moment it passes, the
// payload goes on the stream's h2Pass, and receive reports true.
func (s *h2OriginStream) receive(data []byte, end bool, threshold int64) bool {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	if s.pass != nil {
		s.pass.push(data, end)
		return true
	}
	if s.response == nil {
		s.response = &h2Response{}
	}
	s.response.body = append(s.response.body, data...)
	switch {
	case end:
		if s.stall != nil {
			s.stall.Stop()
		}
	case s.final && int64(len(s.response.body)) > threshold:
		s.startPassLocked(threshold)
		return true
	case s.final && len(data) > 0:
		if s.stall == nil {
			s.stall = time.AfterFunc(bodyStallWait, func() {
				s.passMu.Lock()
				defer s.passMu.Unlock()
				select {
				case <-s.done:
					return
				default:
				}
				if s.pass == nil {
					s.startPassLocked(threshold)
				}
			})
		} else {
			s.stall.Reset(bodyStallWait)
		}
	}
	return false
}

// startPassLocked makes the response body pass with what arrived so far,
// and wakes the stream's wait for its response. The caller holds passMu.
func (s *h2OriginStream) startPassLocked(threshold int64) {
	s.response.body, s.pass = splitPassed(s.response.body, threshold)
	s.response.rest = s.pass
	if s.stall != nil {
		s.stall.Stop()
	}
	close(s.passed)
}

// passing returns the stream's h2Pass, nil while its response body is read
// whole.
func (s *h2OriginStream) passing() *h2Pass {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	return s.pass
}
