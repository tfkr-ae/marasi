package marasi

import (
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// awaitData waits until at least n bytes of DATA arrived on stream id,
// within wait, and returns what arrived on it so far.
func (p *h2TestPeer) awaitData(t *testing.T, id uint32, n int, wait time.Duration) h2TestStream {
	t.Helper()
	done := make(chan struct{})
	timer := time.AfterFunc(wait, func() { close(done) })
	defer timer.Stop()
	for {
		p.mu.Lock()
		s, changed := p.streams[id], p.changed
		var got h2TestStream
		if s != nil {
			got = *s
		}
		p.mu.Unlock()
		if s != nil && len(got.data) >= n {
			return got
		}
		select {
		case <-changed:
		case <-done:
			t.Fatalf("waiting for %d bytes on stream %d: got %q", n, id, got.data)
		}
	}
}

// expectStoredH2 checks the captured request and response raw texts and
// returns their metadata.
func expectStored(t *testing.T, captured *capturedPair) (request, response map[string]any, requestRaw, responseRaw string) {
	t.Helper()
	select {
	case req := <-captured.request:
		request, requestRaw = req.Metadata, string(req.Raw)
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not captured")
	}
	select {
	case res := <-captured.response:
		response, responseRaw = res.Metadata, string(res.Raw)
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
	}
	return request, response, requestRaw, responseRaw
}

func TestH2Engine_BodyThreshold(t *testing.T) {
	t.Run("should send a request declaring a length over the threshold head first and give the client the origin's early answer", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/", "content-length", "999999999")
		id := upstream.nthStream(t, 0)
		upstream.headers(id, true, ":status", "413")

		response := client.stream(t, 1)
		if status, _ := response.headers[0].get(":status"); status != "413" {
			t.Fatalf("wanted the origin's 413\ngot:\n%v", response.headers)
		}
		request, _, raw, _ := expectStored(t, captured)
		want := "POST / HTTP/2\r\n:scheme: https\r\n:authority: " + origin.addr + "\r\ncontent-length: 999999999\r\n\r\n"
		expectPassedThrough(t, "request", want, []byte(raw), request)
	})

	t.Run("should relay the origin's 100 to a request whose body goes as it arrives", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, withBodyThreshold(4))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/", "expect", "100-continue", "content-length", "10")
		id := upstream.nthStream(t, 0)
		upstream.headers(id, false, ":status", "100")
		client.await(t, "the 100", func(p *h2TestPeer) bool { s := p.streams[1]; return s != nil && len(s.headers) == 1 })
		client.data(1, true, "0123456789")
		upstream.awaitData(t, id, 10, 5*time.Second)
		upstream.headers(id, true, ":status", "204")
		if response := client.stream(t, 1); len(response.headers) != 2 {
			t.Fatalf("wanted the 100 and the 204\ngot:\n%v", response.headers)
		}
	})

	t.Run("should forward a request body from the point it passes the threshold, as it arrives, and store it up to the threshold", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true, withBodyThreshold(8))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		client.data(1, false, "0123456789")
		id := upstream.nthStream(t, 0)
		upstream.awaitData(t, id, 10, time.Second)
		client.data(1, false, "abc")
		client.headers(1, true, "x-sum", "1")
		request := upstream.stream(t, id)
		if string(request.data) != "0123456789abc" || len(request.trailers) != 1 {
			t.Fatalf("wanted the origin to receive the whole body and the trailers\ngot:\n%q %v", request.data, request.trailers)
		}
		upstream.headers(id, true, ":status", "204")
		client.stream(t, 1)

		metadata, _, raw, _ := expectStored(t, captured)
		expectPassedThrough(t, "request", "POST / HTTP/2\r\n:scheme: https\r\n:authority: "+origin.addr+"\r\n\r\n01234567", []byte(raw), metadata)
	})

	t.Run("should forward a request body that stalls after some arrived", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		client.data(1, false, "abc")
		id := upstream.nthStream(t, 0)
		upstream.awaitData(t, id, 3, 4*time.Second)
		client.data(1, true, "def")
		if request := upstream.stream(t, id); string(request.data) != "abcdef" {
			t.Fatalf("wanted the origin to receive the whole body\ngot:\n%q", request.data)
		}
		upstream.headers(id, true, ":status", "204")
		client.stream(t, 1)
	})

	t.Run("should send the rest of a passed request body to an origin that answered first", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, withBodyThreshold(8))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		client.data(1, false, "0123456789")
		id := upstream.nthStream(t, 0)
		upstream.awaitData(t, id, 10, time.Second)
		upstream.headers(id, false, ":status", "200")
		upstream.data(id, true, "early")
		if response := client.stream(t, 1); string(response.data) != "early" {
			t.Fatalf("wanted the client to receive the early answer\ngot:\n%q", response.data)
		}
		client.data(1, true, "abc")
		if request := upstream.stream(t, id); string(request.data) != "0123456789abc" {
			t.Fatalf("wanted the origin to receive the rest of the body\ngot:\n%q %q", request.data, request.reset)
		}
	})

	t.Run("should forward a response body from the point it passes the threshold, as it arrives, and store it up to the threshold", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true, withBodyThreshold(8))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		id := upstream.nthStream(t, 0)
		upstream.headers(id, false, ":status", "200", "content-type", "text/plain")
		upstream.data(id, false, "0123456789")
		client.awaitData(t, 1, 10, time.Second)
		upstream.data(id, false, "abc")
		upstream.headers(id, true, "x-sum", "1")
		response := client.stream(t, 1)
		if string(response.data) != "0123456789abc" || len(response.trailers) != 1 {
			t.Fatalf("wanted the client to receive the whole body and the trailers\ngot:\n%q %v", response.data, response.trailers)
		}

		_, metadata, _, raw := expectStored(t, captured)
		expectPassedThrough(t, "response", "HTTP/2 200\r\ncontent-type: text/plain\r\n\r\n01234567", []byte(raw), metadata)
	})

	t.Run("should forward a response body that stalls after some arrived, and pass on the origin's reset", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		id := upstream.nthStream(t, 0)
		upstream.headers(id, false, ":status", "200")
		upstream.data(id, false, "first")
		client.awaitData(t, 1, 5, 4*time.Second)
		upstream.write(func(f *http2.Framer) error { return f.WriteRSTStream(id, http2.ErrCodeInternal) })
		if response := client.stream(t, 1); response.reset != http2.ErrCodeInternal.String() {
			t.Fatalf("wanted the client's stream reset with INTERNAL_ERROR\ngot:\n%q", response.reset)
		}
	})

	t.Run("should not hold a passed request in Checkpoint, and name the skipped hold in a proxy log", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		logs := make(chan string, 64)
		addr, _, ca := serveH1EngineMITMProxy(t, true, withBodyThreshold(4), collectLogs(logs), holdEverything(t))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/", "content-length", "10")
		client.data(1, true, "0123456789")
		id := upstream.nthStream(t, 0)
		if request := upstream.stream(t, id); string(request.data) != "0123456789" {
			t.Fatalf("wanted the origin to receive the body\ngot:\n%q", request.data)
		}
		expectLog(t, logs, "Checkpoint hold", "request to POST")
	})
}
