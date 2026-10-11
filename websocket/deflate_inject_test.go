package websocket

import (
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

func TestRelay_DeflateDropsAndInjections(t *testing.T) {
	sent := []string{
		"the quick brown fox jumps over the lazy dog",
		"the quick brown fox jumps over the lazy dog, dropped",
		"the quick brown fox jumps over the lazy dog, kept",
	}

	t.Run("should deliver every message after a dropped one", func(t *testing.T) {
		relay := startRelayWith(t, func(message *Message) error {
			message.Dropped = string(message.Payload) == sent[1]
			return nil
		}, withDeflate(contextTakeover))
		server := newPeerCompressor(t, false)
		client := &peerDecompressor{t: t}

		writeFrames(relay.upstream, false,
			Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: server.compress([]byte(sent[0]))},
			Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: server.compress([]byte(sent[1]))},
			Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: server.compress([]byte(sent[2]))},
		)
		for _, want := range []string{sent[0], sent[2]} {
			if got := client.decompress(readFrame(t, relay.client).Payload); string(got) != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
			}
		}
	})

	t.Run("should compress an injected message with the leg's compressor", func(t *testing.T) {
		relay := startRelayWith(t, nil, withDeflate(contextTakeover))
		client := newPeerCompressor(t, false)
		upstream := &peerDecompressor{t: t}

		injected := make(chan domain.WebSocketMessage, 1)
		go func() {
			message, err := relay.connection.Inject(DirectionFromClient, OpText, []byte(sent[0]), InjectOptions{})
			if err != nil {
				t.Errorf("injecting: %v", err)
			}
			injected <- message
		}()
		frame := readFrame(t, relay.upstream)
		if got := upstream.decompress(frame.Payload); !frame.Rsv1 || string(got) != sent[0] {
			t.Fatalf("\nwanted injected message:\n%q compressed\ngot:\n%q (RSV1 %v)", sent[0], got, frame.Rsv1)
		}
		if message := <-injected; !message.Compressed {
			t.Fatalf("\nwanted stored injection:\ncompressed\ngot:\nuncompressed")
		}

		writeFrames(relay.client, true, Frame{Fin: true, Rsv1: true, Opcode: OpText, Payload: client.compress([]byte(sent[2]))})
		if got := upstream.decompress(readFrame(t, relay.upstream).Payload); string(got) != sent[2] {
			t.Fatalf("\nwanted message after the injection:\n%q\ngot:\n%q", sent[2], got)
		}
	})

	t.Run("should send an injected message uncompressed when compress is never", func(t *testing.T) {
		relay := startRelayWith(t, nil, withDeflate(contextTakeover))

		injected := make(chan domain.WebSocketMessage, 1)
		go func() {
			message, err := relay.connection.Inject(DirectionFromServer, OpText, []byte("plain"), InjectOptions{NeverCompress: true})
			if err != nil {
				t.Errorf("injecting: %v", err)
			}
			injected <- message
		}()
		assertSameFrame(t, Frame{Fin: true, Opcode: OpText, Payload: []byte("plain")}, readFrame(t, relay.client))
		if message := <-injected; message.Compressed {
			t.Fatalf("\nwanted stored injection:\nuncompressed\ngot:\ncompressed")
		}
	})

	t.Run("should send an injected message uncompressed without deflate", func(t *testing.T) {
		relay := startRelay(t)

		go func() { _, _ = relay.connection.Inject(DirectionFromServer, OpText, []byte("plain"), InjectOptions{}) }()
		assertSameFrame(t, Frame{Fin: true, Opcode: OpText, Payload: []byte("plain")}, readFrame(t, relay.client))
	})
}

func TestRelay_StoresWhetherAnInjectionWentCompressed(t *testing.T) {
	smallClientWindow := domain.WebSocketDeflate{ServerMaxWindowBits: 10, ClientMaxWindowBits: 15}
	toPing := func(message *Message) error {
		message.Opcode = OpPing
		return nil
	}
	tests := []struct {
		name    string
		process ProcessFunc
		params  domain.WebSocketDeflate
		payload string
		want    Frame
	}{
		{
			name:    "should store uncompressed when Huffman-only is not smaller",
			params:  smallClientWindow,
			payload: "a",
			want:    Frame{Fin: true, Opcode: OpText, Payload: []byte("a")},
		},
		{
			name:    "should store uncompressed when a hook makes it a control frame",
			process: toPing,
			params:  contextTakeover,
			payload: "ping",
			want:    Frame{Fin: true, Opcode: OpPing, Payload: []byte("ping")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := make(chan domain.WebSocketMessage, 1)
			process := func(message *Message) error {
				if tt.process != nil {
					if err := tt.process(message); err != nil {
						return err
					}
				}
				stored <- message.ToDomain()
				return nil
			}
			relay := startRelayWith(t, process, withDeflate(tt.params))

			returned := make(chan domain.WebSocketMessage, 1)
			go func() {
				message, err := relay.connection.Inject(DirectionFromServer, OpText, []byte(tt.payload), InjectOptions{})
				if err != nil {
					t.Errorf("injecting: %v", err)
				}
				returned <- message
			}()
			assertSameFrame(t, tt.want, readFrame(t, relay.client))
			if got := <-stored; got.Compressed {
				t.Fatalf("\nwanted stored:\nuncompressed\ngot:\ncompressed")
			}
			if got := <-returned; got.Compressed {
				t.Fatalf("\nwanted returned:\nuncompressed\ngot:\ncompressed")
			}
		})
	}
}
