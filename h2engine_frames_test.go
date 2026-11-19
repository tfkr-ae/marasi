package marasi

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// splitHeaders sends a header block of name, value pairs on stream id as a
// HEADERS frame holding its first two bytes and a CONTINUATION holding the
// rest.
func (p *h2TestPeer) splitHeaders(id uint32, end bool, pairs ...string) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.block.Reset()
	for i := 0; i+1 < len(pairs); i += 2 {
		p.encoder.WriteField(hpack.HeaderField{Name: pairs[i], Value: pairs[i+1]})
	}
	block := bytes.Clone(p.block.Bytes())
	p.framer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: block[:2], EndStream: end})
	p.framer.WriteContinuation(id, true, block[2:])
}

// floodHeaders sends HEADERS on stream id and CONTINUATION frames after it,
// none ending the block, until more than size bytes of block were sent.
func (p *h2TestPeer) floodHeaders(id uint32, size int) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	fragment := make([]byte, 16384)
	if p.framer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: fragment}) != nil {
		return
	}
	for sent := len(fragment); sent <= size; sent += len(fragment) {
		if p.framer.WriteContinuation(id, false, fragment) != nil {
			return
		}
	}
}

func TestH2Engine_HeaderBlocks(t *testing.T) {
	t.Run("should keep HPACK in step with an origin header block split across CONTINUATION on a stream that is no longer open", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/first")
		first := upstream.nthStream(t, 0)
		upstream.headers(first, true, ":status", "204")
		client.stream(t, 1)
		// The block adds x-late to the origin's dynamic table; the next
		// response refers to it by index.
		upstream.splitHeaders(first, true, "x-late", "indexed")

		client.headers(3, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/second")
		second := upstream.nthStream(t, 1)
		upstream.headers(second, true, ":status", "200", "x-late", "indexed")
		want := h2Fields{{":status", "200", false}, {"x-late", "indexed", false}}
		if response := client.stream(t, 3); len(response.headers) != 1 || !equalFields(response.headers[0], want) {
			t.Fatalf("the second response\nwanted:\n%v\ngot:\n%v %q", want, response.headers, response.data)
		}
	})

	t.Run("should end a client connection with ENHANCE_YOUR_CALM once a header block runs past the head limit", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		origin.accept(t)

		go client.floodHeaders(1, h1.MaxHeadBytes+16384)
		client.await(t, "GOAWAY", func(p *h2TestPeer) bool { return p.goAway != nil })
		if code := client.goAway.ErrCode; code != http2.ErrCodeEnhanceYourCalm {
			t.Fatalf("GOAWAY\nwanted:\nENHANCE_YOUR_CALM\ngot:\n%s", code)
		}
	})

	t.Run("should end a client connection with ENHANCE_YOUR_CALM when a header block decodes past the head limit", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		origin.accept(t)

		// One large field, then one-byte references to it.
		pairs := []string{":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/"}
		large := strings.Repeat("v", 4000)
		for range h1.MaxHeadBytes/len(large) + 1 {
			pairs = append(pairs, "x-large", large)
		}
		client.headers(1, true, pairs...)
		client.await(t, "GOAWAY", func(p *h2TestPeer) bool { return p.goAway != nil })
		if code := client.goAway.ErrCode; code != http2.ErrCodeEnhanceYourCalm {
			t.Fatalf("GOAWAY\nwanted:\nENHANCE_YOUR_CALM\ngot:\n%s", code)
		}
	})

	t.Run("should end the origin connection with ENHANCE_YOUR_CALM once a response header block runs past the head limit, and answer the stream with a 502", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		go upstream.floodHeaders(upstream.nthStream(t, 0), h1.MaxHeadBytes+16384)
		upstream.await(t, "GOAWAY", func(p *h2TestPeer) bool { return p.goAway != nil })
		if code := upstream.goAway.ErrCode; code != http2.ErrCodeEnhanceYourCalm {
			t.Fatalf("the origin's GOAWAY\nwanted:\nENHANCE_YOUR_CALM\ngot:\n%s", code)
		}
		if status, _ := client.stream(t, 1).headers[0].get(":status"); status != "502" {
			t.Fatalf("the client's response\nwanted:\n502\ngot:\n%v", status)
		}
	})
}

func TestH2Engine_OriginStreams(t *testing.T) {
	t.Run("should relay every interim response the origin sends before its final one", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		id := upstream.nthStream(t, 0)
		const hints = 100
		for i := range hints {
			upstream.headers(id, false, ":status", "103", "link", "</"+strconv.Itoa(i)+">")
		}
		upstream.headers(id, true, ":status", "204")
		if response := client.stream(t, 1); len(response.headers) != hints+1 {
			t.Fatalf("the client's header blocks\nwanted:\n%d interim and the final one\ngot:\n%d", hints, len(response.headers))
		}
	})

	t.Run("should reset an origin stream whose send window the origin overflows with FLOW_CONTROL_ERROR, and answer the client with a 502", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		id := upstream.nthStream(t, 0)
		upstream.write(func(f *http2.Framer) error { return f.WriteWindowUpdate(id, 1<<31-1) })
		upstream.await(t, "RST_STREAM", func(p *h2TestPeer) bool { return p.streams[id].reset != "" })
		if reset := upstream.stream(t, id).reset; reset != "FLOW_CONTROL_ERROR" {
			t.Fatalf("the origin stream's reset\nwanted:\nFLOW_CONTROL_ERROR\ngot:\n%s", reset)
		}
		if status, _ := client.stream(t, 1).headers[0].get(":status"); status != "502" {
			t.Fatalf("the client's response\nwanted:\n502\ngot:\n%v", status)
		}
	})
}
