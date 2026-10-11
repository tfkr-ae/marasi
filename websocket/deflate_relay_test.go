package websocket

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"net"
	"strings"
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

// recordMessages returns a process function that announces every text and
// binary message on the returned channel.
func recordMessages() (ProcessFunc, <-chan domain.WebSocketMessage) {
	recorded := make(chan domain.WebSocketMessage, 4)
	process := func(message *Message) error {
		if message.Opcode == OpText || message.Opcode == OpBinary {
			recorded <- message.ToDomain()
		}
		return nil
	}
	return process, recorded
}

// corruptDeflate is not a valid deflate stream: 0x07 starts a final block of
// the reserved type 3.
var corruptDeflate = []byte{0x07, 0x00, 0x01, 0x02}

// direction names the sender and receiver of a message in one direction.
type direction struct {
	name     string
	source   string
	sender   func(*runningRelay) net.Conn
	receiver func(*runningRelay) net.Conn
	mask     bool
}

var bothDirections = []direction{
	{
		name:     "from the client",
		source:   DirectionFromClient,
		sender:   func(r *runningRelay) net.Conn { return r.client },
		receiver: func(r *runningRelay) net.Conn { return r.upstream },
		mask:     true,
	},
	{
		name:     "from the server",
		source:   DirectionFromServer,
		sender:   func(r *runningRelay) net.Conn { return r.upstream },
		receiver: func(r *runningRelay) net.Conn { return r.client },
	},
}

// readCloses reads the next frame at the sender and at the receiver at the
// same time, since the relay may write to either first.
func readCloses(t *testing.T, relay *runningRelay, d direction) (toSender, toReceiver Frame) {
	t.Helper()
	type result struct {
		frame Frame
		err   error
	}
	read := func(peer net.Conn) <-chan result {
		out := make(chan result, 1)
		go func() {
			frame, err := ReadFrame(peer)
			out <- result{frame, err}
		}()
		return out
	}
	senderResult, receiverResult := read(d.sender(relay)), read(d.receiver(relay))
	sent, received := <-senderResult, <-receiverResult
	if sent.err != nil || received.err != nil {
		t.Fatalf("reading closes: sender %v, receiver %v", sent.err, received.err)
	}
	return sent.frame, received.frame
}

// assertStoredUninflated checks that message was stored as the compressed
// bytes received, with the inflate error containing wantError.
func assertStoredUninflated(t *testing.T, message domain.WebSocketMessage, source string, payload []byte, wantError string) {
	t.Helper()
	inflateError, _ := message.Metadata["inflate_error"].(string)
	if !bytes.Equal(message.Payload, payload) || !message.Compressed || !strings.Contains(inflateError, wantError) || message.Direction != source {
		t.Fatalf("\nwanted stored message:\n%d compressed bytes, inflate_error containing %q, from %s\ngot:\n%d bytes, compressed=%t, inflate_error %q, from %s",
			len(payload), wantError, source, len(message.Payload), message.Compressed, inflateError, message.Direction)
	}
}

func TestRelay_CorruptDeflate(t *testing.T) {
	for _, tt := range bothDirections {
		t.Run("should store, announce, and close with 1007 and 1001 "+tt.name, func(t *testing.T) {
			process, recorded := recordMessages()
			failures := make(chan InflateFailure, 1)
			relay := startRelayWith(t, process, withDeflate(contextTakeover), func(c *Connection) {
				c.SetInflateFailureHandler(func(failure InflateFailure) { failures <- failure })
			})
			writeFrames(tt.sender(relay), tt.mask, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: corruptDeflate})

			toSender, toReceiver := readCloses(t, relay, tt)
			assertCloseCode(t, CloseInvalidPayload, toSender)
			assertCloseCode(t, CloseGoingAway, toReceiver)
			assertStoredUninflated(t, receiveHeld(t, recorded), tt.source, corruptDeflate, "inflating")

			failure := <-failures
			if failure.Direction != tt.source || failure.Err == nil {
				t.Fatalf("\nwanted failure:\nfrom %s with an error\ngot:\n%+v", tt.source, failure)
			}
			if code, _ := relay.connection.CloseDetails(); code != CloseInvalidPayload {
				t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", CloseInvalidPayload, code)
			}
		})
	}
}

// deflateBomb returns a compressed message, without the sync flush tail, that
// inflates to size zero bytes.
func deflateBomb(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		t.Fatalf("creating flate writer: %v", err)
	}
	zeros := make([]byte, 1<<20)
	for written := 0; written < size; written += len(zeros) {
		if _, err := w.Write(zeros[:min(len(zeros), size-written)]); err != nil {
			t.Fatalf("compressing: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), deflateTail)
}

func TestRelay_DeflateBomb(t *testing.T) {
	const limit = 64 * 1024
	bomb := deflateBomb(t, 16*limit)

	for _, tt := range bothDirections {
		t.Run("should stop at the limit and close with 1009 and 1001 "+tt.name, func(t *testing.T) {
			process, recorded := recordMessages()
			tooBig := make(chan MessageTooBig, 1)
			relay := startRelayWith(t, process, withDeflate(contextTakeover), func(c *Connection) {
				c.SetMaxMessageSize(limit)
				c.SetMessageTooBigHandler(func(event MessageTooBig) { tooBig <- event })
			})
			writeFrames(tt.sender(relay), tt.mask, Frame{Fin: true, Rsv1: true, Opcode: OpBinary, Payload: bomb})

			toSender, toReceiver := readCloses(t, relay, tt)
			assertCloseCode(t, CloseMessageTooBig, toSender)
			assertCloseCode(t, CloseGoingAway, toReceiver)
			assertStoredUninflated(t, receiveHeld(t, recorded), tt.source, bomb, "too large")

			want := MessageTooBig{Direction: tt.source, Size: limit + 1, Limit: limit, Inflated: true}
			if got := <-tooBig; got != want {
				t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
			}
			if code, _ := relay.connection.CloseDetails(); code != CloseMessageTooBig {
				t.Fatalf("\nwanted close code:\n%d\ngot:\n%d", CloseMessageTooBig, code)
			}
		})
	}

	t.Run("should relay a message that inflates to exactly the limit", func(t *testing.T) {
		relay := startRelayWith(t, nil, withDeflate(contextTakeover), func(c *Connection) { c.SetMaxMessageSize(limit) })
		writeFrames(relay.client, true, Frame{Fin: true, Rsv1: true, Opcode: OpBinary, Payload: deflateBomb(t, limit)})

		upstream := &peerDecompressor{t: t}
		if got := upstream.decompress(readFrame(t, relay.upstream).Payload); !bytes.Equal(got, make([]byte, limit)) {
			t.Fatalf("\nwanted:\n%d zero bytes\ngot:\n%d bytes", limit, len(got))
		}
	})
}

// framesUntilEnd reads frames from peer in the background until the
// connection ends, and returns them.
func framesUntilEnd(peer net.Conn) <-chan []Frame {
	out := make(chan []Frame, 1)
	go func() {
		var frames []Frame
		for {
			frame, err := ReadFrame(peer)
			if err != nil {
				out <- frames
				return
			}
			frames = append(frames, frame)
		}
	}()
	return out
}

func TestRelay_SendsNothingAfterItsClose(t *testing.T) {
	const limit = 64 * 1024
	bomb := deflateBomb(t, 16*limit)
	causes := []struct {
		name  string
		frame Frame
	}{
		{"corrupt deflate", Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: corruptDeflate}},
		{"a deflate bomb", Frame{Fin: true, Rsv1: true, Opcode: OpBinary, Payload: bomb}},
		{"a message over the limit", Frame{Fin: true, Opcode: OpBinary, Payload: make([]byte, limit+1)}},
	}
	later := []Frame{
		{Fin: true, Opcode: OpPing, Payload: []byte("ping")},
		{Fin: true, Opcode: OpPong, Payload: []byte("pong")},
		{Fin: true, Opcode: OpText, Payload: []byte("data")},
	}
	closeReply := Frame{Fin: true, Opcode: OpClose, Payload: closePayload(CloseNormalClosure)}

	for _, cause := range causes {
		for _, tt := range bothDirections {
			t.Run("should only finish the close handshake after "+cause.name+" "+tt.name, func(t *testing.T) {
				relay := startRelayWith(t, nil, withDeflate(contextTakeover), func(c *Connection) { c.SetMaxMessageSize(limit) })
				writeFrames(tt.sender(relay), tt.mask, cause.frame)
				readCloses(t, relay, tt)

				atSender, atReceiver := framesUntilEnd(tt.sender(relay)), framesUntilEnd(tt.receiver(relay))
				fromSender := writeFrames(tt.sender(relay), tt.mask, later...)
				fromReceiver := writeFrames(tt.receiver(relay), !tt.mask, later...)
				<-fromSender
				<-fromReceiver
				<-writeFrames(relay.client, true, closeReply)
				<-writeFrames(relay.upstream, false, closeReply)

				if sender, receiver := <-atSender, <-atReceiver; len(sender) > 0 || len(receiver) > 0 {
					t.Fatalf("\nwanted after the close:\nno frames\ngot:\nsender %+v\nreceiver %+v", sender, receiver)
				}
			})
		}
	}
}

func TestRelay_RecordsAnIncompleteCompressedMessage(t *testing.T) {
	compressed := Frame{Fin: false, Rsv1: true, Opcode: OpText, Payload: corruptDeflate}
	next := Frame{Fin: true, Opcode: OpText, Payload: []byte("next")}

	type seen struct {
		message     domain.WebSocketMessage
		passthrough bool
	}
	recorded := make(chan seen, 4)
	relay := startRelayWith(t, func(message *Message) error {
		recorded <- seen{message.ToDomain(), message.Passthrough}
		return nil
	}, withDeflate(contextTakeover))
	writeFrames(relay.client, true, compressed, next)

	assertSameFrame(t, compressed, readFrame(t, relay.upstream))
	got := <-recorded
	if !got.passthrough || !got.message.Compressed || got.message.Metadata["incomplete"] != true || !bytes.Equal(got.message.Payload, corruptDeflate) {
		t.Fatalf("\nwanted recorded message:\npassthrough, compressed, incomplete, payload %x\ngot:\npassthrough %t, compressed %t, metadata %v, payload %x",
			corruptDeflate, got.passthrough, got.message.Compressed, got.message.Metadata, got.message.Payload)
	}
}

func TestRelay_ClearsRsv1WhenAnEditIsNotCompressed(t *testing.T) {
	toPing := func(message *Message) error {
		if message.Opcode == OpText {
			message.Opcode = OpPing
			message.Payload = []byte("ping")
		}
		return nil
	}
	relay := startRelayWith(t, toPing, withDeflate(contextTakeover))
	client := newPeerCompressor(t, false)
	writeFrames(relay.client, true, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: client.compress([]byte("hello"))})

	assertSameFrame(t, Frame{Fin: true, Opcode: OpPing, Payload: []byte("ping")}, readFrame(t, relay.upstream))
}
