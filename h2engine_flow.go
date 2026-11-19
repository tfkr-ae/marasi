package marasi

import (
	"bufio"
	"bytes"
	"errors"
	"math"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// Each HTTP/2 leg has its own SETTINGS, flow-control windows, HPACK tables
// and stream IDs (docs/adr/2026-11-19-http2-transparency-is-end-to-end-for-messages-and-per-leg-for-frames.md).
// This file is what the two legs share: the writer, which owns the leg's
// framer and HPACK encoder, the reading of header blocks, and the send side
// of flow control.

// h2DefaultWindow is the initial flow-control window of RFC 9113 §6.9.2.
const h2DefaultWindow = 65535

// h2MaxWindow is the largest flow-control window (RFC 9113 §6.9.1).
const h2MaxWindow = math.MaxInt32

// h2MaxBlock bounds a header block the peer sends, both as read and as
// decoded (each field counted as its name, its value and 32 octets, as RFC
// 9113 §6.5.2 does). It is the HTTP/1.1 head limit, so Marasi refuses no
// header section Go's own servers take.
const h2MaxBlock = h1.MaxHeadBytes

// errH2BlockTooLarge is the connection error for a header block past
// h2MaxBlock. A block is either decoded whole or the connection ends, since
// the HPACK table cannot be kept in step otherwise.
var errH2BlockTooLarge = http2.ConnectionError(http2.ErrCodeEnhanceYourCalm)

// h2Block is a header block being read, from its HEADERS frame to the
// CONTINUATION that ends it. Each leg reads one at a time (RFC 9113 §6.10).
type h2Block struct {
	buf       []byte
	id        uint32
	endStream bool
}

// start begins the block of HEADERS frame f.
func (b *h2Block) start(f *http2.HeadersFrame) error {
	b.buf, b.id, b.endStream = b.buf[:0], f.StreamID, f.StreamEnded()
	return b.add(f.HeaderBlockFragment())
}

// add appends a fragment, refusing a block that runs past h2MaxBlock.
func (b *h2Block) add(fragment []byte) error {
	if len(b.buf)+len(fragment) > h2MaxBlock {
		return errH2BlockTooLarge
	}
	b.buf = append(b.buf, fragment...)
	return nil
}

// decode decodes the whole block, which keeps the leg's HPACK table in step
// whatever becomes of the block.
func (b *h2Block) decode(decoder *hpack.Decoder) (h2Fields, error) {
	var fields h2Fields
	size := 0
	decoder.SetEmitFunc(func(f hpack.HeaderField) {
		if size += int(f.Size()); size <= h2MaxBlock {
			fields = append(fields, h2Field{Name: f.Name, Value: f.Value, Sensitive: f.Sensitive})
		}
	})
	if _, err := decoder.Write(b.buf); err != nil {
		return nil, http2.ConnectionError(http2.ErrCodeCompression)
	}
	if err := decoder.Close(); err != nil {
		return nil, http2.ConnectionError(http2.ErrCodeCompression)
	}
	if size > h2MaxBlock {
		return nil, errH2BlockTooLarge
	}
	return fields, nil
}

// h2Writer writes one leg's frames. Every write goes through it, so the
// HPACK encoder's state follows the order header blocks go out in.
type h2Writer struct {
	mu      sync.Mutex
	bw      *bufio.Writer
	framer  *http2.Framer
	encoder *hpack.Encoder
	block   bytes.Buffer
}

func newH2Writer(bw *bufio.Writer, framer *http2.Framer) *h2Writer {
	w := &h2Writer{bw: bw, framer: framer}
	w.encoder = hpack.NewEncoder(&w.block)
	return w
}

// do runs write with the framer and flushes.
func (w *h2Writer) do(write func(*http2.Framer) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := write(w.framer); err != nil {
		return err
	}
	return w.bw.Flush()
}

// headers writes a header block on the stream id picks, split into
// CONTINUATION frames of at most maxFrame bytes. Fields go out as they are,
// whatever RFC 9113 says of them. id runs under the writer's lock, so a new
// stream's ID is picked in the order header blocks go out.
func (w *h2Writer) headers(id func() (uint32, error), fields h2Fields, endStream bool, maxFrame uint32) (uint32, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	streamID, err := id()
	if err != nil {
		return 0, err
	}
	w.block.Reset()
	for _, f := range fields {
		if err := w.encoder.WriteField(hpack.HeaderField{Name: f.Name, Value: f.Value, Sensitive: f.Sensitive}); err != nil {
			return streamID, err
		}
	}
	block := w.block.Bytes()
	first := block[:min(len(block), int(maxFrame))]
	rest := block[len(first):]
	if err := w.framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID: streamID, BlockFragment: first, EndStream: endStream, EndHeaders: len(rest) == 0,
	}); err != nil {
		return streamID, err
	}
	for len(rest) > 0 {
		chunk := rest[:min(len(rest), int(maxFrame))]
		rest = rest[len(chunk):]
		if err := w.framer.WriteContinuation(streamID, len(rest) == 0, chunk); err != nil {
			return streamID, err
		}
	}
	return streamID, w.bw.Flush()
}

// fixedID is an ID picker for a stream whose ID is known.
func fixedID(id uint32) func() (uint32, error) {
	return func() (uint32, error) { return id, nil }
}

// errStreamGone is the cause when a stream ended, reset by either side or
// with its connection, before what was being sent on it went out.
var errStreamGone = errors.New("the stream ended")

// h2Flow is the send side of one leg's flow control: the connection's
// window, the largest frame the peer takes, and the windows of its streams,
// which each stream holds. Every field, and every stream window, is guarded
// by mu, and cond wakes senders when a window opens or a stream ends.
type h2Flow struct {
	mu   sync.Mutex
	cond *sync.Cond
	// window is the connection's send window.
	window int64
	// initial is the peer's SETTINGS_INITIAL_WINDOW_SIZE, and maxFrame
	// its SETTINGS_MAX_FRAME_SIZE.
	initial  int64
	maxFrame uint32
	// closed is set once the leg's connection ended.
	closed bool
}

func newH2Flow() *h2Flow {
	f := &h2Flow{window: h2DefaultWindow, initial: h2DefaultWindow, maxFrame: 16384}
	f.cond = sync.NewCond(&f.mu)
	return f
}

// h2SendWindow is one stream's send window and whether it can still send.
type h2SendWindow struct {
	window int64
	gone   bool
}

// take waits until data may be sent on stream and returns how many bytes,
// at most want and at most a frame, it may send now, deducted from both
// windows. It fails with errStreamGone once the stream or the leg ended.
func (f *h2Flow) take(stream *h2SendWindow, want int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		if stream.gone || f.closed {
			return 0, errStreamGone
		}
		n := min(int64(want), f.window, stream.window, int64(f.maxFrame))
		if n > 0 {
			f.window -= n
			stream.window -= n
			return int(n), nil
		}
		f.cond.Wait()
	}
}

// frameSize is the largest frame the peer takes.
func (f *h2Flow) frameSize() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxFrame
}

// end marks stream ended and wakes its sender.
func (f *h2Flow) end(stream *h2SendWindow) {
	f.mu.Lock()
	stream.gone = true
	f.mu.Unlock()
	f.cond.Broadcast()
}

// close marks the leg ended and wakes every sender.
func (f *h2Flow) close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.cond.Broadcast()
}

// grow adds n to a window, the connection's when stream is nil, and reports
// false when that takes it past the largest window, a FLOW_CONTROL_ERROR
// (RFC 9113 §6.9.1).
func (f *h2Flow) grow(stream *h2SendWindow, n int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	window := &f.window
	if stream != nil {
		window = &stream.window
	}
	if *window+n > h2MaxWindow {
		return false
	}
	*window += n
	f.cond.Broadcast()
	return true
}

// settings applies the peer's SETTINGS that bear on sending: a new initial
// window changes every open stream's window by the difference (RFC 9113
// §6.9.2), and reports false when one passes the largest window. The caller
// holds the lock that guards the leg's streams, so none opens meanwhile.
func (f *h2Flow) settings(frame *http2.SettingsFrame, streams []*h2SendWindow) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := true
	frame.ForeachSetting(func(s http2.Setting) error {
		switch s.ID {
		case http2.SettingInitialWindowSize:
			delta := int64(s.Val) - f.initial
			f.initial = int64(s.Val)
			for _, w := range streams {
				w.window += delta
				if w.window > h2MaxWindow {
					ok = false
				}
			}
		case http2.SettingMaxFrameSize:
			f.maxFrame = s.Val
		}
		return nil
	})
	f.cond.Broadcast()
	return ok
}

// sendData writes data on stream id in DATA frames as the windows allow,
// the last one with END_STREAM when endStream is set. An empty body with
// endStream is one empty DATA frame.
func sendData(w *h2Writer, flow *h2Flow, stream *h2SendWindow, id uint32, data []byte, endStream bool) error {
	if len(data) == 0 {
		if !endStream {
			return nil
		}
		return w.do(func(f *http2.Framer) error { return f.WriteData(id, true, nil) })
	}
	for len(data) > 0 {
		n, err := flow.take(stream, len(data))
		if err != nil {
			return err
		}
		chunk := data[:n]
		data = data[n:]
		last := endStream && len(data) == 0
		if err := w.do(func(f *http2.Framer) error { return f.WriteData(id, last, chunk) }); err != nil {
			return err
		}
	}
	return nil
}
