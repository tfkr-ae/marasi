package marasi

import (
	"bytes"
	"compress/zlib"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// startCompressedCall starts a gRPC call whose request messages are
// compressed with encoding (grpc-encoding).
func startCompressedCall(t *testing.T, encoding string, configure ...func(*Proxy)) grpcCall {
	t.Helper()
	origin := newH2TestOrigin(t)
	addr, captured, ca := serveH1EngineMITMProxy(t, true, configure...)
	client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
	upstream := origin.accept(t)
	client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/PingPong",
		"content-type", "application/grpc", "te", "trailers", "grpc-encoding", encoding, "grpc-accept-encoding", "gzip")
	return grpcCall{client: client, upstream: upstream, id: upstream.nthStream(t, 0), proxy: proxyOf(t, addr), captured: captured}
}

func TestH2Engine_CompressedStreamMessages(t *testing.T) {
	t.Run("should forward a gzip message as its original bytes and store it decompressed, marked compressed", func(t *testing.T) {
		call := startCompressedCall(t, "gzip")
		sent := grpcEnvelope(1, gzipped(t, "hello"))
		call.client.data(1, true, sent)
		awaitData(t, call.upstream, "the origin", call.id, sent)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0")

		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 1 || string(messages[0].Payload) != "hello" || !messages[0].Compressed || *messages[0].Flags != 1 {
			t.Fatalf("stored messages\nwanted:\n%q, compressed, flags 1\ngot:\n%d %+v", "hello", len(messages), messages)
		}
	})
	t.Run("should show a hook the decompressed payload and send its edit uncompressed with the flag cleared", func(t *testing.T) {
		call := startCompressedCall(t, "gzip", withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				if message:direction() == "request" and message:compressed() then
					message:set_payload(message:payload() .. " (edited)")
				end
			end
		`))
		call.client.data(1, true, grpcEnvelope(1, gzipped(t, "hello")))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "hello (edited)"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0")

		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 1 || string(messages[0].Payload) != "hello (edited)" || messages[0].Compressed || *messages[0].Flags != 0 || messages[0].Metadata["edited"] != true {
			t.Fatalf("stored messages\nwanted:\n%q, edited, not compressed, flags 0\ngot:\n%d %+v", "hello (edited)", len(messages), messages)
		}
	})

	t.Run("should decode a deflate message", func(t *testing.T) {
		call := startCompressedCall(t, "deflate")
		var b bytes.Buffer
		w := zlib.NewWriter(&b)
		w.Write([]byte("hello"))
		w.Close()
		sent := grpcEnvelope(1, b.String())
		call.client.data(1, true, sent)
		awaitData(t, call.upstream, "the origin", call.id, sent)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0")

		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 1 || string(messages[0].Payload) != "hello" || !messages[0].Compressed || messages[0].Metadata["encoding"] != "deflate" {
			t.Fatalf("stored messages\nwanted:\n%q, compressed, encoding deflate\ngot:\n%d %+v", "hello", len(messages), messages)
		}
	})
	t.Run("should pass a message in an encoding it does not decode through byte for byte and refuse an edit to it", func(t *testing.T) {
		logs := &proxyLogs{}
		call := startCompressedCall(t, "snappy", logs.record, withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				if message:direction() == "request" then
					message:set_metadata({ editable = message:editable(), encoding = message:encoding() })
					message:set_payload("edited")
				end
			end
		`))
		sent := grpcEnvelope(1, "\x05\x10hello")
		call.client.data(1, true, sent)
		awaitData(t, call.upstream, "the origin", call.id, sent)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0")

		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 1 || string(messages[0].Payload) != "\x05\x10hello" || !messages[0].Compressed || messages[0].Metadata["edited"] != nil {
			t.Fatalf("stored messages\nwanted:\nthe raw payload, compressed, not edited\ngot:\n%d %+v", len(messages), messages)
		}
		if seen, _ := messages[0].Metadata["rewriter"].(map[string]any); seen["editable"] != false || seen["encoding"] != "snappy" {
			t.Errorf("what the hook saw\nwanted:\nnot editable, snappy\ngot:\n%v", seen)
		}
		if !logs.find("WARN", "cannot be edited") {
			t.Errorf("proxy logs\nwanted:\na WARN for the refused edit\ngot:\n%+v", logs.entries)
		}
	})

	t.Run("should store a message that fails to decompress with its error, reset the stream and log it", func(t *testing.T) {
		logs := &proxyLogs{}
		call := startGRPCCall(t, logs.record)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc", "grpc-encoding", "gzip")
		call.upstream.data(call.id, false, grpcEnvelope(1, "not gzip"))

		call.client.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].reset != "" })
		call.upstream.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[call.id].reset != "" })
		if got := arrived(call.client, 1); len(got.data) != 0 || got.reset != "INTERNAL_ERROR" {
			t.Fatalf("the client's stream\nwanted:\nno data, then RST_STREAM INTERNAL_ERROR\ngot:\n%q, %q", got.data, got.reset)
		}
		req, res := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 1 || string(messages[0].Payload) != "not gzip" || !messages[0].Compressed || messages[0].Metadata["inflate_error"] == nil {
			t.Fatalf("stored messages\nwanted:\nthe compressed bytes with inflate_error\ngot:\n%d %+v", len(messages), messages)
		}
		if d := direction(t, res.Metadata, "response"); d["ending"] != "reset" || d["reset_code"] != "INTERNAL_ERROR" {
			t.Errorf("metadata.streamed.response\nwanted:\nreset INTERNAL_ERROR\ngot:\n%v", d)
		}
		if !logs.find("WARN", "could not be decoded") {
			t.Errorf("proxy logs\nwanted:\na WARN naming the decode failure\ngot:\n%+v", logs.entries)
		}
	})
	t.Run("should decode a Connect message compressed with its connect-content-encoding", func(t *testing.T) {
		call := startGRPCCall(t)
		call.client.data(1, true, grpcEnvelope(0, "ping"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/connect+proto", "connect-content-encoding", "gzip")
		sent := grpcEnvelope(1, gzipped(t, "pong"))
		call.upstream.data(call.id, true, sent)
		awaitData(t, call.client, "the client", 1, sent)

		req, _ := streamedResponse(t, call.captured)
		var reply *domain.StreamMessage
		for _, m := range storedMessages(t, call.proxy, req.ID) {
			if m.Direction == "response" {
				reply = m
			}
		}
		if reply == nil || string(reply.Payload) != "pong" || !reply.Compressed || reply.Framing != "connect" {
			t.Fatalf("the stored reply\nwanted:\n%q, compressed, connect\ngot:\n%+v", "pong", reply)
		}
	})
}
