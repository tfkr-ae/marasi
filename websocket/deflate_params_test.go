package websocket

import (
	"bytes"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

func TestRelay_DeflateFollowsEachSidesParameters(t *testing.T) {
	sent := []string{
		"abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabc",
		"abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabc",
		"abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabc and more",
	}

	tests := []struct {
		name   string
		params domain.WebSocketDeflate
	}{
		{name: "context takeover", params: contextTakeover},
		{name: "no context takeover on either side", params: domain.WebSocketDeflate{
			ServerNoContextTakeover: true, ClientNoContextTakeover: true, ServerMaxWindowBits: 15, ClientMaxWindowBits: 15,
		}},
		{name: "client context takeover only", params: domain.WebSocketDeflate{
			ServerNoContextTakeover: true, ServerMaxWindowBits: 15, ClientMaxWindowBits: 15,
		}},
	}

	for _, tt := range tests {
		t.Run("should deliver every message both ways with "+tt.name, func(t *testing.T) {
			relay := startRelayWith(t, nil, withDeflate(tt.params))
			client := newPeerCompressor(t, tt.params.ClientNoContextTakeover)
			server := newPeerCompressor(t, tt.params.ServerNoContextTakeover)
			atUpstream := &peerDecompressor{t: t, noContextTakeover: tt.params.ClientNoContextTakeover}
			atClient := &peerDecompressor{t: t, noContextTakeover: tt.params.ServerNoContextTakeover}

			for i, text := range sent {
				writeFrames(relay.client, true, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: client.compress([]byte(text))})
				if got := atUpstream.decompress(readFrame(t, relay.upstream).Payload); string(got) != text {
					t.Fatalf("\nwanted client message %d:\n%q\ngot:\n%q", i, text, got)
				}
				writeFrames(relay.upstream, false, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: server.compress([]byte(text))})
				if got := atClient.decompress(readFrame(t, relay.client).Payload); string(got) != text {
					t.Fatalf("\nwanted server message %d:\n%q\ngot:\n%q", i, text, got)
				}
			}
		})
	}

	t.Run("should inflate a message fragmented over several frames", func(t *testing.T) {
		recorded := make(chan *Message, 1)
		relay := startRelayWith(t, func(message *Message) error {
			recorded <- message
			return nil
		}, withDeflate(contextTakeover))
		compressed := newPeerCompressor(t, false).compress([]byte(sent[2]))
		writeFrames(relay.client, true,
			Frame{Fin: false, Rsv1: true, Opcode: OpText, Payload: compressed[:3]},
			Frame{Fin: false, Opcode: OpContinuation, Payload: compressed[3:7]},
			Frame{Fin: true, Opcode: OpContinuation, Payload: compressed[7:]},
		)

		frame := readFrame(t, relay.upstream)
		if got := (&peerDecompressor{t: t}).decompress(frame.Payload); string(got) != sent[2] || !frame.Fin {
			t.Fatalf("\nwanted one final frame:\n%q\ngot:\n%q (fin %v)", sent[2], got, frame.Fin)
		}
		message := <-recorded
		if string(message.Payload) != sent[2] || !message.Compressed || message.FrameCount != 3 {
			t.Fatalf("\nwanted recorded message:\n%q compressed, 3 frames\ngot:\n%q compressed %v, %d frames",
				sent[2], message.Payload, message.Compressed, message.FrameCount)
		}
	})

	t.Run("should forward an uncompressed message on a deflate connection unchanged", func(t *testing.T) {
		recorded := make(chan *Message, 1)
		relay := startRelayWith(t, func(message *Message) error {
			recorded <- message
			return nil
		}, withDeflate(contextTakeover))
		frame := Frame{Fin: true, Opcode: OpText, Payload: []byte("plain")}

		assertSameFrame(t, frame, relay.relayFromClient(t, frame))
		if message := <-recorded; message.Compressed || string(message.Payload) != "plain" {
			t.Fatalf("\nwanted recorded message:\n%q uncompressed\ngot:\n%q compressed %v", "plain", message.Payload, message.Compressed)
		}
	})
}

func TestRelay_DeflateTowardASmallWindow(t *testing.T) {
	small := domain.WebSocketDeflate{ServerMaxWindowBits: 9, ClientMaxWindowBits: 15}
	text := bytes.Repeat([]byte("aaaaaaaabbbbcc"), 100)

	t.Run("should compress each message on its own with no back references", func(t *testing.T) {
		relay := startRelayWith(t, nil, withDeflate(small))
		server := newPeerCompressor(t, false)

		for i := range 3 {
			writeFrames(relay.upstream, false, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: server.compress(text)})
			frame := readFrame(t, relay.client)
			if !frame.Rsv1 || len(frame.Payload) >= len(text) {
				t.Fatalf("\nwanted message %d:\ncompressed below %d bytes\ngot:\nRSV1 %v, %d bytes", i, len(text), frame.Rsv1, len(frame.Payload))
			}
			if got := (&peerDecompressor{t: t}).decompress(frame.Payload); !bytes.Equal(got, text) {
				t.Fatalf("\nwanted message %d to inflate on its own:\n%q\ngot:\n%q", i, text, got)
			}
			// Huffman-only output of this text is far larger than with
			// back references, which the peer's window may not hold.
			if len(frame.Payload) < len(text)/8 {
				t.Fatalf("\nwanted message %d:\nHuffman-only\ngot:\n%d bytes, which needs back references", i, len(frame.Payload))
			}
		}
	})

	t.Run("should send a message uncompressed when compressing does not make it smaller", func(t *testing.T) {
		relay := startRelayWith(t, nil, withDeflate(small))
		random := make([]byte, 256)
		for i := range random {
			random[i] = byte(i)
		}
		writeFrames(relay.upstream, false, Frame{Fin: true, Rsv1: true, Opcode: OpBinary, Payload: newPeerCompressor(t, false).compress(random)})

		assertSameFrame(t, Frame{Fin: true, Opcode: OpBinary, Payload: random}, readFrame(t, relay.client))
	})
}
