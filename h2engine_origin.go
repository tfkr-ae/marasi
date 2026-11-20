package marasi

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/stream"
)

// The origin leg of an HTTP/2 client connection: Marasi as the origin's
// client, on the one origin connection the client connection owns
// (docs/adr/2026-11-17-one-origin-connection-per-client-connection.md). It
// sends ENABLE_PUSH=0 and opens one origin stream for each client stream.

// h2OriginSettingsTimeout bounds the wait for the origin's first SETTINGS.
const h2OriginSettingsTimeout = 10 * time.Second

// h2Origin is the origin leg.
type h2Origin struct {
	conn *routedConn
	addr string

	w    *h2Writer
	flow *h2Flow

	// maxStreams is the origin's SETTINGS_MAX_CONCURRENT_STREAMS, which
	// the client is told; set is false when the origin sent none.
	maxStreams    uint32
	maxStreamsSet bool

	// onGoAway is called with each GOAWAY the origin sends, and onEnd once
	// the connection ends.
	onGoAway func(last uint32, code http2.ErrCode, debug []byte)
	onEnd    func()

	// block is the header block being read; only read touches it.
	block h2Block
	// threshold is the body threshold its response bodies are read up to.
	threshold int64

	mu      sync.Mutex
	streams map[uint32]*h2OriginStream
	nextID  uint32
	// gone is set once the origin sent GOAWAY, with the last stream it
	// processes; err once the connection ended.
	gone     bool
	goneLast uint32
	err      error
	// relayed is closed once the first GOAWAY has been handled: onGoAway
	// returned and the streams it left unprocessed were finished.
	relayed chan struct{}
}

// h2OriginStream is one origin stream: the request sent on it and, once done
// is closed, the response that came back.
type h2OriginStream struct {
	id     uint32
	window h2SendWindow
	done   chan struct{}

	// interim holds each 1xx response from its arrival until it is taken
	// (takeInterim); moreInterim is signalled when one arrives.
	interimMu   sync.Mutex
	interim     []h2Fields
	moreInterim chan struct{}

	// headed is closed once a streamed response's head arrived: its body
	// goes on in response.streamed, and done closes when the stream ends.
	headed chan struct{}
	// streamed is response.streamed, for the goroutines other than the
	// leg's reader, which reset or end the stream.
	streamed atomic.Pointer[h2StreamedBody]

	// Set before done or headed is closed, and only read after; or for a
	// response body that passed the threshold, response is set before
	// passed is closed, and its rest goes on pass (h2engine_threshold.go).
	response *h2Response
	final    bool
	reset    *http2.ErrCode
	err      error

	// sending is set while the rest of a passed request body is sent,
	// and finished once the response finished; both guarded by
	// h2Origin.mu.
	sending  bool
	finished bool

	passMu sync.Mutex
	pass   *h2Pass
	passed chan struct{}
	stall  *time.Timer
}

func newH2OriginStream() *h2OriginStream {
	return &h2OriginStream{done: make(chan struct{}), headed: make(chan struct{}), moreInterim: make(chan struct{}, 1), passed: make(chan struct{})}
}

// addInterim files an interim response for takeInterim.
func (s *h2OriginStream) addInterim(fields h2Fields) {
	s.interimMu.Lock()
	s.interim = append(s.interim, fields)
	s.interimMu.Unlock()
	select {
	case s.moreInterim <- struct{}{}:
	default:
	}
}

// takeInterim returns the interim responses filed since it last ran.
func (s *h2OriginStream) takeInterim() []h2Fields {
	s.interimMu.Lock()
	defer s.interimMu.Unlock()
	interim := s.interim
	s.interim = nil
	return interim
}

// errOriginGoneAway is the cause for a request the origin will not process:
// it sent GOAWAY naming a last stream below the request's.
var errOriginGoneAway = errors.New("the origin went away (GOAWAY) before it processed the request")

// startH2Origin opens the HTTP/2 origin leg on conn, which negotiated h2:
// it sends the connection preface and SETTINGS with ENABLE_PUSH=0, waits for
// the origin's SETTINGS, and reads the origin's frames from then on.
func startH2Origin(conn *routedConn, addr string, onGoAway func(uint32, http2.ErrCode, []byte), onEnd func(), threshold int64) (*h2Origin, error) {
	bw := bufio.NewWriter(conn)
	br := bufio.NewReader(conn)
	framer := http2.NewFramer(bw, br)
	framer.AllowIllegalWrites = true
	o := &h2Origin{
		conn: conn, addr: addr, w: newH2Writer(bw, framer), flow: newH2Flow(),
		onGoAway: onGoAway, onEnd: onEnd, threshold: threshold,
		streams: make(map[uint32]*h2OriginStream), nextID: 1,
		relayed: make(chan struct{}),
	}
	if _, err := io.WriteString(bw, http2.ClientPreface); err != nil {
		return nil, err
	}
	// A large connection window, which Marasi opens again as data
	// arrives, and a stream window a streamed response's reader opens only
	// as it reads (h2engine_streamed.go); a body read whole opens it with
	// each DATA frame.
	err := o.w.do(func(f *http2.Framer) error {
		if err := f.WriteSettings(
			http2.Setting{ID: http2.SettingEnablePush, Val: 0},
			http2.Setting{ID: http2.SettingInitialWindowSize, Val: h2OriginStreamWindow},
		); err != nil {
			return err
		}
		return f.WriteWindowUpdate(0, h2MaxWindow-h2DefaultWindow)
	})
	if err != nil {
		return nil, fmt.Errorf("sending the origin the HTTP/2 preface: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(h2OriginSettingsTimeout))
	frame, err := framer.ReadFrame()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, fmt.Errorf("reading the origin's HTTP/2 SETTINGS: %w", err)
	}
	settings, ok := frame.(*http2.SettingsFrame)
	if !ok || settings.IsAck() {
		return nil, fmt.Errorf("the origin opened HTTP/2 with a %s frame, not SETTINGS", frame.Header().Type)
	}
	if err := o.settings(settings); err != nil {
		return nil, err
	}
	go o.read(framer)
	return o, nil
}

// settings applies the origin's SETTINGS and acknowledges them.
func (o *h2Origin) settings(frame *http2.SettingsFrame) error {
	if err := frame.ForeachSetting(func(s http2.Setting) error { return s.Valid() }); err != nil {
		return err
	}
	if v, ok := frame.Value(http2.SettingMaxConcurrentStreams); ok {
		o.mu.Lock()
		o.maxStreams, o.maxStreamsSet = v, true
		o.mu.Unlock()
	}
	if v, ok := frame.Value(http2.SettingHeaderTableSize); ok {
		o.w.mu.Lock()
		o.w.encoder.SetMaxDynamicTableSizeLimit(v)
		o.w.mu.Unlock()
	}
	o.mu.Lock()
	windows := make([]*h2SendWindow, 0, len(o.streams))
	for _, s := range o.streams {
		windows = append(windows, &s.window)
	}
	ok := o.flow.settings(frame, windows)
	o.mu.Unlock()
	if !ok {
		return http2.ConnectionError(http2.ErrCodeFlowControl)
	}
	return o.w.do(func(f *http2.Framer) error { return f.WriteSettingsAck() })
}

// read reads the origin's frames until the connection ends.
func (o *h2Origin) read(framer *http2.Framer) {
	decoder := hpack.NewDecoder(4096, nil)
	var err error
	for err == nil {
		var frame http2.Frame
		frame, err = framer.ReadFrame()
		if err == nil {
			err = o.frame(frame, decoder)
		}
	}
	var connErr http2.ConnectionError
	if errors.As(err, &connErr) {
		o.w.do(func(f *http2.Framer) error { return f.WriteGoAway(0, http2.ErrCode(connErr), nil) })
	}
	o.end(fmt.Errorf("the origin's HTTP/2 connection ended: %w", err))
}

// frame handles one frame from the origin.
func (o *h2Origin) frame(frame http2.Frame, decoder *hpack.Decoder) error {
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if !f.IsAck() {
			return o.settings(f)
		}
	case *http2.PingFrame:
		if !f.IsAck() {
			return o.w.do(func(fr *http2.Framer) error { return fr.WritePing(true, f.Data) })
		}
	case *http2.WindowUpdateFrame:
		if f.StreamID == 0 {
			if !o.flow.grow(nil, int64(f.Increment)) {
				return http2.ConnectionError(http2.ErrCodeFlowControl)
			}
		} else if s := o.stream(f.StreamID); s != nil && !o.flow.grow(&s.window, int64(f.Increment)) {
			// A stream error (RFC 9113 §6.9.1): the stream is reset and
			// the client answered with a proxy error.
			o.w.do(func(fr *http2.Framer) error { return fr.WriteRSTStream(s.id, http2.ErrCodeFlowControl) })
			o.finish(s, errors.New("the origin overflowed the stream's flow-control window (FLOW_CONTROL_ERROR)"))
		}
	case *http2.HeadersFrame:
		if err := o.block.start(f); err != nil {
			return err
		}
		if f.HeadersEnded() {
			return o.headers(decoder)
		}
	case *http2.ContinuationFrame:
		if err := o.block.add(f.HeaderBlockFragment()); err != nil {
			return err
		}
		if f.HeadersEnded() {
			return o.headers(decoder)
		}
	case *http2.DataFrame:
		n := f.Header().Length
		if n > 0 {
			o.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(0, n) })
		}
		s := o.stream(f.StreamID)
		if s == nil {
			return nil
		}
		if s.final && s.response.streamed != nil {
			o.streamedData(s, f)
			return nil
		}
		if s.receive(f.Data(), f.StreamEnded(), o.threshold) {
			if f.StreamEnded() {
				o.finish(s, nil)
			} else if n > 0 {
				o.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(f.StreamID, n) })
			}
			return nil
		}
		if f.StreamEnded() {
			s.response.endsOnData = true
			o.finish(s, nil)
		} else if n > 0 {
			o.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(f.StreamID, n) })
		}
	case *http2.RSTStreamFrame:
		if s := o.stream(f.StreamID); s != nil {
			if s.final && s.response.streamed != nil {
				s.response.streamed.in.closeUnlessEnded(&streamResetError{code: f.ErrCode})
			}
			o.resetBy(s, f.ErrCode)
		}
	case *http2.GoAwayFrame:
		o.mu.Lock()
		first := !o.gone
		o.gone, o.goneLast = true, f.LastStreamID
		var unprocessed []*h2OriginStream
		for id, s := range o.streams {
			if id > f.LastStreamID {
				unprocessed = append(unprocessed, s)
			}
		}
		o.mu.Unlock()
		// The GOAWAY is relayed before the unprocessed streams finish, so
		// the client streams it leaves unanswered are gone by the time
		// their waits end, and are not answered with a 502.
		if o.onGoAway != nil {
			o.onGoAway(f.LastStreamID, f.ErrCode, f.DebugData())
		}
		for _, s := range unprocessed {
			o.finish(s, errOriginGoneAway)
		}
		if first {
			close(o.relayed)
		}
	case *http2.PushPromiseFrame:
		// Marasi sent ENABLE_PUSH=0 (RFC 9113 §8.4).
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	return nil
}

// headers decodes a whole header block from the origin and files it on its
// stream: an interim response, the final one, or its trailers. A block on a
// stream no longer open is decoded all the same, to keep the table in step.
func (o *h2Origin) headers(decoder *hpack.Decoder) error {
	fields, err := o.block.decode(decoder)
	if err != nil {
		return err
	}
	s := o.stream(o.block.id)
	if s == nil {
		return nil
	}
	switch status, _ := fields.get(":status"); {
	case s.final && s.response.streamed != nil:
		// A streamed response's trailers end its body, not the
		// stream: the request may still be going out.
		s.response.streamed.in.finish(fields)
		return nil
	case s.final && s.passing() != nil:
		s.passing().end(fields)
	case s.final:
		s.response.trailers = fields
	case len(status) == 3 && status[0] == '1' && status != "101":
		s.addInterim(fields)
	default:
		s.passMu.Lock()
		s.final = true
		if s.response == nil {
			s.response = &h2Response{}
		}
		s.response.fields = fields
		framing := h2StreamedFraming(fields, true)
		if framing == stream.FramingNone && !o.block.endStream && declaredOver(fields, o.threshold) {
			// An ordinary body declared over the threshold goes as it
			// arrives.
			s.startPassLocked(o.threshold)
		}
		s.passMu.Unlock()
		if framing != stream.FramingNone && !o.block.endStream {
			o.streamResponse(s, framing)
			return nil
		}
	}
	if o.block.endStream {
		o.finish(s, nil)
	}
	return nil
}

// streamResponse carries s's response as stream messages: the head goes on
// now (headed), and the body as it arrives.
func (o *h2Origin) streamResponse(s *h2OriginStream, framing stream.Framing) {
	id := s.id
	s.response.streamed = &h2StreamedBody{
		framing: framing,
		in: newH2Inbound(h2OriginStreamWindow, func(n uint32) {
			o.w.do(func(f *http2.Framer) error { return f.WriteWindowUpdate(id, n) })
		}),
		resetBy: func(code http2.ErrCode) { o.cancel(s, code) },
	}
	s.streamed.Store(s.response.streamed)
	close(s.headed)
}

// streamedData puts a DATA frame of a streamed response in its inbound. One
// past the stream's window resets the stream with FLOW_CONTROL_ERROR.
func (o *h2Origin) streamedData(s *h2OriginStream, f *http2.DataFrame) {
	in := s.response.streamed.in
	if !in.put(f.Data(), f.Header().Length) {
		code := http2.ErrCodeFlowControl
		in.close(&streamResetError{code: code})
		o.cancel(s, code)
		return
	}
	if f.StreamEnded() {
		in.finish(nil)
	}
}

// stream returns the open stream id, nil for any other.
func (o *h2Origin) stream(id uint32) *h2OriginStream {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streams[id]
}

// finish ends stream s, with err when no response will come on it.
//
// A response that ends while the rest of a passed request body is still
// being sent leaves the stream open to send it, as the origin's half of
// the stream closed, not the client's (requestSent).
func (o *h2Origin) finish(s *h2OriginStream, err error) {
	o.mu.Lock()
	if _, open := o.streams[s.id]; !open {
		o.mu.Unlock()
		return
	}
	if s.finished {
		// The response finished before; now the request's sending
		// ends too.
		delete(o.streams, s.id)
		o.mu.Unlock()
		o.flow.end(&s.window)
		return
	}
	s.finished = true
	sending := err == nil && s.reset == nil && s.sending
	if !sending {
		delete(o.streams, s.id)
	}
	o.mu.Unlock()
	if pass := s.passing(); pass != nil {
		// The response went with what arrived; how it ends goes after
		// it.
		pass.cut(s.reset, err)
	}
	s.err = err
	if !sending {
		o.flow.end(&s.window)
	}
	if body := s.streamed.Load(); body != nil {
		// The connection ended, or the stream was given up; a body
		// that already ended stays as it ended.
		if err == nil {
			err = errStreamGone
		}
		body.in.closeUnlessEnded(err)
	}
	close(s.done)
}

// requestSent notes that the rest of a passed request body was sent on s,
// or failed to be, and closes the stream when its response finished
// first.
func (o *h2Origin) requestSent(s *h2OriginStream) {
	o.mu.Lock()
	s.sending = false
	_, open := o.streams[s.id]
	closing := open && s.finished
	if closing {
		delete(o.streams, s.id)
	}
	o.mu.Unlock()
	if closing {
		o.flow.end(&s.window)
	}
}

// resetBy files the origin's RST_STREAM on s and finishes it.
func (o *h2Origin) resetBy(s *h2OriginStream, code http2.ErrCode) {
	o.mu.Lock()
	if !s.finished {
		s.reset = &code
	}
	o.mu.Unlock()
	o.finish(s, nil)
}

// end ends every open stream with err and calls onEnd.
func (o *h2Origin) end(err error) {
	o.mu.Lock()
	if o.err != nil {
		o.mu.Unlock()
		return
	}
	o.err = err
	streams := make([]*h2OriginStream, 0, len(o.streams))
	for _, s := range o.streams {
		streams = append(streams, s)
	}
	o.mu.Unlock()
	o.flow.close()
	o.conn.Close()
	for _, s := range streams {
		o.finish(s, err)
	}
	if o.onEnd != nil {
		o.onEnd()
	}
}

// close closes the origin connection.
func (o *h2Origin) close() {
	o.end(errors.New("the client connection ended"))
}

// send opens an origin stream and sends the request on it: its header
// block, its body, and its trailers, END_STREAM falling where it fell on the
// client's stream.
func (o *h2Origin) send(request *h2Request) (*h2OriginStream, error) {
	if request.rest != nil {
		// A body past the threshold: what arrived, then the rest as it
		// arrives (h2engine_threshold.go).
		request.rest.take()
		s, err := o.openStream(request.fields, false, true)
		if err != nil {
			// Nothing will forward the rest.
			close(request.rest.forwarded)
			return nil, err
		}
		go o.forwardRequestPass(s, request)
		return s, nil
	}
	hasBody := len(request.body) > 0 || request.endsOnData
	hasTrailers := len(request.trailers) > 0
	s, err := o.openStream(request.fields, !hasBody && !hasTrailers, false)
	if err != nil {
		return nil, err
	}
	go func() {
		err := sendData(o.w, o.flow, &s.window, s.id, request.body, hasBody && !hasTrailers)
		if err == nil && hasTrailers {
			_, err = o.w.headers(fixedID(s.id), request.trailers, true, o.flow.frameSize())
		}
		if err != nil && !errors.Is(err, errStreamGone) {
			o.end(err)
		}
	}()
	return s, nil
}

// sendHead opens an origin stream with a streamed request's head, leaving
// the stream open for its body (h2OriginSink).
func (o *h2Origin) sendHead(fields h2Fields) (*h2OriginStream, error) {
	return o.openStream(fields, false, false)
}

// openStream opens an origin stream with a header block. sending marks it,
// before it opens, as one whose passed request body is still to be sent,
// so a response that ends first leaves it open for the rest (finish).
func (o *h2Origin) openStream(fields h2Fields, endStream, sending bool) (*h2OriginStream, error) {
	s := newH2OriginStream()
	s.sending = sending
	pick := func() (uint32, error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		switch {
		case o.err != nil:
			return 0, o.err
		case o.gone:
			return 0, errOriginGoneAway
		}
		s.id = o.nextID
		o.nextID += 2
		o.flow.mu.Lock()
		s.window.window = o.flow.initial
		o.flow.mu.Unlock()
		o.streams[s.id] = s
		return s.id, nil
	}
	if _, err := o.w.headers(pick, fields, endStream, o.flow.frameSize()); err != nil {
		if s.id != 0 {
			o.finish(s, err)
		}
		return nil, err
	}
	return s, nil
}

// cancel resets stream s with code, as the client reset its own.
func (o *h2Origin) cancel(s *h2OriginStream, code http2.ErrCode) {
	o.mu.Lock()
	_, open := o.streams[s.id]
	o.mu.Unlock()
	if !open {
		return
	}
	if body := s.streamed.Load(); body != nil {
		body.in.close(&streamResetError{code: code})
	}
	o.w.do(func(f *http2.Framer) error { return f.WriteRSTStream(s.id, code) })
	o.finish(s, errStreamGone)
}

// open reports whether stream s is still open.
func (o *h2Origin) open(s *h2OriginStream) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, open := o.streams[s.id]
	return open
}

// relayGoAway sends the origin a GOAWAY the client sent Marasi, with the
// same code. Marasi accepts no stream from the origin, so it names none.
func (o *h2Origin) relayGoAway(code http2.ErrCode, debug []byte) {
	o.w.do(func(f *http2.Framer) error { return f.WriteGoAway(0, code, debug) })
}
