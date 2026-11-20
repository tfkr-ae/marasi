package marasi

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
)

// holdClientMessages is a checkpoint extension that holds every stream
// message from the client.
const holdClientMessages = `
	function interceptStreamMessage(message)
		return message:direction() == "request"
	end
`

// awaitStreamItems waits until n stream items are held, and returns them
// oldest first.
func awaitStreamItems(t *testing.T, proxy *Proxy, n int) []domain.CheckpointItem {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var items []domain.CheckpointItem
		for _, item := range proxy.CheckpointItems() {
			if item.Type == domain.CheckpointTypeStream {
				items = append(items, item)
			}
		}
		if len(items) == n {
			return items
		}
		if time.Now().After(deadline) {
			t.Fatalf("held stream items\nwanted:\n%d\ngot:\n%d %+v", n, len(items), items)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestH2Engine_CheckpointStreamMessages(t *testing.T) {
	t.Run("should hold a client message while the origin's messages reach the client, and send the client's later messages after it once forwarded", func(t *testing.T) {
		call := startGRPCCall(t, withStreamExtension(t, "checkpoint", holdClientMessages))
		call.client.data(1, false, grpcEnvelope(0, "one"))
		held := awaitStreamItems(t, call.proxy, 1)[0]
		if string(held.Payload) != "one" || held.Direction != "request" || held.Framing != "grpc" || held.PairID == [16]byte{} ||
			held.Flags == nil || *held.Flags != 0 || held.Compressed || held.Metadata["intercepted"] != true {
			t.Fatalf("the held item\nwanted:\n%q from the request, grpc, flags 0, the pair id, intercepted\ngot:\n%+v", "one", held)
		}
		call.client.data(1, false, grpcEnvelope(0, "two"))

		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "reply"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "reply"))
		if s := arrived(call.upstream, call.id); len(s.data) != 0 {
			t.Fatalf("the origin's request body while held\nwanted:\nnothing\ngot:\n%q", s.data)
		}

		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		second := awaitStreamItems(t, call.proxy, 1)[0]
		if string(second.Payload) != "two" {
			t.Fatalf("the second held item\nwanted:\n%q\ngot:\n%q", "two", second.Payload)
		}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "one"))
		if err := call.proxy.ForwardCheckpoint(second.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "one")+grpcEnvelope(0, "two"))
	})

	t.Run("should send an edited payload in a new envelope and store it edited, and store a dropped message dropped without sending it", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, false, grpcEnvelope(0, "secret"))
		dropped := awaitStreamItems(t, call.proxy, 1)[0]
		if err := call.proxy.DropCheckpoint(dropped.ID); err != nil {
			t.Fatal(err)
		}
		call.client.data(1, true, grpcEnvelope(0, "hello"))
		edited := awaitStreamItems(t, call.proxy, 1)[0]
		payload := []byte("hello, edited")
		if err := call.proxy.ForwardCheckpoint(edited.ID, CheckpointForward{Payload: &payload}); err != nil {
			t.Fatal(err)
		}
		if request := call.upstream.stream(t, call.id); string(request.data) != grpcEnvelope(0, "hello, edited") {
			t.Fatalf("the origin's request body\nwanted:\nonly the edited message\ngot:\n%q", request.data)
		}
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "reply"))
		reply := awaitStreamItems(t, call.proxy, 1)[0]
		if err := call.proxy.ForwardCheckpoint(reply.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		call.upstream.headers(call.id, true, "grpc-status", "0")

		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 3 {
			t.Fatalf("stored messages\nwanted:\n3\ngot:\n%d", len(messages))
		}
		first, second, third := messages[0], messages[1], messages[2]
		if string(first.Payload) != "secret" || first.Metadata["dropped"] != true || first.Metadata["intercepted"] != true {
			t.Errorf("the dropped message\nwanted:\n%q intercepted, dropped\ngot:\n%q %v", "secret", first.Payload, first.Metadata)
		}
		if string(second.Payload) != "hello, edited" || second.Metadata["edited"] != true || second.Metadata["intercepted"] != true {
			t.Errorf("the edited message\nwanted:\n%q intercepted, edited\ngot:\n%q %v", "hello, edited", second.Payload, second.Metadata)
		}
		if string(third.Payload) != "reply" || third.Metadata["edited"] != nil || third.Metadata["intercepted"] != true {
			t.Errorf("the forwarded reply\nwanted:\n%q intercepted, not edited\ngot:\n%q %v", "reply", third.Payload, third.Metadata)
		}
	})

	t.Run("should drop a held message as abandoned when the client cancels the call", func(t *testing.T) {
		var mu sync.Mutex
		var abandoned []domain.CheckpointItem
		call := startGRPCCall(t, func(proxy *Proxy) {
			proxy.SetStreamIntercept(true)
			proxy.OnCheckpointAbandoned = func(item domain.CheckpointItem) error {
				mu.Lock()
				defer mu.Unlock()
				abandoned = append(abandoned, item)
				return nil
			}
		})
		call.client.data(1, false, grpcEnvelope(0, "held"))
		held := awaitStreamItems(t, call.proxy, 1)[0]
		call.client.write(func(f *http2.Framer) error { return f.WriteRSTStream(1, http2.ErrCodeCancel) })

		call.upstream.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[call.id].reset != "" })
		awaitStreamItems(t, call.proxy, 0)
		mu.Lock()
		if len(abandoned) != 1 || abandoned[0].ID != held.ID {
			t.Errorf("abandoned items\nwanted:\nthe held message\ngot:\n%+v", abandoned)
		}
		mu.Unlock()
		if s := arrived(call.upstream, call.id); len(s.data) != 0 || s.reset != "CANCEL" {
			t.Errorf("the origin's stream\nwanted:\nno data, RST_STREAM CANCEL\ngot:\n%q, reset %q", s.data, s.reset)
		}
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{}); !errors.Is(err, ErrCheckpointNotFound) {
			t.Errorf("forwarding the abandoned item\nwanted:\nErrCheckpointNotFound\ngot:\n%v", err)
		}
	})

	t.Run("should drop a held origin message as abandoned when the origin resets the call", func(t *testing.T) {
		var mu sync.Mutex
		var abandoned int
		call := startGRPCCall(t, func(proxy *Proxy) {
			proxy.SetStreamIntercept(true)
			proxy.OnCheckpointAbandoned = func(domain.CheckpointItem) error {
				mu.Lock()
				defer mu.Unlock()
				abandoned++
				return nil
			}
		})
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "held"))
		awaitStreamItems(t, call.proxy, 1)
		call.upstream.write(func(f *http2.Framer) error { return f.WriteRSTStream(call.id, http2.ErrCodeInternal) })

		awaitStreamItems(t, call.proxy, 0)
		call.client.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].reset != "" })
		mu.Lock()
		defer mu.Unlock()
		if abandoned != 1 {
			t.Errorf("abandoned items\nwanted:\n1\ngot:\n%d", abandoned)
		}
	})

	t.Run("should refuse an edit that changes the length, and a drop, on a direction whose head declared a length, keeping the message held", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		body := grpcEnvelope(0, "abc")
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/Unary",
			"content-type", "application/grpc", "content-length", strconv.Itoa(len(body)))
		id := upstream.nthStream(t, 0)
		client.data(1, true, body)
		proxy := proxyOf(t, addr)
		held := awaitStreamItems(t, proxy, 1)[0]

		longer := []byte("abcdef")
		if err := proxy.ForwardCheckpoint(held.ID, CheckpointForward{Payload: &longer}); !errors.Is(err, ErrStreamLengthDeclared) {
			t.Fatalf("a length-changing edit\nwanted:\nErrStreamLengthDeclared\ngot:\n%v", err)
		}
		if err := proxy.DropCheckpoint(held.ID); !errors.Is(err, ErrStreamLengthDeclared) {
			t.Fatalf("a drop\nwanted:\nErrStreamLengthDeclared\ngot:\n%v", err)
		}
		same := []byte("xyz")
		if err := proxy.ForwardCheckpoint(held.ID, CheckpointForward{Payload: &same}); err != nil {
			t.Fatalf("an edit that keeps the length\nwanted:\nforwarded\ngot:\n%v", err)
		}
		if request := upstream.stream(t, id); string(request.data) != grpcEnvelope(0, "xyz") {
			t.Fatalf("the origin's request body\nwanted:\n%q\ngot:\n%q", grpcEnvelope(0, "xyz"), request.data)
		}
	})

	t.Run("should hold nothing when the checkpoint extension is disabled, whatever the switch", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) {
			proxy.SetStreamIntercept(true)
			ext := &domain.Extension{ID: uuid.New(), Name: "checkpoint", LuaContent: holdClientMessages}
			if err := proxy.WithOptions(WithExtension(ext)); err != nil {
				t.Fatal(err)
			}
		})
		call.client.data(1, true, grpcEnvelope(0, "free"))
		if request := call.upstream.stream(t, call.id); string(request.data) != grpcEnvelope(0, "free") {
			t.Fatalf("the origin's request body\nwanted:\nthe message, not held\ngot:\n%q", request.data)
		}
	})

	t.Run("should hold a gzip message decoded, forward it unedited as its original bytes, and send an edit uncompressed", func(t *testing.T) {
		call := startCompressedCall(t, "gzip", func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		sent := grpcEnvelope(1, gzipped(t, "hello"))
		call.client.data(1, false, sent)
		held := awaitStreamItems(t, call.proxy, 1)[0]
		if string(held.Payload) != "hello" || !held.Compressed || held.Flags == nil || *held.Flags != 1 {
			t.Fatalf("the held item\nwanted:\n%q decoded, compressed, flags 1\ngot:\n%+v", "hello", held)
		}
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, sent)

		call.client.data(1, true, grpcEnvelope(1, gzipped(t, "again")))
		edited := awaitStreamItems(t, call.proxy, 1)[0]
		payload := []byte("AGAIN")
		if err := call.proxy.ForwardCheckpoint(edited.ID, CheckpointForward{Payload: &payload}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, sent+grpcEnvelope(0, "AGAIN"))
	})

	t.Run("should refuse an edit of a message in an encoding it does not decode, keeping it held", func(t *testing.T) {
		call := startCompressedCall(t, "snappy", func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		sent := grpcEnvelope(1, "opaque")
		call.client.data(1, true, sent)
		held := awaitStreamItems(t, call.proxy, 1)[0]
		if held.HasText {
			t.Errorf("the held item's text\nwanted:\nnone\ngot:\n%q", held.Text)
		}
		payload := []byte("edited")
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{Payload: &payload}); !errors.Is(err, ErrStreamReadOnly) {
			t.Fatalf("an edit\nwanted:\nErrStreamReadOnly\ngot:\n%v", err)
		}
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, sent)
	})
}
