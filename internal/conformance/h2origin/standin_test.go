package h2origin_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// mode is how the stand-in proxy treats h2 inside CONNECT.
type mode int

const (
	// reencode terminates h2 on both legs and relays each stream's
	// meaning with its own SETTINGS, its own HPACK encoding (no dynamic
	// table), every DATA payload split into padded one-byte frames, and
	// other stream IDs towards the origin.
	reencode mode = iota
	// broken re-encodes as reencode does and also appends "x" to the
	// first regular field of each response header block and flips the
	// first DATA byte of each response.
	broken
	// http11 answers ALPN with http/1.1 only, as Martian does, and so
	// never reaches the origin.
	http11
)

// standIn is an in-process forward proxy for the harness's own tests: it
// takes CONNECT, dials every origin on loopback, and intercepts TLS with
// its own CA, dialling the origin over TLS that trusts the harness CA.
type standIn struct {
	listener  net.Listener
	mode      mode
	harnessCA *conformance.CA
	ownCA     *conformance.CA

	mu    sync.Mutex
	conns []net.Conn
	wg    sync.WaitGroup
}

func startStandIn(t *testing.T, m mode, harnessCA *conformance.CA) *standIn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ownCA, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	s := &standIn{listener: listener, mode: m, harnessCA: harnessCA, ownCA: ownCA}
	s.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.track(conn)
			s.wg.Go(func() { s.serve(conn) })
		}
	})
	t.Cleanup(s.Stop)
	return s
}

func (s *standIn) Address() string { return s.listener.Addr().String() }

// Newest is never asked: h2-origin checks intercepted TLS instead.
func (s *standIn) Newest(context.Context, conformance.TrafficKind) (string, error) { return "", nil }

// Certificate is the stand-in's CA certificate, as Marasi's comes from
// certificate get.
func (s *standIn) Certificate(context.Context) ([]byte, error) { return s.ownCA.PEM(), nil }

func (s *standIn) Stop() {
	_ = s.listener.Close()
	s.mu.Lock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *standIn) track(conn net.Conn) {
	s.mu.Lock()
	s.conns = append(s.conns, conn)
	s.mu.Unlock()
}

func (s *standIn) serve(raw net.Conn) {
	defer func() { _ = raw.Close() }()
	reader := bufio.NewReader(raw)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(header) == "" {
			break
		}
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "CONNECT" {
		return
	}
	authority := fields[1]
	if _, err := io.WriteString(raw, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	host, port, _ := net.SplitHostPort(authority)
	config, err := s.ownCA.ServerTLS(host)
	if err != nil {
		return
	}
	config.NextProtos = []string{"h2"}
	if s.mode == http11 {
		config.NextProtos = []string{"http/1.1"}
	}
	client := tls.Server(&bufferedConn{Conn: raw, r: reader}, config)
	if err := client.Handshake(); err != nil {
		return
	}
	if s.mode == http11 {
		_, _ = io.Copy(io.Discard, client)
		return
	}
	upstream, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", port), &tls.Config{
		RootCAs: s.harnessCA.Pool(), ServerName: host, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return
	}
	s.track(upstream)
	defer func() { _ = upstream.Close() }()

	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(client, preface); err != nil || string(preface) != http2.ClientPreface {
		return
	}
	if _, err := io.WriteString(upstream, http2.ClientPreface); err != nil {
		return
	}
	r := &relay{
		broken:   s.mode == broken,
		toOrigin: newLeg(upstream),
		toClient: newLeg(client),
		up:       map[uint32]uint32{},
		down:     map[uint32]uint32{},
		next:     3,
	}
	if err := r.toClient.do(func(f *http2.Framer) error {
		return f.WriteSettings(http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 33},
			http2.Setting{ID: http2.SettingInitialWindowSize, Val: 1 << 20},
			http2.Setting{ID: http2.SettingMaxFrameSize, Val: 1 << 20})
	}); err != nil {
		return
	}
	if err := r.toOrigin.do(func(f *http2.Framer) error {
		return f.WriteSettings(http2.Setting{ID: http2.SettingEnablePush, Val: 0},
			http2.Setting{ID: http2.SettingInitialWindowSize, Val: 12345})
	}); err != nil {
		return
	}
	var pumps sync.WaitGroup
	pumps.Go(func() {
		r.pump(r.toClient, r.toOrigin, true)
		_ = upstream.Close()
	})
	pumps.Go(func() {
		r.pump(r.toOrigin, r.toClient, false)
		_ = client.Close()
	})
	pumps.Wait()
}

// leg is one side of the relay: its framer, guarded for the two pumps
// that write to it, and the HPACK state for what arrives from it and what
// is sent to it.
type leg struct {
	mu      sync.Mutex
	framer  *http2.Framer
	decoder *hpack.Decoder
	block   bytes.Buffer
	encoder *hpack.Encoder
	pending []byte // a header block waiting for its CONTINUATION
}

func newLeg(conn net.Conn) *leg {
	l := &leg{framer: http2.NewFramer(conn, conn), decoder: hpack.NewDecoder(4096, nil)}
	l.encoder = hpack.NewEncoder(&l.block)
	// No dynamic table: every field is sent as a literal, an encoding
	// that differs from the origin's and the client's.
	l.encoder.SetMaxDynamicTableSizeLimit(0)
	return l
}

func (l *leg) do(write func(*http2.Framer) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return write(l.framer)
}

// relay maps streams between the legs: client stream IDs to origin IDs
// starting at 3, so the IDs differ.
type relay struct {
	broken             bool
	toOrigin, toClient *leg
	mu                 sync.Mutex
	up, down           map[uint32]uint32
	next               uint32
	changed            map[uint32]bool // response streams whose first DATA byte was flipped
}

// streamID maps a stream ID from one leg to the other.
func (r *relay) streamID(id uint32, upstream bool) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !upstream {
		return r.down[id]
	}
	if mapped, ok := r.up[id]; ok {
		return mapped
	}
	r.up[id], r.down[r.next] = r.next, id
	r.next += 2
	return r.up[id]
}

// lastStreamID maps a GOAWAY's last stream ID to the other leg: the
// highest mapped ID at or below it.
func (r *relay) lastStreamID(last uint32, upstream bool) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	from := r.down
	if upstream {
		from = r.up
	}
	var mapped uint32
	for id, to := range from {
		if id <= last {
			mapped = max(mapped, to)
		}
	}
	return mapped
}

// pump reads frames from one leg and sends their meaning to the other.
func (r *relay) pump(from, to *leg, upstream bool) {
	for {
		frame, err := from.framer.ReadFrame()
		if err != nil {
			return
		}
		if err := r.relayFrame(frame, from, to, upstream); err != nil {
			return
		}
	}
}

func (r *relay) relayFrame(frame http2.Frame, from, to *leg, upstream bool) error {
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if !f.IsAck() {
			return from.do(func(w *http2.Framer) error { return w.WriteSettingsAck() })
		}
	case *http2.PingFrame:
		if !f.IsAck() {
			data := f.Data
			return from.do(func(w *http2.Framer) error { return w.WritePing(true, data) })
		}
	case *http2.HeadersFrame:
		from.pending = append(from.pending[:0], f.HeaderBlockFragment()...)
		if f.HeadersEnded() {
			return r.headers(f.StreamID, f.StreamEnded(), from, to, upstream)
		}
	case *http2.ContinuationFrame:
		from.pending = append(from.pending, f.HeaderBlockFragment()...)
		if f.HeadersEnded() {
			// END_STREAM sits on the HEADERS frame; this relay's
			// tests never split a block, so a CONTINUATION ends nothing.
			return r.headers(f.StreamID, false, from, to, upstream)
		}
	case *http2.DataFrame:
		id := r.streamID(f.StreamID, upstream)
		data := bytes.Clone(f.Data())
		if r.broken && !upstream && len(data) > 0 && !r.flipped(id) {
			data[0] ^= 0x20
		}
		return to.do(func(w *http2.Framer) error {
			if len(data) == 0 {
				return w.WriteData(id, f.StreamEnded(), nil)
			}
			for i := range data {
				end := f.StreamEnded() && i == len(data)-1
				if err := w.WriteDataPadded(id, end, data[i:i+1], make([]byte, 2)); err != nil {
					return err
				}
			}
			return nil
		})
	case *http2.RSTStreamFrame:
		id := r.streamID(f.StreamID, upstream)
		return to.do(func(w *http2.Framer) error { return w.WriteRSTStream(id, f.ErrCode) })
	case *http2.GoAwayFrame:
		last := r.lastStreamID(f.LastStreamID, upstream)
		return to.do(func(w *http2.Framer) error { return w.WriteGoAway(last, f.ErrCode, nil) })
	}
	return nil
}

// flipped reports whether a response stream's first DATA byte was already
// flipped, and marks it.
func (r *relay) flipped(id uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.changed == nil {
		r.changed = map[uint32]bool{}
	}
	done := r.changed[id]
	r.changed[id] = true
	return done
}

// headers decodes a whole header block from one leg and sends it, encoded
// afresh, to the other.
func (r *relay) headers(streamID uint32, endStream bool, from, to *leg, upstream bool) error {
	fields, err := from.decoder.DecodeFull(from.pending)
	if err != nil {
		return err
	}
	if r.broken && !upstream {
		for i := range fields {
			if !strings.HasPrefix(fields[i].Name, ":") {
				fields[i].Value += "x"
				break
			}
		}
	}
	id := r.streamID(streamID, upstream)
	to.mu.Lock()
	defer to.mu.Unlock()
	to.block.Reset()
	for _, field := range fields {
		if err := to.encoder.WriteField(field); err != nil {
			return err
		}
	}
	return to.framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID: id, BlockFragment: to.block.Bytes(), EndStream: endStream, EndHeaders: true,
	})
}

// bufferedConn reads through r, which may hold bytes already read from Conn.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// uncertified hides a proxy's Certificate method, as a proxy that gives no
// CA certificate would.
type uncertified struct{ conformance.Proxy }
