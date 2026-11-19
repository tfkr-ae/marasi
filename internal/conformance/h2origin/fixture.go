package h2origin

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// Case is one h2 origin fixture case: the requests the harness client sends
// as streams on one connection, all at once, and the frame script the origin
// follows on each connection it accepts.
type Case struct {
	ID       string // case ID before the client approach
	RFC      string // the RFC rule the case exercises
	Requests []Request
	Script   []Step
	// Redirect, when set, makes the case a redirected one: its requests go
	// to an origin that speaks only HTTP/1.1 and answers each with these
	// bytes, framed by content-length. Directly the client sends them there
	// in HTTP/1.1; through the proxy it sends them to the h2 origin, which
	// follows Script, with RedirectField, which Setup's Lua turns into a
	// redirect. The recording's origin side is the HTTP/1.1 origin's.
	Redirect string
}

// Request is one request the client sends on its own stream. The client
// adds the pseudo-header fields :method, :scheme (https), :authority (the
// origin's host:port) and :path before Fields.
type Request struct {
	Method   string
	Path     string
	Fields   []h2.Field
	Body     string     // sent as one DATA frame when not empty
	Trailers []h2.Field // sent as a last header block when not empty
	// Fragment, when not zero, splits the header block into a HEADERS
	// frame and CONTINUATION frames of at most this many bytes.
	Fragment int
}

// StepKind names what a script step does.
type StepKind string

// Step kinds.
const (
	StepAwait    StepKind = "await"    // wait for the next request stream to end
	StepHeaders  StepKind = "headers"  // send a header block
	StepData     StepKind = "data"     // send a DATA frame
	StepReset    StepKind = "reset"    // send RST_STREAM
	StepGoAway   StepKind = "goaway"   // send GOAWAY
	StepSettings StepKind = "settings" // send SETTINGS
	StepDelay    StepKind = "delay"    // pause
	StepClose    StepKind = "close"    // close the connection
)

// Step is one step of an origin's frame script. Stream is the position of
// a request stream in the order the streams opened, not its ID.
type Step struct {
	Kind      StepKind
	Stream    int
	EndStream bool
	Fields    []h2.Field      // StepHeaders
	Fragment  int             // StepHeaders: when not zero, the most bytes of the block in one frame
	Data      string          // StepData
	Pad       uint8           // StepData: padding bytes
	Code      http2.ErrCode   // StepReset, StepGoAway
	Processed int             // StepGoAway: how many request streams the origin processed
	Settings  []http2.Setting // StepSettings
	Delay     time.Duration   // StepDelay
}

// Await waits for the next request stream, in opening order, to end.
func Await() Step { return Step{Kind: StepAwait} }

// Headers sends a header block of name, value pairs on a request stream.
func Headers(stream int, endStream bool, pairs ...string) Step {
	return Step{Kind: StepHeaders, Stream: stream, EndStream: endStream, Fields: h2.Fields(pairs...)}
}

// ContinuedHeaders sends a header block as HEADERS and CONTINUATION frames
// of at most fragment bytes each.
func ContinuedHeaders(stream int, endStream bool, fragment int, pairs ...string) Step {
	step := Headers(stream, endStream, pairs...)
	step.Fragment = fragment
	return step
}

// Data sends one DATA frame on a request stream.
func Data(stream int, endStream bool, data string) Step {
	return Step{Kind: StepData, Stream: stream, EndStream: endStream, Data: data}
}

// PaddedData sends one DATA frame with pad bytes of padding.
func PaddedData(stream int, endStream bool, data string, pad uint8) Step {
	return Step{Kind: StepData, Stream: stream, EndStream: endStream, Data: data, Pad: pad}
}

// Reset sends RST_STREAM with code on a request stream.
func Reset(stream int, code http2.ErrCode) Step {
	return Step{Kind: StepReset, Stream: stream, Code: code}
}

// GoAway sends GOAWAY with code, naming the first processed request
// streams as processed: its last stream ID is that of the processed-th
// stream, or 0.
func GoAway(processed int, code http2.ErrCode) Step {
	return Step{Kind: StepGoAway, Processed: processed, Code: code}
}

// Settings sends a SETTINGS frame.
func Settings(settings ...http2.Setting) Step {
	return Step{Kind: StepSettings, Settings: settings}
}

// Delay pauses the script.
func Delay(d time.Duration) Step { return Step{Kind: StepDelay, Delay: d} }

// Close closes the connection.
func Close() Step { return Step{Kind: StepClose} }

// origin is a running h2 origin fixture: one TLS listener offering ALPN h2
// for one case, recording what every connection it accepts received.
type origin struct {
	listener net.Listener
	script   []Step
	timeout  time.Duration // how long a handshake, the preface or Await waits

	mu          sync.Mutex
	connections []*originConn
	wg          sync.WaitGroup
}

// originConn is one accepted connection. mu guards recorder.
type originConn struct {
	conn     net.Conn
	recorder *h2.Recorder
}

// startOrigin starts an origin serving TLS with config.
func startOrigin(listen string, script []Step, timeout time.Duration, config *tls.Config) (*origin, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(listen, "0"))
	if err != nil {
		return nil, err
	}
	o := &origin{listener: tls.NewListener(listener, config), script: script, timeout: timeout}
	o.wg.Go(o.accept)
	return o, nil
}

func (o *origin) port() string {
	_, port, _ := net.SplitHostPort(o.listener.Addr().String())
	return port
}

func (o *origin) accept() {
	for {
		conn, err := o.listener.Accept()
		if err != nil {
			return
		}
		c := &originConn{conn: conn}
		o.mu.Lock()
		o.connections = append(o.connections, c)
		o.mu.Unlock()
		o.wg.Go(func() { o.serve(c) })
	}
}

// serve shakes hands, reads the preface, then runs the script while a
// reader records every frame, and reads until the connection ends.
func (o *origin) serve(c *originConn) {
	conn := c.conn.(*tls.Conn)
	_ = conn.SetDeadline(time.Now().Add(o.timeout))
	err := conn.Handshake()
	o.mu.Lock()
	c.recorder = h2.NewRecorder(conn.ConnectionState().NegotiatedProtocol)
	o.mu.Unlock()
	if err != nil {
		return
	}
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != http2.ClientPreface {
		o.mu.Lock()
		c.recorder.NoPreface()
		o.mu.Unlock()
		_ = conn.SetDeadline(time.Time{})
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	_ = conn.SetDeadline(time.Time{})

	w := &frameWriter{framer: http2.NewFramer(conn, conn)}
	w.encoder = hpack.NewEncoder(&w.block)
	if err := w.do(func(f *http2.Framer) error { return f.WriteSettings() }); err != nil {
		return
	}
	changed := make(chan struct{}, 1)
	read := make(chan struct{})
	windows := newWindows()
	go func() {
		defer close(read)
		defer windows.end()
		o.read(c, http2.NewFramer(io.Discard, conn), w, windows, changed)
	}()
	o.run(c, w, windows, changed, read)
	<-read
}

// read records every frame, acknowledges SETTINGS and PING, keeps the send
// windows, gives back the window each DATA frame took, and signals changed
// after each frame, until the connection ends.
func (o *origin) read(c *originConn, reader *http2.Framer, w *frameWriter, windows *windows, changed chan<- struct{}) {
	defer func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}()
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			return
		}
		if err := acknowledge(frame, w); err != nil {
			return
		}
		windows.received(frame)
		if err := w.do(func(f *http2.Framer) error { return replenish(f, frame) }); err != nil {
			return
		}
		o.mu.Lock()
		err = c.recorder.Frame(frame)
		o.mu.Unlock()
		if err != nil {
			return
		}
		select {
		case changed <- struct{}{}:
		default:
		}
	}
}

// run follows the script. An Await that times out, or a connection that
// ends, stops it.
func (o *origin) run(c *originConn, w *frameWriter, windows *windows, changed <-chan struct{}, read <-chan struct{}) {
	awaited := 0
	streamID := func(i int) uint32 {
		o.mu.Lock()
		defer o.mu.Unlock()
		return c.recorder.StreamID(i)
	}
	for _, step := range o.script {
		var err error
		switch step.Kind {
		case StepAwait:
			deadline := time.NewTimer(o.timeout)
			for {
				o.mu.Lock()
				closed := c.recorder.Closed(awaited)
				o.mu.Unlock()
				if closed {
					break
				}
				select {
				case <-changed:
					select {
					case <-read:
						deadline.Stop()
						return
					default:
					}
					continue
				case <-deadline.C:
					return
				}
			}
			deadline.Stop()
			awaited++
		case StepHeaders:
			err = w.headers(streamID(step.Stream), step.EndStream, step.Fields, step.Fragment)
		case StepData:
			err = w.data(streamID(step.Stream), windows, step)
		case StepReset:
			id := streamID(step.Stream)
			err = w.do(func(f *http2.Framer) error { return f.WriteRSTStream(id, step.Code) })
		case StepGoAway:
			var last uint32
			if step.Processed > 0 {
				last = streamID(step.Processed - 1)
			}
			err = w.do(func(f *http2.Framer) error { return f.WriteGoAway(last, step.Code, nil) })
		case StepSettings:
			err = w.do(func(f *http2.Framer) error { return f.WriteSettings(step.Settings...) })
		case StepDelay:
			time.Sleep(step.Delay)
		case StepClose:
			_ = c.conn.Close()
			return
		}
		if err != nil {
			return
		}
	}
}

// snapshot records every connection as it is now, with every field value
// passed through normalise.
func (o *origin) snapshot(normalise func(string) string) []h2.Connection {
	o.mu.Lock()
	defer o.mu.Unlock()
	connections := make([]h2.Connection, 0, len(o.connections))
	for _, c := range o.connections {
		if c.recorder == nil {
			// Still shaking hands: no protocol yet.
			connections = append(connections, h2.NewRecorder("").Connection(normalise))
			continue
		}
		connections = append(connections, c.recorder.Connection(normalise))
	}
	return connections
}

// stop closes the listener and every connection.
func (o *origin) stop() {
	_ = o.listener.Close()
	o.mu.Lock()
	for _, c := range o.connections {
		_ = c.conn.Close()
	}
	o.mu.Unlock()
	o.wg.Wait()
}

// frameWriter serialises writes to one connection's framer, which both the
// script and the reader's acknowledgements use, and HPACK-encodes header
// blocks with the connection's one encoder.
type frameWriter struct {
	mu      sync.Mutex
	framer  *http2.Framer
	block   bytes.Buffer
	encoder *hpack.Encoder
}

func (w *frameWriter) do(write func(*http2.Framer) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return write(w.framer)
}

// errEnded is a script step that could not finish: the connection ended
// while it waited for a flow-control window.
var errEnded = errors.New("the connection ended")

// data sends a StepData within the peer's flow-control windows: a padded
// frame whole once its window is open, other data in frames as large as
// the windows allow.
func (w *frameWriter) data(id uint32, windows *windows, step Step) error {
	data := []byte(step.Data)
	if step.Pad > 0 {
		need := len(data) + int(step.Pad) + 1
		if windows.take(id, need, need) == 0 {
			return errEnded
		}
		return w.do(func(f *http2.Framer) error {
			return f.WriteDataPadded(id, step.EndStream, data, make([]byte, step.Pad))
		})
	}
	if len(data) == 0 {
		return w.do(func(f *http2.Framer) error { return f.WriteData(id, step.EndStream, nil) })
	}
	for len(data) > 0 {
		n := windows.take(id, len(data), 0)
		if n == 0 {
			return errEnded
		}
		end := step.EndStream && n == len(data)
		chunk := data[:n]
		if err := w.do(func(f *http2.Framer) error { return f.WriteData(id, end, chunk) }); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// headers encodes fields and sends them as one HEADERS frame, or split
// into fragments of at most fragment bytes when fragment is not zero.
func (w *frameWriter) headers(id uint32, endStream bool, fields []h2.Field, fragment int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.block.Reset()
	for _, f := range fields {
		if err := w.encoder.WriteField(hpack.HeaderField{Name: f.Name, Value: f.Value}); err != nil {
			return err
		}
	}
	return writeBlock(w.framer, id, w.block.Bytes(), endStream, fragment)
}

// writeBlock sends an encoded header block as one HEADERS frame, or as
// HEADERS and CONTINUATION frames of at most fragment bytes each.
func writeBlock(framer *http2.Framer, id uint32, block []byte, endStream bool, fragment int) error {
	if fragment <= 0 || len(block) <= fragment {
		return framer.WriteHeaders(http2.HeadersFrameParam{
			StreamID: id, BlockFragment: block, EndStream: endStream, EndHeaders: true,
		})
	}
	if err := framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID: id, BlockFragment: block[:fragment], EndStream: endStream,
	}); err != nil {
		return err
	}
	for rest := block[fragment:]; len(rest) > 0; {
		n := min(fragment, len(rest))
		if err := framer.WriteContinuation(id, n == len(rest), rest[:n]); err != nil {
			return err
		}
		rest = rest[n:]
	}
	return nil
}

// acknowledge answers SETTINGS and PING, as every HTTP/2 endpoint must.
func acknowledge(frame http2.Frame, w *frameWriter) error {
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if !f.IsAck() {
			return w.do(func(f *http2.Framer) error { return f.WriteSettingsAck() })
		}
	case *http2.PingFrame:
		if !f.IsAck() {
			data := f.Data
			return w.do(func(f *http2.Framer) error { return f.WritePing(true, data) })
		}
	}
	return nil
}
