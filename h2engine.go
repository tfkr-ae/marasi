package marasi

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// Marasi's HTTP/2 engine (docs/adr/2026-11-19-marasi-owns-its-http2-framing.md):
// the client leg, served on a client connection whose TLS handshake
// negotiated h2, and its origin leg (h2engine_origin.go), the client
// connection's one origin connection. Each client stream is carried on its
// own goroutine through the request step, the round trip on its own origin
// stream, and the response step (h2engine_stream.go), so a Checkpoint hold
// blocks only its own stream. Frames belong to their leg: SETTINGS, flow
// control (h2engine_flow.go), HPACK state and stream IDs are each leg's own.
// Messages are carried end to end as ordered field lists (h2engine_fields.go).
//
// Files:
//   - h2engine.go: the client leg's connection, frames and stream states;
//   - h2engine_stream.go: one stream's pair through the base pipeline;
//   - h2engine_origin.go: the origin leg;
//   - h2engine_flow.go: what both legs share: the writer and flow control;
//   - h2engine_fields.go: field lists, their views and the stored text form.

// h2LingerTimeout bounds how long a client connection that is ending is read
// for the client's own close, so closing never resets it under unread bytes.
const h2LingerTimeout = time.Second

// h2Conn is a client connection that speaks HTTP/2.
type h2Conn struct {
	c       *h1Conn
	engine  *h1Engine
	conn    net.Conn
	br      *bufio.Reader
	framer  *http2.Framer
	decoder *hpack.Decoder
	w       *h2Writer
	flow    *h2Flow

	// origin is the origin leg, nil when it could not be opened; originErr
	// is then why, and each request is answered with it. originAddr is the
	// address the handshake dialled.
	origin     *h2Origin
	originErr  error
	originAddr string
	originDial *handshakeDial

	// The header block being read, until its END_HEADERS.
	block         []byte
	blockID       uint32
	blockEnd      bool
	blockSelfDeps bool
	sawSettings   bool

	finishOnce sync.Once
	streamsWG  sync.WaitGroup

	mu sync.Mutex
	// streams are the client's open streams, by ID; closed are the
	// streams that were open once and are no longer.
	streams map[uint32]*h2Stream
	closed  map[uint32]bool
	// maxID is the highest stream ID the client used.
	maxID uint32
	// maxStreams is the SETTINGS_MAX_CONCURRENT_STREAMS the client was
	// told: the origin's, when it sent one.
	maxStreams    uint32
	maxStreamsSet bool
	// goingAway is set once Marasi sent the client GOAWAY, naming
	// goAwayLast: streams above it are ignored.
	goingAway  bool
	goAwayLast uint32
	// clientGone is set once the client sent GOAWAY, originEnded once the
	// origin leg ended, and ended once the client connection ended.
	clientGone  bool
	originEnded bool
	ended       bool
	// sent maps each client stream sent to the origin to its origin stream
	// ID, so an origin GOAWAY can be told to the client in its own IDs.
	sent map[uint32]uint32
	// unsent are the streams dispatched and not yet sent to the origin, in
	// the order the client opened them (waitTurn).
	unsent []*h2Stream
}

// h2Stream is one client stream.
type h2Stream struct {
	id     uint32
	window h2SendWindow
	ctx    context.Context
	cancel context.CancelFunc

	// Guarded by h2Conn.mu.
	request h2Request
	// ended is set once the client ended its side of the stream.
	ended bool
	// gone is set once the stream closed or was reset.
	gone   bool
	origin *h2OriginStream
	// turn is closed once the stream was sent to the origin or ended, so
	// the streams the client opened after it may go.
	turn     chan struct{}
	turnOnce sync.Once
}

// h2OrderWait bounds how long a stream waits for the streams the client
// opened before it to go to the origin first. A stream that is held, by
// Checkpoint or a slow hook, lets the others pass once it runs out.
const h2OrderWait = 250 * time.Millisecond

// waitTurn waits until every stream the client opened before st was sent
// to the origin or ended, or h2OrderWait passed, so origin streams open in
// the client's order as they would going direct.
func (h *h2Conn) waitTurn(st *h2Stream) {
	h.mu.Lock()
	var before []chan struct{}
	for _, other := range h.unsent {
		if other == st {
			break
		}
		before = append(before, other.turn)
	}
	h.mu.Unlock()
	timeout := time.NewTimer(h2OrderWait)
	defer timeout.Stop()
	for _, turn := range before {
		select {
		case <-turn:
		case <-timeout.C:
			return
		case <-st.ctx.Done():
			return
		}
	}
}

// passTurn lets the streams opened after st go to the origin.
func (h *h2Conn) passTurn(st *h2Stream) {
	st.turnOnce.Do(func() {
		h.mu.Lock()
		for i, other := range h.unsent {
			if other == st {
				h.unsent = append(h.unsent[:i], h.unsent[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
		close(st.turn)
	})
}

// serveH2 serves the client connection as HTTP/2 until it ends. The engine
// tracks it for route swaps and shutdown.
func (c *h1Conn) serveH2() {
	h := &h2Conn{
		c: c, engine: c.engine, conn: c.conn, br: c.br,
		flow:    newH2Flow(),
		decoder: hpack.NewDecoder(4096, nil),
		streams: make(map[uint32]*h2Stream), closed: make(map[uint32]bool), sent: make(map[uint32]uint32),
	}
	h.framer = http2.NewFramer(c.bw, c.br)
	h.framer.AllowIllegalWrites = true
	h.w = newH2Writer(c.bw, h.framer)
	if dialled := c.handshake; dialled != nil {
		h.originAddr, h.originErr, h.originDial = dialled.addr, dialled.err, dialled
	}
	h.openOrigin()
	if !c.engine.trackH2(h) {
		if h.origin != nil {
			h.origin.close()
		}
		return
	}
	defer c.engine.forgetH2(h)
	h.serve()
}

// serve sends the client SETTINGS and reads the client's frames until the
// connection ends.
func (h *h2Conn) serve() {
	defer h.close()
	var settings []http2.Setting
	if h.maxStreamsSet {
		settings = append(settings, http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: h.maxStreams})
	}
	if err := h.w.do(func(f *http2.Framer) error { return f.WriteSettings(settings...) }); err != nil {
		return
	}
	h.conn.SetReadDeadline(time.Now().Add(h.engine.headerTimeout))
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(h.br, preface); err != nil {
		return
	}
	if string(preface) != http2.ClientPreface {
		h.fail(http2.ErrCodeProtocol)
		return
	}
	h.conn.SetReadDeadline(time.Time{})
	for {
		frame, err := h.framer.ReadFrame()
		if err == nil {
			err = h.frame(frame)
		}
		if err != nil && !h.handle(err) {
			return
		}
	}
}

// openOrigin opens the origin leg on the connection the handshake dialled.
func (h *h2Conn) openOrigin() {
	if h.originDial == nil || h.originDial.conn == nil {
		if h.originErr == nil {
			h.originErr = errors.New("no origin connection was dialled for the client's handshake")
		}
		return
	}
	origin, err := startH2Origin(h.originDial.conn, h.originAddr, h.originGoAway, h.originGone)
	if err != nil {
		h.originDial.conn.Close()
		h.originErr = err
		return
	}
	h.origin = origin
	origin.mu.Lock()
	h.maxStreams, h.maxStreamsSet = origin.maxStreams, origin.maxStreamsSet
	origin.mu.Unlock()
}

// handle deals with an error reading or handling a frame. A stream error
// resets the stream; a connection error sends GOAWAY and ends the
// connection. It reports whether the connection goes on.
func (h *h2Conn) handle(err error) bool {
	var streamErr http2.StreamError
	var connErr http2.ConnectionError
	switch {
	case errors.As(err, &streamErr):
		h.resetStream(streamErr.StreamID, streamErr.Code)
		return true
	case errors.As(err, &connErr):
		h.fail(http2.ErrCode(connErr))
	case errors.Is(err, http2.ErrFrameTooLarge):
		h.fail(http2.ErrCodeFrameSize)
	}
	return false
}

// fail ends the connection with a connection error: GOAWAY with code, then
// the client's own close, for a moment.
func (h *h2Conn) fail(code http2.ErrCode) {
	h.mu.Lock()
	h.goingAway, h.goAwayLast = true, h.maxID
	last := h.maxID
	h.mu.Unlock()
	h.w.do(func(f *http2.Framer) error { return f.WriteGoAway(last, code, nil) })
	h.finish()
	io.Copy(io.Discard, h.br)
}

// frame handles one frame from the client (RFC 9113 §5.1, §6).
func (h *h2Conn) frame(frame http2.Frame) error {
	if _, ok := frame.(*http2.SettingsFrame); !ok && !h.sawSettings {
		// The client's preface ends with SETTINGS (RFC 9113 §3.4).
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		return h.settings(f)
	case *http2.PingFrame:
		if !f.IsAck() {
			return h.w.do(func(fr *http2.Framer) error { return fr.WritePing(true, f.Data) })
		}
	case *http2.WindowUpdateFrame:
		return h.windowUpdate(f)
	case *http2.RSTStreamFrame:
		return h.rstStream(f)
	case *http2.PriorityFrame:
		if f.StreamDep == f.StreamID {
			return http2.StreamError{StreamID: f.StreamID, Code: http2.ErrCodeProtocol}
		}
	case *http2.HeadersFrame:
		if f.StreamID%2 == 0 {
			return http2.ConnectionError(http2.ErrCodeProtocol)
		}
		h.block = append(h.block[:0], f.HeaderBlockFragment()...)
		h.blockID, h.blockEnd = f.StreamID, f.StreamEnded()
		h.blockSelfDeps = f.HasPriority() && f.Priority.StreamDep == f.StreamID
		if f.HeadersEnded() {
			return h.headerBlock()
		}
	case *http2.ContinuationFrame:
		h.block = append(h.block, f.HeaderBlockFragment()...)
		if f.HeadersEnded() {
			return h.headerBlock()
		}
	case *http2.DataFrame:
		return h.data(f)
	case *http2.GoAwayFrame:
		h.clientGoAway(f)
	case *http2.PushPromiseFrame:
		// A client never pushes (RFC 9113 §8.4).
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	return nil
}

// settings applies the client's SETTINGS and acknowledges them.
func (h *h2Conn) settings(f *http2.SettingsFrame) error {
	if f.IsAck() {
		return nil
	}
	if err := f.ForeachSetting(func(s http2.Setting) error { return s.Valid() }); err != nil {
		return err
	}
	h.sawSettings = true
	if v, ok := f.Value(http2.SettingHeaderTableSize); ok {
		h.w.mu.Lock()
		h.w.encoder.SetMaxDynamicTableSizeLimit(v)
		h.w.mu.Unlock()
	}
	h.mu.Lock()
	windows := make([]*h2SendWindow, 0, len(h.streams))
	for _, st := range h.streams {
		windows = append(windows, &st.window)
	}
	ok := h.flow.settings(f, windows)
	h.mu.Unlock()
	if !ok {
		return http2.ConnectionError(http2.ErrCodeFlowControl)
	}
	return h.w.do(func(fr *http2.Framer) error { return fr.WriteSettingsAck() })
}

// windowUpdate opens a send window.
func (h *h2Conn) windowUpdate(f *http2.WindowUpdateFrame) error {
	if f.StreamID == 0 {
		if !h.flow.grow(nil, int64(f.Increment)) {
			return http2.ConnectionError(http2.ErrCodeFlowControl)
		}
		return nil
	}
	h.mu.Lock()
	st, idle := h.streams[f.StreamID], f.StreamID > h.maxID
	h.mu.Unlock()
	switch {
	case st != nil:
		if !h.flow.grow(&st.window, int64(f.Increment)) {
			return http2.StreamError{StreamID: f.StreamID, Code: http2.ErrCodeFlowControl}
		}
	case idle:
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	return nil
}

// rstStream ends a stream the client reset, and resets its origin stream
// with the same code.
func (h *h2Conn) rstStream(f *http2.RSTStreamFrame) error {
	h.mu.Lock()
	st, idle := h.streams[f.StreamID], f.StreamID > h.maxID
	h.mu.Unlock()
	switch {
	case st != nil:
		code := f.ErrCode
		h.closeStream(st, &code)
	case idle:
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	return nil
}

// headerBlock handles a whole header block from the client: a new stream's
// request, or an open stream's trailers. It is decoded first whatever
// becomes of it, to keep the HPACK table in step. Its fields are not
// checked: a malformed request goes to the origin as it came.
func (h *h2Conn) headerBlock() error {
	decoded, err := h.decoder.DecodeFull(h.block)
	if err != nil {
		return http2.ConnectionError(http2.ErrCodeCompression)
	}
	fields := make(h2Fields, len(decoded))
	for i, f := range decoded {
		fields[i] = h2Field{Name: f.Name, Value: f.Value, Sensitive: f.Sensitive}
	}
	id, end := h.blockID, h.blockEnd
	h.mu.Lock()
	if st := h.streams[id]; st != nil {
		switch {
		case st.ended:
			h.mu.Unlock()
			return http2.StreamError{StreamID: id, Code: http2.ErrCodeStreamClosed}
		case !end:
			// Trailers end the stream (RFC 9113 §8.1).
			h.mu.Unlock()
			return http2.StreamError{StreamID: id, Code: http2.ErrCodeProtocol}
		}
		st.request.trailers, st.ended = fields, true
		h.mu.Unlock()
		h.dispatch(st)
		return nil
	}
	if id <= h.maxID {
		closed := h.closed[id]
		h.mu.Unlock()
		// HEADERS on a closed stream (RFC 9113 §5.1), or on one the
		// client skipped, which is closed too (§5.1.1).
		if closed {
			return http2.ConnectionError(http2.ErrCodeStreamClosed)
		}
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	h.maxID = id
	switch {
	case h.goingAway && id > h.goAwayLast, h.clientGone:
		h.mu.Unlock()
		return nil
	case h.blockSelfDeps:
		h.closed[id] = true
		h.mu.Unlock()
		return http2.StreamError{StreamID: id, Code: http2.ErrCodeProtocol}
	case h.maxStreamsSet && uint32(len(h.streams)) >= h.maxStreams:
		h.closed[id] = true
		h.mu.Unlock()
		return http2.StreamError{StreamID: id, Code: http2.ErrCodeRefusedStream}
	}
	ctx, cancel := context.WithCancel(h.c.ctx)
	st := &h2Stream{id: id, ctx: ctx, cancel: cancel, request: h2Request{fields: fields}, ended: end}
	h.flow.mu.Lock()
	st.window.window = h.flow.initial
	h.flow.mu.Unlock()
	h.streams[id] = st
	h.mu.Unlock()
	if end {
		h.dispatch(st)
	}
	return nil
}

// data adds a DATA frame's payload to its stream's request body. Every
// window is opened again at once: bodies are read whole.
func (h *h2Conn) data(f *http2.DataFrame) error {
	id, n := f.StreamID, f.Header().Length
	if n > 0 {
		h.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(0, n) })
	}
	h.mu.Lock()
	st := h.streams[id]
	switch {
	case h.goingAway && id > h.goAwayLast:
		h.mu.Unlock()
		return nil
	case st == nil:
		idle := id > h.maxID
		h.mu.Unlock()
		if idle {
			return http2.ConnectionError(http2.ErrCodeProtocol)
		}
		return http2.StreamError{StreamID: id, Code: http2.ErrCodeStreamClosed}
	case st.ended:
		h.mu.Unlock()
		return http2.StreamError{StreamID: id, Code: http2.ErrCodeStreamClosed}
	}
	st.request.body = append(st.request.body, f.Data()...)
	if f.StreamEnded() {
		st.ended, st.request.endsOnData = true, true
	}
	ended := st.ended
	h.mu.Unlock()
	if ended {
		h.dispatch(st)
	} else if n > 0 {
		h.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(id, n) })
	}
	return nil
}

// clientGoAway handles the client's GOAWAY: it opens no more streams, the
// origin is sent the same, and the connection ends once its streams are
// done.
func (h *h2Conn) clientGoAway(f *http2.GoAwayFrame) {
	h.mu.Lock()
	h.clientGone = true
	h.mu.Unlock()
	if h.origin != nil {
		h.origin.relayGoAway(f.ErrCode, f.DebugData())
	}
	h.finishIfDone()
}

// dispatch carries a stream whose request is whole on its own goroutine.
func (h *h2Conn) dispatch(st *h2Stream) {
	h.mu.Lock()
	st.turn = make(chan struct{})
	h.unsent = append(h.unsent, st)
	h.mu.Unlock()
	h.streamsWG.Add(1)
	go func() {
		defer h.streamsWG.Done()
		h.carry(st)
		h.closeStream(st, nil)
	}()
}

// resetStream sends RST_STREAM with code and ends the stream, resetting its
// origin stream with CANCEL.
func (h *h2Conn) resetStream(id uint32, code http2.ErrCode) {
	h.w.do(func(f *http2.Framer) error { return f.WriteRSTStream(id, code) })
	h.mu.Lock()
	st := h.streams[id]
	h.mu.Unlock()
	if st != nil {
		cancel := http2.ErrCodeCancel
		h.closeStream(st, &cancel)
	}
}

// closeStream ends st. When originCode is set and its origin stream is
// open, that is reset with the code.
func (h *h2Conn) closeStream(st *h2Stream, originCode *http2.ErrCode) {
	h.mu.Lock()
	if st.gone {
		h.mu.Unlock()
		return
	}
	st.gone = true
	delete(h.streams, st.id)
	h.closed[st.id] = true
	os := st.origin
	h.mu.Unlock()
	st.cancel()
	if st.turn != nil {
		h.passTurn(st)
	}
	h.flow.end(&st.window)
	if os != nil && originCode != nil && h.origin != nil {
		h.origin.cancel(os, *originCode)
	}
	h.finishIfDone()
}

// gone reports whether st closed or was reset.
func (h *h2Conn) gone(st *h2Stream) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return st.gone
}

// goAway sends the client a graceful GOAWAY naming the last stream it
// opened: streams up to it finish, then the connection ends. A route swap
// and shutdown end a connection so.
func (h *h2Conn) goAway() {
	h.mu.Lock()
	if h.goingAway || h.ended {
		h.mu.Unlock()
		return
	}
	h.goingAway, h.goAwayLast = true, h.maxID
	last := h.maxID
	h.mu.Unlock()
	h.w.do(func(f *http2.Framer) error { return f.WriteGoAway(last, http2.ErrCodeNo, nil) })
	h.finishIfDone()
}

// originGoAway tells the client the origin's GOAWAY, with the origin's last
// stream ID turned into the client's: the highest client stream sent on an
// origin stream at or below it. Client streams above that are left
// unanswered, as the origin leaves its own.
func (h *h2Conn) originGoAway(last uint32, code http2.ErrCode, debug []byte) {
	h.mu.Lock()
	var clientLast uint32
	for clientID, originID := range h.sent {
		if originID <= last {
			clientLast = max(clientLast, clientID)
		}
	}
	if h.goingAway {
		clientLast = min(clientLast, h.goAwayLast)
	}
	h.goingAway, h.goAwayLast = true, clientLast
	var abandoned []*h2Stream
	for id, st := range h.streams {
		if id > clientLast {
			abandoned = append(abandoned, st)
		}
	}
	h.mu.Unlock()
	h.w.do(func(f *http2.Framer) error { return f.WriteGoAway(clientLast, code, debug) })
	for _, st := range abandoned {
		h.closeStream(st, nil)
	}
	h.finishIfDone()
}

// originGone notes that the origin leg ended: the client connection ends
// once its streams are done, as a direct client's would end with the
// origin's.
func (h *h2Conn) originGone() {
	h.mu.Lock()
	h.originEnded = true
	h.mu.Unlock()
	h.finishIfDone()
}

// finishIfDone ends the connection once it is going away and none of its
// streams is still open.
func (h *h2Conn) finishIfDone() {
	h.mu.Lock()
	done := (h.goingAway || h.clientGone || h.originEnded) && len(h.streams) == 0 && !h.ended
	h.mu.Unlock()
	if done {
		h.finish()
	}
}

// finish ends the client connection gracefully: Marasi stops writing and
// reads until the client closes too, for a moment at most.
func (h *h2Conn) finish() {
	h.finishOnce.Do(func() {
		if tlsConn, ok := h.conn.(*tls.Conn); ok {
			h.w.mu.Lock()
			tlsConn.CloseWrite()
			h.w.mu.Unlock()
		}
		h.conn.SetReadDeadline(time.Now().Add(h2LingerTimeout))
	})
}

// close ends every stream and the origin leg once the client connection
// ended, and waits for the streams' goroutines.
func (h *h2Conn) close() {
	h.mu.Lock()
	h.ended = true
	streams := make([]*h2Stream, 0, len(h.streams))
	for _, st := range h.streams {
		streams = append(streams, st)
	}
	h.mu.Unlock()
	for _, st := range streams {
		h.closeStream(st, nil)
	}
	h.flow.close()
	if h.origin != nil {
		h.origin.close()
	}
	h.streamsWG.Wait()
}

// h2NoHijack is the client of an HTTP/2 pair: a stream has no connection
// of its own to take over.
type h2NoHijack struct{}

var errH2NoHijack = errors.New("an HTTP/2 stream's connection cannot be taken over")

func (h2NoHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, errH2NoHijack }

// trackH2 registers h for route swaps and shutdown. It reports false when
// the engine is closing.
func (engine *h1Engine) trackH2(h *h2Conn) bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closing.Load() {
		return false
	}
	if engine.h2conns == nil {
		engine.h2conns = make(map[*h2Conn]bool)
	}
	engine.h2conns[h] = true
	return true
}

func (engine *h1Engine) forgetH2(h *h2Conn) {
	engine.mu.Lock()
	delete(engine.h2conns, h)
	engine.mu.Unlock()
}

// h2Swapped sends a graceful GOAWAY to each HTTP/2 client connection whose
// origin connection is on a route other than route. Its in-flight streams
// finish on the old route; its next connection dials on the new one. The
// caller holds engine.mu.
func (engine *h1Engine) h2Swapped(route *Route) {
	for h := range engine.h2conns {
		if h.origin != nil && h.origin.conn.route != route {
			go h.goAway()
		}
	}
}

// h2Close sends every HTTP/2 client connection a graceful GOAWAY. The
// caller holds engine.mu.
func (engine *h1Engine) h2Close() {
	for h := range engine.h2conns {
		go h.goAway()
	}
}
