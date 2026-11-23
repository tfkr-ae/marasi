package marasi

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"sync"
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
