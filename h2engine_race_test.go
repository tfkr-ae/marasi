package marasi

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// newBareH2Origin is an origin leg with no connection, for tests that
// drive its streams' bookkeeping directly.
func newBareH2Origin() *h2Origin {
	return &h2Origin{flow: newH2Flow(), streams: make(map[uint32]*h2OriginStream), nextID: 1, relayed: make(chan struct{})}
}

// openBareStream files a new stream on o, as openStream does.
func openBareStream(o *h2Origin) *h2OriginStream {
	s := newH2OriginStream()
	o.mu.Lock()
	s.id = o.nextID
	o.nextID += 2
	o.streams[s.id] = s
	o.mu.Unlock()
	return s
}

func TestH2Origin_FinishRacesStall(t *testing.T) {
	t.Run("should leave no passing response body unended, and read the response without a data race, when the stall wait fires as the trailers arrive", func(t *testing.T) {
		wait := bodyStallWait
		bodyStallWait = time.Millisecond
		t.Cleanup(func() { bodyStallWait = wait })

		const rounds = 300
		for i := range rounds {
			o := newBareH2Origin()
			s := openBareStream(o)
			s.passMu.Lock()
			s.final = true
			s.response = &h2Response{fields: h2Fields{{Name: ":status", Value: "200"}}}
			s.passMu.Unlock()
			s.receive([]byte("partial"), 7, false, 1<<20, func(uint32) {})
			// The stream's wait reads the response once the body
			// passed or the stream finished, as awaitResponse does.
			read := make(chan struct{})
			go func() {
				defer close(read)
				select {
				case <-s.passed:
				case <-s.done:
				}
				s.response.response()
			}()
			// The trailers end the stream around when the stall fires.
			time.Sleep(time.Duration(i%3) * 500 * time.Microsecond)
			s.fileTrailers(h2Fields{{Name: "x-end", Value: "yes"}})
			o.finish(s, nil)
			<-read

			pass := s.passing()
			if pass == nil {
				continue
			}
			ended := make(chan error, 1)
			go func() {
				for {
					data, err := pass.in.peek()
					if len(data) > 0 {
						pass.in.discard(len(data))
						continue
					}
					ended <- err
					return
				}
			}()
			select {
			case err := <-ended:
				if trailers := pass.in.endTrailers(); err == nil || len(trailers) != 1 {
					t.Fatalf("round %d: the passing body's end\nwanted:\nEOF with the trailers\ngot:\n%v, %v", i, err, trailers)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("round %d: the passing body\nwanted:\nended\ngot:\nwaiting for more forever", i)
			}
		}
	})

	t.Run("should cut a passing response body the stall made when the origin resets the stream", func(t *testing.T) {
		wait := bodyStallWait
		bodyStallWait = time.Millisecond
		t.Cleanup(func() { bodyStallWait = wait })

		o := newBareH2Origin()
		s := openBareStream(o)
		s.final = true
		s.response = &h2Response{}
		s.receive([]byte("partial"), 7, false, 1<<20, func(uint32) {})
		select {
		case <-s.passed:
		case <-time.After(2 * time.Second):
			t.Fatal("the stall\nwanted:\nthe body passed\ngot:\nnot yet")
		}
		o.resetBy(s, http2.ErrCodeInternal)
		pass := s.passing()
		pass.in.discard(len(pass.in.buf))
		_, err := pass.in.peek()
		var reset *streamResetError
		if !errors.As(err, &reset) || reset.code != http2.ErrCodeInternal {
			t.Fatalf("the passing body's end\nwanted:\nreset INTERNAL_ERROR\ngot:\n%v", err)
		}
	})
}

func TestH2Origin_ResetRace(t *testing.T) {
	t.Run("should read an origin's reset without a data race while it is filed", func(t *testing.T) {
		o := newBareH2Origin()
		s := openBareStream(o)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			o.resetBy(s, http2.ErrCodeRefusedStream)
		}()
		var got error
		go func() {
			defer wg.Done()
			got = o.endedBy(s, errStreamGone)
		}()
		wg.Wait()
		var reset *streamResetError
		if !errors.Is(got, errStreamGone) && !(errors.As(got, &reset) && reset.code == http2.ErrCodeRefusedStream) {
			t.Fatalf("why the send failed\nwanted:\nthe stream gone or REFUSED_STREAM\ngot:\n%v", got)
		}
		if got := o.endedBy(s, errStreamGone); !errors.As(got, &reset) {
			t.Fatalf("why the send failed, after the reset\nwanted:\nREFUSED_STREAM\ngot:\n%v", got)
		}
	})
}

func TestH2Conn_LazyOriginDialRace(t *testing.T) {
	t.Run("should read the dialled origin for a redirected stream without a data race while the first stream dials it", func(t *testing.T) {
		proxy := &Proxy{}
		h := &h2Conn{engine: &h1Engine{proxy: proxy}, lazyOrigin: true, ended: true, streams: make(map[uint32]*h2Stream)}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			h.openOriginFor(t.Context(), "origin.test:80")
		}()
		var offered []string
		go func() {
			defer wg.Done()
			offered = h.redirectedALPN()
		}()
		wg.Wait()
		if len(offered) != 2 {
			t.Fatalf("the ALPN offered a redirected origin\nwanted:\nh2 and http/1.1\ngot:\n%v", offered)
		}
		if dialled := h.originDial.Load(); dialled == nil || !errors.Is(dialled.err, errH2CClosed) {
			t.Fatalf("the dialled origin\nwanted:\nfiled, closed\ngot:\n%v", dialled)
		}
	})
}

func TestH2Conn_CloseStreamWithOriginCode(t *testing.T) {
	t.Run("should close a streamed request body with the origin reset code so carry forwards CANCEL instead of INTERNAL_ERROR", func(t *testing.T) {
		h := &h2Conn{
			flow:    newH2Flow(),
			streams: make(map[uint32]*h2Stream),
		}
		in := newH2Inbound(1<<20, func(uint32) {})
		st := &h2Stream{
			id:     1,
			cancel: func() {},
			request: h2Request{
				streamed: &h2StreamedBody{in: in},
			},
		}
		h.streams[1] = st

		cancel := http2.ErrCodeCancel
		h.closeStream(st, &cancel)

		_, err := in.peek()
		var reset *streamResetError
		if !errors.As(err, &reset) || reset.code != http2.ErrCodeCancel {
			t.Fatalf("the streamed request body's end\nwanted:\nreset CANCEL\ngot:\n%v", err)
		}
	})

	t.Run("should cut a threshold-passed request body with the origin reset code so forwarding preserves the client's code", func(t *testing.T) {
		h := &h2Conn{
			flow:    newH2Flow(),
			streams: make(map[uint32]*h2Stream),
		}
		pass := newH2Passing(h2DefaultWindow, func(uint32) {}, nil)
		st := &h2Stream{
			id:     1,
			cancel: func() {},
			pass:   pass,
		}
		h.streams[1] = st

		code := http2.ErrCodeInternal
		h.closeStream(st, &code)

		_, err := pass.in.peek()
		var reset *streamResetError
		if !errors.As(err, &reset) || reset.code != http2.ErrCodeInternal {
			t.Fatalf("the passed request body's end\nwanted:\nreset INTERNAL_ERROR\ngot:\n%v", err)
		}
	})

	t.Run("should write at most one RST_STREAM to the client when reset races with itself or closeStream", func(t *testing.T) {
		var buf bytes.Buffer
		bw := bufio.NewWriter(&buf)
		framer := http2.NewFramer(bw, io.NopCloser(&bytes.Reader{}))
		h := &h2Conn{
			w:       newH2Writer(bw, framer),
			flow:    newH2Flow(),
			streams: make(map[uint32]*h2Stream),
		}
		st := &h2Stream{id: 1, cancel: func() {}}
		h.streams[1] = st

		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.reset(st, http2.ErrCodeCancel)
			}()
		}
		wg.Wait()

		reader := http2.NewFramer(io.Discard, bytes.NewReader(buf.Bytes()))
		resets := 0
		for {
			f, err := reader.ReadFrame()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("reading frames\nwanted:\nvalid frames\ngot:\n%v", err)
			}
			if rst, ok := f.(*http2.RSTStreamFrame); ok {
				resets++
				if rst.StreamID != 1 || rst.ErrCode != http2.ErrCodeCancel {
					t.Fatalf("the RST_STREAM frame\nwanted:\nstream 1 CANCEL\ngot:\nstream %d %s", rst.StreamID, rst.ErrCode)
				}
			}
		}
		if resets != 1 {
			t.Fatalf("RST_STREAM count\nwanted:\n1\ngot:\n%d", resets)
		}
	})
}

func TestH2Origin_CancelAndResetStreamed(t *testing.T) {
	t.Run("should write at most one RST_STREAM to the origin when cancel runs concurrently", func(t *testing.T) {
		var buf bytes.Buffer
		bw := bufio.NewWriter(&buf)
		framer := http2.NewFramer(bw, io.NopCloser(&bytes.Reader{}))
		o := newBareH2Origin()
		o.w = newH2Writer(bw, framer)
		s := openBareStream(o)

		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				o.cancel(s, http2.ErrCodeCancel)
			}()
		}
		wg.Wait()

		reader := http2.NewFramer(io.Discard, bytes.NewReader(buf.Bytes()))
		resets := 0
		for {
			f, err := reader.ReadFrame()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("reading frames\nwanted:\nvalid frames\ngot:\n%v", err)
			}
			if rst, ok := f.(*http2.RSTStreamFrame); ok {
				resets++
				if rst.StreamID != s.id || rst.ErrCode != http2.ErrCodeCancel {
					t.Fatalf("the RST_STREAM frame\nwanted:\nstream %d CANCEL\ngot:\nstream %d %s", s.id, rst.StreamID, rst.ErrCode)
				}
			}
		}
		if resets != 1 {
			t.Fatalf("RST_STREAM count\nwanted:\n1\ngot:\n%d", resets)
		}
	})

	t.Run("should close a streamed response body with the origin's reset code when resetBy finishes the stream", func(t *testing.T) {
		o := newBareH2Origin()
		s := openBareStream(o)
		in := newH2Inbound(h2OriginStreamWindow, func(uint32) {})
		s.streamed.Store(&h2StreamedBody{in: in})

		o.resetBy(s, http2.ErrCodeEnhanceYourCalm)

		_, err := in.peek()
		var reset *streamResetError
		if !errors.As(err, &reset) || reset.code != http2.ErrCodeEnhanceYourCalm {
			t.Fatalf("the streamed response body's end\nwanted:\nreset ENHANCE_YOUR_CALM\ngot:\n%v", err)
		}
	})
}

func TestH2Conn_Finish(t *testing.T) {
	t.Run("should drain pipelined frames after client GOAWAY without resetting the TCP connection", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, origin.addr)
		tlsConn, _ := clientTLS(t, conn, br, ca, "127.0.0.1", "h2")
		t.Cleanup(func() { tlsConn.Close() })
		origin.accept(t)

		if _, err := io.WriteString(tlsConn, http2.ClientPreface); err != nil {
			t.Fatalf("writing preface: %v", err)
		}
		framer := http2.NewFramer(tlsConn, tlsConn)
		if err := framer.WriteSettings(); err != nil {
			t.Fatalf("writing SETTINGS: %v", err)
		}
		// Read the proxy's SETTINGS and SETTINGS ACK before sending GOAWAY.
		sawSettings, sawAck := false, false
		tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for !sawSettings || !sawAck {
			f, err := framer.ReadFrame()
			if err != nil {
				t.Fatalf("reading handshake frames: %v", err)
			}
			if sf, ok := f.(*http2.SettingsFrame); ok {
				if sf.IsAck() {
					sawAck = true
				} else {
					sawSettings = true
					if err := framer.WriteSettingsAck(); err != nil {
						t.Fatalf("writing SETTINGS ACK: %v", err)
					}
				}
			}
		}

		// Send GOAWAY followed immediately by PING frames (as h2spec's
		// http2/7/1 and generic/3.8/1 do) and a TLS close_notify, coalesced
		// so unread frames are still buffered when GOAWAY is handled.
		var burst bytes.Buffer
		bf := http2.NewFramer(&burst, io.NopCloser(&bytes.Reader{}))
		if err := bf.WriteGoAway(0, 0xff, nil); err != nil {
			t.Fatalf("encoding GOAWAY: %v", err)
		}
		for range 16 {
			if err := bf.WritePing(false, [8]byte{'h', '2', 's', 'p', 'e', 'c'}); err != nil {
				t.Fatalf("encoding PING: %v", err)
			}
		}
		if _, err := tlsConn.Write(burst.Bytes()); err != nil {
			t.Fatalf("writing GOAWAY and PING burst: %v", err)
		}
		if err := tlsConn.CloseWrite(); err != nil {
			t.Fatalf("sending close_notify: %v", err)
		}

		// Drain the TLS stream to EOF, then verify the underlying TCP
		// connection ends with a clean FIN rather than RST.
		if _, err := io.Copy(io.Discard, tlsConn); err != nil {
			t.Fatalf("draining TLS stream\nwanted:\nclean EOF\ngot:\n%v", err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.Copy(io.Discard, conn); err != nil {
			if errors.Is(err, syscall.ECONNRESET) {
				t.Fatalf("the underlying TCP connection\nwanted:\nclean FIN\ngot:\nTCP reset (%v)", err)
			}
			t.Fatalf("draining the underlying TCP connection\nwanted:\nclean FIN\ngot:\n%v", err)
		}
	})

	t.Run("should half-close and drain pipelined frames after client GOAWAY over h2c", func(t *testing.T) {
		origin := newH2COrigin(t)
		addr, _ := serveH1EngineProxy(t)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, origin)

		if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
			t.Fatalf("writing preface: %v", err)
		}
		framer := http2.NewFramer(conn, br)
		if err := framer.WriteSettings(); err != nil {
			t.Fatalf("writing SETTINGS: %v", err)
		}
		sawSettings, sawAck := false, false
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for !sawSettings || !sawAck {
			f, err := framer.ReadFrame()
			if err != nil {
				t.Fatalf("reading handshake frames: %v", err)
			}
			if sf, ok := f.(*http2.SettingsFrame); ok {
				if sf.IsAck() {
					sawAck = true
				} else {
					sawSettings = true
					if err := framer.WriteSettingsAck(); err != nil {
						t.Fatalf("writing SETTINGS ACK: %v", err)
					}
				}
			}
		}

		var burst bytes.Buffer
		bf := http2.NewFramer(&burst, io.NopCloser(&bytes.Reader{}))
		if err := bf.WriteGoAway(0, 0xff, nil); err != nil {
			t.Fatalf("encoding GOAWAY: %v", err)
		}
		for range 16 {
			if err := bf.WritePing(false, [8]byte{'h', '2', 's', 'p', 'e', 'c'}); err != nil {
				t.Fatalf("encoding PING: %v", err)
			}
		}
		if _, err := conn.Write(burst.Bytes()); err != nil {
			t.Fatalf("writing GOAWAY and PING burst: %v", err)
		}

		// Keep the client's write side open at first: only a TCP half-close
		// ends the read before h2LingerTimeout.
		conn.SetReadDeadline(time.Now().Add(h2LingerTimeout / 2))
		if _, err := io.Copy(io.Discard, br); err != nil {
			t.Fatalf("waiting for the half-close\nwanted:\nclean EOF before linger\ngot:\n%v", err)
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	})

	t.Run("should half-close and drain pipelined frames after origin close ends an idle connection", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		conn, br := dialProxy(t, addr)
		openTunnel(t, conn, br, origin.addr)
		tlsConn, _ := clientTLS(t, conn, br, ca, "127.0.0.1", "h2")
		t.Cleanup(func() { tlsConn.Close() })
		upstream := origin.accept(t)

		if _, err := io.WriteString(tlsConn, http2.ClientPreface); err != nil {
			t.Fatalf("writing preface: %v", err)
		}
		framer := http2.NewFramer(tlsConn, tlsConn)
		if err := framer.WriteSettings(); err != nil {
			t.Fatalf("writing SETTINGS: %v", err)
		}
		sawSettings, sawAck := false, false
		tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for !sawSettings || !sawAck {
			f, err := framer.ReadFrame()
			if err != nil {
				t.Fatalf("reading handshake frames: %v", err)
			}
			if sf, ok := f.(*http2.SettingsFrame); ok {
				if sf.IsAck() {
					sawAck = true
				} else {
					sawSettings = true
					if err := framer.WriteSettingsAck(); err != nil {
						t.Fatalf("writing SETTINGS ACK: %v", err)
					}
				}
			}
		}

		// Origin closes while idle: Marasi half-closes the client side before
		// h2LingerTimeout, and drains pipelined frames without resetting TCP.
		upstream.conn.Close()
		tlsConn.SetReadDeadline(time.Now().Add(h2LingerTimeout / 2))
		if _, err := io.Copy(io.Discard, tlsConn); err != nil {
			t.Fatalf("waiting for EOF after origin close\nwanted:\nclean EOF before linger\ngot:\n%v", err)
		}
		if err := framer.WritePing(false, [8]byte{'l', 'a', 't', 'e'}); err != nil {
			t.Fatalf("writing late PING: %v", err)
		}
		if err := tlsConn.CloseWrite(); err != nil {
			t.Fatalf("sending close_notify: %v", err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.Copy(io.Discard, conn); err != nil {
			if errors.Is(err, syscall.ECONNRESET) {
				t.Fatalf("the underlying TCP connection\nwanted:\nclean FIN\ngot:\nTCP reset (%v)", err)
			}
			t.Fatalf("draining the underlying TCP connection\nwanted:\nclean FIN\ngot:\n%v", err)
		}
	})

	t.Run("should flush GOAWAY before half-closing writes when the last stream ends concurrently", func(t *testing.T) {
		for range 50 {
			c := &halfCloseRecorder{}
			bw := bufio.NewWriter(c)
			framer := http2.NewFramer(bw, io.NopCloser(&bytes.Reader{}))
			h := &h2Conn{
				conn:    c,
				w:       newH2Writer(bw, framer),
				flow:    newH2Flow(),
				streams: make(map[uint32]*h2Stream),
				sent:    make(map[uint32]uint32),
				maxID:   1,
			}
			st := &h2Stream{id: 1, cancel: func() {}}
			h.streams[1] = st

			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				h.goAway()
			}()
			go func() {
				defer wg.Done()
				<-start
				h.closeStream(st, nil)
			}()
			close(start)
			wg.Wait()

			if !c.closedWrite {
				t.Fatal("the write side\nwanted:\nhalf-closed\ngot:\nstill open")
			}
			reader := http2.NewFramer(io.Discard, bytes.NewReader(c.bytes()))
			sawGoAway := false
			for {
				f, err := reader.ReadFrame()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("reading frames\nwanted:\nvalid frames\ngot:\n%v", err)
				}
				if _, ok := f.(*http2.GoAwayFrame); ok {
					sawGoAway = true
				}
			}
			if !sawGoAway {
				t.Fatal("GOAWAY\nwanted:\nwritten before CloseWrite\ngot:\nnone")
			}
		}
	})
}

type halfCloseRecorder struct {
	mu          sync.Mutex
	buf         bytes.Buffer
	closedWrite bool
}

func (c *halfCloseRecorder) Read([]byte) (int, error) { return 0, io.EOF }
func (c *halfCloseRecorder) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closedWrite {
		return 0, net.ErrClosed
	}
	return c.buf.Write(p)
}
func (c *halfCloseRecorder) CloseWrite() error {
	c.mu.Lock()
	c.closedWrite = true
	c.mu.Unlock()
	return nil
}
func (c *halfCloseRecorder) Close() error                     { return c.CloseWrite() }
func (c *halfCloseRecorder) LocalAddr() net.Addr              { return nil }
func (c *halfCloseRecorder) RemoteAddr() net.Addr             { return nil }
func (c *halfCloseRecorder) SetDeadline(time.Time) error      { return nil }
func (c *halfCloseRecorder) SetReadDeadline(time.Time) error  { return nil }
func (c *halfCloseRecorder) SetWriteDeadline(time.Time) error { return nil }
func (c *halfCloseRecorder) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}
