package marasi

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
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

	mu      sync.Mutex
	streams map[uint32]*h2OriginStream
	nextID  uint32
	// gone is set once the origin sent GOAWAY, with the last stream it
	// processes; err once the connection ended.
	gone     bool
	goneLast uint32
	err      error
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

	// Set before done is closed, and only read after.
	response *h2Response
	final    bool
	reset    *http2.ErrCode
	err      error
}

func newH2OriginStream() *h2OriginStream {
	return &h2OriginStream{done: make(chan struct{}), moreInterim: make(chan struct{}, 1)}
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
func startH2Origin(conn *routedConn, addr string, onGoAway func(uint32, http2.ErrCode, []byte), onEnd func()) (*h2Origin, error) {
	bw := bufio.NewWriter(conn)
	br := bufio.NewReader(conn)
	framer := http2.NewFramer(bw, br)
	framer.AllowIllegalWrites = true
	o := &h2Origin{
		conn: conn, addr: addr, w: newH2Writer(bw, framer), flow: newH2Flow(),
		onGoAway: onGoAway, onEnd: onEnd,
		streams: make(map[uint32]*h2OriginStream), nextID: 1,
	}
	if _, err := io.WriteString(bw, http2.ClientPreface); err != nil {
		return nil, err
	}
	// A large window on both levels, so the origin sends a body whole
	// without waiting on Marasi, which reads bodies whole.
	err := o.w.do(func(f *http2.Framer) error {
		if err := f.WriteSettings(
			http2.Setting{ID: http2.SettingEnablePush, Val: 0},
			http2.Setting{ID: http2.SettingInitialWindowSize, Val: h2MaxWindow},
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
		if s.response == nil {
			s.response = &h2Response{}
		}
		s.response.body = append(s.response.body, f.Data()...)
		if f.StreamEnded() {
			s.response.endsOnData = true
			o.finish(s, nil)
		} else if n > 0 {
			o.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(f.StreamID, n) })
		}
	case *http2.RSTStreamFrame:
		if s := o.stream(f.StreamID); s != nil {
			code := f.ErrCode
			s.reset = &code
			o.finish(s, nil)
		}
	case *http2.GoAwayFrame:
		o.mu.Lock()
		o.gone, o.goneLast = true, f.LastStreamID
		var unprocessed []*h2OriginStream
		for id, s := range o.streams {
			if id > f.LastStreamID {
				unprocessed = append(unprocessed, s)
			}
		}
		o.mu.Unlock()
		for _, s := range unprocessed {
			o.finish(s, errOriginGoneAway)
		}
		if o.onGoAway != nil {
			o.onGoAway(f.LastStreamID, f.ErrCode, f.DebugData())
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
	case s.final:
		s.response.trailers = fields
	case len(status) == 3 && status[0] == '1' && status != "101":
		s.addInterim(fields)
	default:
		s.final = true
		if s.response == nil {
			s.response = &h2Response{}
		}
		s.response.fields = fields
	}
	if o.block.endStream {
		o.finish(s, nil)
	}
	return nil
}

// stream returns the open stream id, nil for any other.
func (o *h2Origin) stream(id uint32) *h2OriginStream {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streams[id]
}

// finish ends stream s, with err when no response will come on it.
func (o *h2Origin) finish(s *h2OriginStream, err error) {
	o.mu.Lock()
	if _, open := o.streams[s.id]; !open {
		o.mu.Unlock()
		return
	}
	delete(o.streams, s.id)
	o.mu.Unlock()
	s.err = err
	o.flow.end(&s.window)
	close(s.done)
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
	s := newH2OriginStream()
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
	hasBody := len(request.body) > 0 || request.endsOnData
	hasTrailers := len(request.trailers) > 0
	if _, err := o.w.headers(pick, request.fields, !hasBody && !hasTrailers, o.flow.frameSize()); err != nil {
		if s.id != 0 {
			o.finish(s, err)
		}
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

// cancel resets stream s with code, as the client reset its own.
func (o *h2Origin) cancel(s *h2OriginStream, code http2.ErrCode) {
	o.mu.Lock()
	_, open := o.streams[s.id]
	o.mu.Unlock()
	if !open {
		return
	}
	o.w.do(func(f *http2.Framer) error { return f.WriteRSTStream(s.id, code) })
	o.finish(s, errStreamGone)
}

// relayGoAway sends the origin a GOAWAY the client sent Marasi, with the
// same code. Marasi accepts no stream from the origin, so it names none.
func (o *h2Origin) relayGoAway(code http2.ErrCode, debug []byte) {
	o.w.do(func(f *http2.Framer) error { return f.WriteGoAway(0, code, debug) })
}
