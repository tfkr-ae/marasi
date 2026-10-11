package websocket

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// deflateTail is the empty stored block that ends every sync flush. RFC 7692
// removes it from each compressed message.
var deflateTail = []byte{0x00, 0x00, 0xff, 0xff}

// peerCompressor is a test peer's permessage-deflate compressor. With context
// takeover it keeps one flate stream across messages.
type peerCompressor struct {
	t                 *testing.T
	noContextTakeover bool
	buf               bytes.Buffer
	w                 *flate.Writer
}

func newPeerCompressor(t *testing.T, noContextTakeover bool) *peerCompressor {
	t.Helper()
	p := &peerCompressor{t: t, noContextTakeover: noContextTakeover}
	w, err := flate.NewWriter(&p.buf, flate.BestCompression)
	if err != nil {
		t.Fatalf("creating flate writer: %v", err)
	}
	p.w = w
	return p
}

func (p *peerCompressor) compress(payload []byte) []byte {
	p.t.Helper()
	if p.noContextTakeover {
		p.w.Reset(&p.buf)
	}
	p.buf.Reset()
	if _, err := p.w.Write(payload); err != nil {
		p.t.Fatalf("compressing: %v", err)
	}
	if err := p.w.Flush(); err != nil {
		p.t.Fatalf("flushing: %v", err)
	}
	out := bytes.Clone(p.buf.Bytes())
	if !bytes.HasSuffix(out, deflateTail) {
		p.t.Fatalf("compressed message %x does not end with a sync flush", out)
	}
	return out[:len(out)-len(deflateTail)]
}

// peerDecompressor is a test peer's permessage-deflate decompressor. It keeps
// every compressed byte it received and inflates the whole stream again for
// each message, so context takeover needs no dictionary handling.
type peerDecompressor struct {
	t                 *testing.T
	noContextTakeover bool
	stream            []byte
	inflated          int
}

func (p *peerDecompressor) decompress(payload []byte) []byte {
	p.t.Helper()
	if p.noContextTakeover {
		p.stream, p.inflated = nil, 0
	}
	p.stream = append(append(p.stream, payload...), deflateTail...)
	all, err := io.ReadAll(flate.NewReader(bytes.NewReader(p.stream)))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		p.t.Fatalf("inflating message %x: %v", payload, err)
	}
	if len(all) < p.inflated {
		p.t.Fatalf("inflated stream shrank from %d to %d bytes", p.inflated, len(all))
	}
	message := all[p.inflated:]
	p.inflated = len(all)
	return message
}

func withDeflate(params domain.WebSocketDeflate) func(*Connection) {
	return func(c *Connection) { c.SetDeflate(&params) }
}

// contextTakeover is permessage-deflate with no parameters: both sides keep
// their compression context across messages and use the full window.
var contextTakeover = domain.WebSocketDeflate{ServerMaxWindowBits: 15, ClientMaxWindowBits: 15}

func TestRelay_DeflateEditKeepsLaterMessagesIntact(t *testing.T) {
	sent := []string{
		"the quick brown fox jumps over the lazy dog",
		"the quick brown fox jumps over the lazy dog, twice",
		"the quick brown fox jumps over the lazy dog, three times",
		"the quick brown fox jumps over the lazy dog, four times",
	}
	want := []string{sent[0], "edited", sent[2], sent[3]}

	edit := func(message *Message) error {
		if bytes.Equal(message.Payload, []byte(sent[1])) {
			message.Payload = []byte("edited")
		}
		return nil
	}

	t.Run("should deliver every message after an edit from the client", func(t *testing.T) {
		relay := startRelayWith(t, edit, withDeflate(contextTakeover))
		client := newPeerCompressor(t, false)
		upstream := &peerDecompressor{t: t}

		for i, text := range sent {
			writeFrames(relay.client, true, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: client.compress([]byte(text))})
			frame := readFrame(t, relay.upstream)
			if !frame.Rsv1 {
				t.Fatalf("\nwanted message %d:\ncompressed\ngot:\nuncompressed %q", i, frame.Payload)
			}
			if got := upstream.decompress(frame.Payload); string(got) != want[i] {
				t.Fatalf("\nwanted message %d:\n%q\ngot:\n%q", i, want[i], got)
			}
		}
	})

	t.Run("should deliver every message after an edit from the server", func(t *testing.T) {
		relay := startRelayWith(t, edit, withDeflate(contextTakeover))
		server := newPeerCompressor(t, false)
		client := &peerDecompressor{t: t}

		for i, text := range sent {
			writeFrames(relay.upstream, false, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: server.compress([]byte(text))})
			frame := readFrame(t, relay.client)
			if got := client.decompress(frame.Payload); string(got) != want[i] {
				t.Fatalf("\nwanted message %d:\n%q\ngot:\n%q", i, want[i], got)
			}
		}
	})
}
