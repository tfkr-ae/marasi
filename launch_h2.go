package marasi

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// A launch or Armory request whose request line says HTTP/2 is sent over
// HTTP/2 (docs/adr/2026-11-19-marasi-gives-the-client-the-origins-protocol.md):
// through a CONNECT to the proxy listener with the proxy's credentials, then
// TLS offering h2 alone, or prior knowledge in a plain tunnel. Its fields go
// out in the order and case typed, with nothing recalculated
// (docs/adr/2026-11-19-marasi-owns-its-http2-framing.md). There is no
// fallback to HTTP/1.1: the proxy answers a launch whose origin will not
// speak HTTP/2 with a 502 proxy error response.

// isH2Text reports whether raw's request line names HTTP/2.
func isH2Text(raw string) bool {
	line, _, _ := strings.Cut(raw, "\n")
	return strings.HasSuffix(strings.TrimSuffix(line, "\r"), " HTTP/2")
}

// readH2RawRequest reads raw in the HTTP/2 text form. A :scheme field is
// added after the pseudo-header fields when the text has none; everything
// else is kept as typed.
func readH2RawRequest(raw string, scheme string) (*rawRequest, error) {
	text, err := readH2RequestText([]byte(raw), nil)
	if err != nil {
		return nil, fmt.Errorf("reading http request: %w", err)
	}
	fields := text.fields
	if method, _ := fields.get(":method"); method == http.MethodConnect {
		return nil, errors.New("reading http request: a CONNECT cannot be sent raw")
	}
	if _, ok := fields.get(":scheme"); !ok {
		fields = fields.set(":scheme", scheme)
	}
	message := &h2Request{fields: fields, body: text.body, endsOnData: len(text.body) > 0}
	view := message.request()
	if view.Host == "" {
		return nil, errors.New("host header not found or is empty")
	}
	view.URL.Scheme, view.URL.Host = scheme, view.Host
	if len(text.body) > 0 {
		view.Body = io.NopCloser(bytes.NewReader(text.body))
	}
	return &rawRequest{h2: message, view: view, scheme: scheme}, nil
}

// errNoH2 is the cause when the proxy listener did not give a launch HTTP/2.
var errNoH2 = errors.New("the proxy listener did not negotiate h2")

// sendH2Raw sends r on stream 1 of a new HTTP/2 connection over stream,
// with the internal fields appended in lowercase, and reads the response to
// its end.
func sendH2Raw(stream net.Conn, r *rawRequest, internal [][2]string) (*rawResponse, error) {
	fields := slices.Clone(r.h2.fields)
	for _, field := range internal {
		fields = append(fields, h2Field{Name: strings.ToLower(field[0]), Value: field[1]})
	}
	client := newH2LaunchClient(stream)
	if err := client.start(); err != nil {
		return nil, fmt.Errorf("starting HTTP/2: %w", err)
	}
	go client.read()
	if err := client.send(fields, r.h2.body); err != nil {
		return nil, fmt.Errorf("writing the request: %w", err)
	}
	status, err := client.response()
	if err != nil {
		return nil, fmt.Errorf("reading the response: %w", err)
	}
	return &rawResponse{statusCode: status}, nil
}

// h2LaunchStream is the stream a launch sends on.
const h2LaunchStream = 1

// h2LaunchClient is the client end of a launch's HTTP/2 connection: one
// stream, its flow control, and the frames read until the response ends.
type h2LaunchClient struct {
	conn   net.Conn
	w      *h2Writer
	framer *http2.Framer
	flow   *h2Flow
	window h2SendWindow

	mu     sync.Mutex
	status int
	done   chan struct{}
	err    error
}

func newH2LaunchClient(conn net.Conn) *h2LaunchClient {
	bw := bufio.NewWriter(conn)
	framer := http2.NewFramer(bw, bufio.NewReader(conn))
	framer.AllowIllegalWrites = true
	framer.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	framer.MaxHeaderListSize = 1 << 20
	c := &h2LaunchClient{conn: conn, w: newH2Writer(bw, framer), framer: framer, flow: newH2Flow(), done: make(chan struct{})}
	c.window.window = h2DefaultWindow
	return c
}

// start writes the connection preface and SETTINGS: no push, and a
// receive window wide enough that the response is never held up.
func (c *h2LaunchClient) start() error {
	return c.w.do(func(f *http2.Framer) error {
		if _, err := io.WriteString(c.conn, http2.ClientPreface); err != nil {
			return err
		}
		if err := f.WriteSettings(
			http2.Setting{ID: http2.SettingEnablePush, Val: 0},
			http2.Setting{ID: http2.SettingInitialWindowSize, Val: h2MaxWindow},
		); err != nil {
			return err
		}
		return f.WriteWindowUpdate(0, h2MaxWindow-h2DefaultWindow)
	})
}

// send writes the header block and the body. A request with no body ends on
// its HEADERS frame.
func (c *h2LaunchClient) send(fields h2Fields, body []byte) error {
	if _, err := c.w.headers(fixedID(h2LaunchStream), fields, len(body) == 0, c.flow.frameSize()); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return sendData(c.w, c.flow, &c.window, h2LaunchStream, body, true)
}

// response waits for the stream to end and returns the final status.
func (c *h2LaunchClient) response() (int, error) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status, c.err
}

// finish ends the stream with err, nil when the response ended.
func (c *h2LaunchClient) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return
	default:
	}
	c.err = err
	close(c.done)
	c.flow.close()
}

// read handles frames until the response ends or the connection fails.
func (c *h2LaunchClient) read() {
	for {
		frame, err := c.framer.ReadFrame()
		if err != nil {
			c.finish(err)
			return
		}
		if ended, err := c.frame(frame); ended || err != nil {
			c.finish(err)
			return
		}
	}
}

// frame handles one frame and reports whether the response ended.
func (c *h2LaunchClient) frame(frame http2.Frame) (bool, error) {
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if f.IsAck() {
			return false, nil
		}
		if !c.flow.settings(f, []*h2SendWindow{&c.window}) {
			return false, errors.New("the proxy's SETTINGS overflowed the send window")
		}
		return false, c.w.do(func(fr *http2.Framer) error { return fr.WriteSettingsAck() })
	case *http2.PingFrame:
		if f.IsAck() {
			return false, nil
		}
		return false, c.w.do(func(fr *http2.Framer) error { return fr.WritePing(true, f.Data) })
	case *http2.WindowUpdateFrame:
		var window *h2SendWindow
		if f.StreamID == h2LaunchStream {
			window = &c.window
		}
		if f.StreamID == 0 || window != nil {
			c.flow.grow(window, int64(f.Increment))
		}
		return false, nil
	case *http2.MetaHeadersFrame:
		if f.StreamID != h2LaunchStream {
			return false, nil
		}
		if status, err := strconv.Atoi(f.PseudoValue("status")); err == nil && (status >= 200 || status == http.StatusSwitchingProtocols) {
			c.mu.Lock()
			if c.status == 0 {
				c.status = status
			}
			c.mu.Unlock()
		}
		return f.StreamEnded(), nil
	case *http2.DataFrame:
		if f.StreamID != h2LaunchStream {
			return false, nil
		}
		if n := f.Length; n > 0 {
			c.w.do(func(fr *http2.Framer) error { return fr.WriteWindowUpdate(0, n) })
		}
		return f.StreamEnded(), nil
	case *http2.RSTStreamFrame:
		if f.StreamID == h2LaunchStream {
			return true, fmt.Errorf("the stream was reset: %v", f.ErrCode)
		}
	case *http2.GoAwayFrame:
		if f.LastStreamID < h2LaunchStream {
			return true, fmt.Errorf("the proxy refused the stream: GOAWAY %v", f.ErrCode)
		}
		if f.ErrCode != http2.ErrCodeNo {
			return true, fmt.Errorf("the connection ended: GOAWAY %v", f.ErrCode)
		}
	}
	return false, nil
}
