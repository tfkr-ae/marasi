package streamorigin

import (
	"bytes"
	"io"
	"net"
	"strings"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// h2Conn is one side of an HTTP/2 connection carrying one stream: the
// client's stream 1, or the first stream the origin's peer opens. Its
// reader records what arrives on that stream. Bodies are small, so it
// sends within the initial flow-control windows and gives back at once
// every window the peer's DATA takes.
type h2Conn struct {
	side string
	t    *transcript
	conn net.Conn

	mu      sync.Mutex // guards writes, the encoder and stream
	framer  *http2.Framer
	block   bytes.Buffer
	encoder *hpack.Encoder
	stream  uint32
}

// newH2Client sends the client connection preface and SETTINGS, and opens
// stream 1.
func newH2Client(t *transcript, conn net.Conn) (*h2Conn, error) {
	c := newH2Conn(SideClient, t, conn)
	c.stream = 1
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		return nil, err
	}
	return c, c.framer.WriteSettings(http2.Setting{ID: http2.SettingEnablePush, Val: 0})
}

// newH2Origin reads the client connection preface and sends SETTINGS.
// It reports false when the peer did not send the preface.
func newH2Origin(t *transcript, conn net.Conn) (*h2Conn, bool) {
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != http2.ClientPreface {
		return nil, false
	}
	c := newH2Conn(SideOrigin, t, conn)
	return c, c.framer.WriteSettings() == nil
}

func newH2Conn(side string, t *transcript, conn net.Conn) *h2Conn {
	c := &h2Conn{side: side, t: t, conn: conn, framer: http2.NewFramer(conn, conn)}
	c.framer.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	c.encoder = hpack.NewEncoder(&c.block)
	return c
}

// read records the stream's arrivals until the connection ends. onHead is
// called once, after the stream's first head is recorded, with the stream
// open for the origin's sends.
func (c *h2Conn) read(onHead func()) {
	var split splitter
	heads, ended := 0, false
	end := func() {
		for _, a := range split.end(c.side) {
			c.t.add(a)
		}
		ended = true
		c.t.add(Arrival{Side: c.side, Kind: KindEnd})
	}
	for {
		frame, err := c.framer.ReadFrame()
		if err != nil {
			if heads > 0 && !ended {
				for _, a := range split.end(c.side) {
					c.t.add(a)
				}
				c.t.add(Arrival{Side: c.side, Kind: KindClosed})
			}
			return
		}
		if c.answer(frame) != nil {
			return
		}
		c.mu.Lock()
		if c.stream == 0 && frame.Header().StreamID%2 == 1 {
			c.stream = frame.Header().StreamID
		}
		mine := frame.Header().StreamID == c.stream && c.stream != 0
		c.mu.Unlock()
		if !mine || ended {
			continue
		}
		switch f := frame.(type) {
		case *http2.MetaHeadersFrame:
			fields := make([]h2.Field, len(f.Fields))
			contentType := ""
			for i, field := range f.Fields {
				fields[i] = h2.Field{Name: field.Name, Value: field.Value}
				if field.Name == "content-type" {
					contentType = field.Value
				}
			}
			isTrailers := heads > 0 && !interim(f.PseudoValue("status"))
			if isTrailers {
				c.t.add(Arrival{Side: c.side, Kind: KindTrailers, Fields: fields})
			} else {
				c.t.add(Arrival{Side: c.side, Kind: KindHead, Fields: fields})
				split.head(contentType, fields)
			}
			if !interim(f.PseudoValue("status")) {
				heads++
			}
			if heads == 1 && !isTrailers && onHead != nil {
				onHead()
				onHead = nil
			}
			if f.StreamEnded() {
				end()
			}
		case *http2.DataFrame:
			for _, m := range split.write(f.Data()) {
				c.t.add(Arrival{Side: c.side, Kind: KindMessage, Data: m})
			}
			if f.StreamEnded() {
				end()
			}
		case *http2.RSTStreamFrame:
			for _, a := range split.end(c.side) {
				c.t.add(a)
			}
			ended = true
			c.t.add(Arrival{Side: c.side, Kind: KindReset, Code: f.ErrCode.String()})
		}
	}
}

// interim reports whether a :status is a 1xx interim response. A request
// head has no :status.
func interim(status string) bool { return strings.HasPrefix(status, "1") }

// answer acknowledges SETTINGS and PING, and gives back the window a DATA
// frame took.
func (c *h2Conn) answer(frame http2.Frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if !f.IsAck() {
			return c.framer.WriteSettingsAck()
		}
	case *http2.PingFrame:
		if !f.IsAck() {
			return c.framer.WritePing(true, f.Data)
		}
	case *http2.DataFrame:
		if f.Length == 0 {
			return nil
		}
		if err := c.framer.WriteWindowUpdate(0, f.Length); err != nil {
			return err
		}
		if !f.StreamEnded() {
			return c.framer.WriteWindowUpdate(f.StreamID, f.Length)
		}
	}
	return nil
}

func (c *h2Conn) head(fields []h2.Field, end bool) error { return c.headers(fields, end) }

func (c *h2Conn) trailers(fields []h2.Field) error { return c.headers(fields, true) }

func (c *h2Conn) headers(fields []h2.Field, end bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.block.Reset()
	for _, f := range fields {
		if err := c.encoder.WriteField(hpack.HeaderField{Name: f.Name, Value: f.Value}); err != nil {
			return err
		}
	}
	return c.framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID: c.stream, BlockFragment: c.block.Bytes(), EndStream: end, EndHeaders: true,
	})
}

func (c *h2Conn) message(data []byte, end bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.framer.WriteData(c.stream, end, data)
}
