package marasi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
)

// callPair waits for the call's request to be captured and returns it.
func callPair(t *testing.T, call grpcCall) domain.ProxyRequest {
	t.Helper()
	select {
	case req := <-call.captured.request:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not captured")
		return domain.ProxyRequest{}
	}
}

// pairResponse waits for the call's stored response, once its directions
// ended.
func pairResponse(t *testing.T, call grpcCall) domain.ProxyResponse {
	t.Helper()
	select {
	case res := <-call.captured.response:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
		return domain.ProxyResponse{}
	}
}

// injectOnceCarried injects into the pair's direction, retrying while the
// direction's carry has not started.
func injectOnceCarried(t *testing.T, proxy *Proxy, pair uuid.UUID, direction, payload string, options StreamInjectOptions) domain.StreamMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		message, err := proxy.InjectStreamMessage(context.Background(), pair, direction, []byte(payload), options)
		if err == nil {
			return message
		}
		if !errors.Is(err, ErrStreamDirectionNotStreamed) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestProxy_InjectStreamMessage(t *testing.T) {
	t.Run("should send an injected message to the origin between the client's messages, framed, and store it injected", func(t *testing.T) {
		call := startGRPCCall(t)
		req := callPair(t, call)
		call.client.data(1, false, grpcEnvelope(0, "one"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "one"))

		injected, err := call.proxy.InjectStreamMessage(context.Background(), req.ID, domain.StreamDirectionRequest, []byte("injected"), StreamInjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "one")+grpcEnvelope(0, "injected"))
		call.client.data(1, true, grpcEnvelope(0, "two"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "one")+grpcEnvelope(0, "injected")+grpcEnvelope(0, "two"))

		if string(injected.Payload) != "injected" || injected.Metadata["injected"] != true || injected.Direction != "request" || injected.PairID != req.ID {
			t.Fatalf("the injected message\nwanted:\n%q from the request, injected\ngot:\n%+v", "injected", injected)
		}
		call.upstream.headers(call.id, true, ":status", "200", "content-type", "application/grpc", "grpc-status", "0")
		pairResponse(t, call)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 3 || string(messages[1].Payload) != "injected" || messages[1].Metadata["injected"] != true || messages[1].ID != injected.ID {
			t.Fatalf("stored messages\nwanted:\none, injected (injected), two\ngot:\n%d %+v", len(messages), messages)
		}
	})

	t.Run("should wait for a message the client is partway through before sending an injected one", func(t *testing.T) {
		partway := make(chan struct{}, 1)
		call := startGRPCCall(t, func(proxy *Proxy) {
			proxy.OnStreamMessageProgress = func(domain.StreamMessageProgress) {
				select {
				case partway <- struct{}{}:
				default:
				}
			}
		})
		req := callPair(t, call)
		whole := grpcEnvelope(0, "client")
		call.client.data(1, false, whole[:4])
		select {
		case <-partway:
		case <-time.After(5 * time.Second):
			t.Fatal("the proxy did not read the start of the message")
		}

		done := make(chan error, 1)
		go func() {
			_, err := call.proxy.InjectStreamMessage(context.Background(), req.ID, domain.StreamDirectionRequest, []byte("injected"), StreamInjectOptions{})
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("the injection inside a message\nwanted:\nwaiting for it\ngot:\nreturned %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if s := arrived(call.upstream, call.id); len(s.data) != 0 {
			t.Fatalf("the origin's request body inside a message\nwanted:\nnothing\ngot:\n%q", s.data)
		}
		call.client.data(1, false, whole[4:])
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, whole+grpcEnvelope(0, "injected"))
	})

	t.Run("should refuse an injection into a direction that ended", func(t *testing.T) {
		call := startGRPCCall(t)
		req := callPair(t, call)
		call.client.data(1, true, grpcEnvelope(0, "last"))
		call.upstream.await(t, "the request to end", func(p *h2TestPeer) bool { return p.streams[call.id].ended })

		_, err := call.proxy.InjectStreamMessage(context.Background(), req.ID, domain.StreamDirectionRequest, []byte("late"), StreamInjectOptions{})
		if !errors.Is(err, ErrStreamDirectionEnded) {
			t.Fatalf("an injection after the request ended\nwanted:\nErrStreamDirectionEnded\ngot:\n%v", err)
		}
	})

	t.Run("should refuse an injection into a direction whose head declared a length", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		body := grpcEnvelope(0, "abc") + grpcEnvelope(0, "def")
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/Unary",
			"content-type", "application/grpc", "content-length", strconv.Itoa(len(body)))
		id := upstream.nthStream(t, 0)
		call := grpcCall{client: client, upstream: upstream, id: id, proxy: proxyOf(t, addr), captured: captured}
		req := callPair(t, call)
		client.data(1, false, body[:8])
		awaitData(t, upstream, "the origin", id, grpcEnvelope(0, "abc"))

		_, err := call.proxy.InjectStreamMessage(context.Background(), req.ID, domain.StreamDirectionRequest, []byte("x"), StreamInjectOptions{})
		if !errors.Is(err, ErrStreamLengthDeclared) {
			t.Fatalf("an injection into a declared-length request\nwanted:\nErrStreamLengthDeclared\ngot:\n%v", err)
		}
		client.data(1, true, body[8:])
		awaitData(t, upstream, "the origin", id, body)
	})

	t.Run("should refuse an injection into a pair that is not a streamed pair being carried", func(t *testing.T) {
		call := startGRPCCall(t)
		_, err := call.proxy.InjectStreamMessage(context.Background(), uuid.New(), domain.StreamDirectionRequest, []byte("x"), StreamInjectOptions{})
		if !errors.Is(err, ErrStreamedPairNotFound) {
			t.Fatalf("an injection into an unknown pair\nwanted:\nErrStreamedPairNotFound\ngot:\n%v", err)
		}
	})

	t.Run("should compress an injected message the way the direction compresses with auto, and send it plain with never", func(t *testing.T) {
		call := startCompressedCall(t, "gzip")
		req := callPair(t, call)
		injectOnceCarried(t, call.proxy, req.ID, domain.StreamDirectionRequest, "auto", StreamInjectOptions{})
		never := injectOnceCarried(t, call.proxy, req.ID, domain.StreamDirectionRequest, "never", StreamInjectOptions{NeverCompress: true})
		var data []byte
		call.upstream.await(t, "both messages", func(p *h2TestPeer) bool {
			if s := p.streams[call.id]; s != nil {
				data = bytes.Clone(s.data)
			}
			if len(data) < 5 {
				return false
			}
			first := 5 + int(binary.BigEndian.Uint32(data[1:5]))
			return len(data) == first+len(grpcEnvelope(0, "never"))
		})
		first := len(data) - len(grpcEnvelope(0, "never"))
		if data[0] != 1 {
			t.Fatalf("the auto message's flags\nwanted:\n1\ngot:\n%d", data[0])
		}
		r, err := gzip.NewReader(bytes.NewReader(data[5:first]))
		if err != nil {
			t.Fatal(err)
		}
		plain, err := io.ReadAll(r)
		if err != nil || string(plain) != "auto" {
			t.Fatalf("the auto message, gunzipped\nwanted:\n%q\ngot:\n%q %v", "auto", plain, err)
		}
		if string(data[first:]) != grpcEnvelope(0, "never") || never.Compressed || never.Flags == nil || *never.Flags != 0 {
			t.Fatalf("the never message\nwanted:\nplain, flags 0, stored uncompressed\ngot:\n%q %+v", data[first:], never)
		}
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 2 || string(messages[0].Payload) != "auto" || !messages[0].Compressed || messages[0].Metadata["encoding"] != "gzip" {
			t.Fatalf("the stored auto message\nwanted:\n%q, compressed with gzip\ngot:\n%+v", "auto", messages)
		}
	})

	t.Run("should hold an injected message in Checkpoint, and send it once forwarded", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		req := callPair(t, call)
		done := make(chan error, 1)
		go func() {
			_, err := call.proxy.InjectStreamMessage(context.Background(), req.ID, domain.StreamDirectionRequest, []byte("injected"), StreamInjectOptions{})
			done <- err
		}()
		held := awaitStreamItems(t, call.proxy, 1)[0]
		if string(held.Payload) != "injected" || held.Metadata["injected"] != true || held.Metadata["intercepted"] != true {
			t.Fatalf("the held item\nwanted:\n%q, injected, intercepted\ngot:\n%+v", "injected", held)
		}
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "injected"))
	})
}

func TestProxy_CancelStreamedPair(t *testing.T) {
	t.Run("should reset a running bidirectional call with CANCEL on both legs and record both directions cancelled", func(t *testing.T) {
		call := startGRPCCall(t)
		req := callPair(t, call)
		call.client.data(1, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "ping"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "pong"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "pong"))

		if err := call.proxy.CancelStreamedPair(req.ID); err != nil {
			t.Fatal(err)
		}
		call.client.await(t, "the client's reset", func(p *h2TestPeer) bool { return p.streams[1].reset != "" })
		call.upstream.await(t, "the origin's reset", func(p *h2TestPeer) bool { return p.streams[call.id].reset != "" })
		if client, origin := arrived(call.client, 1).reset, arrived(call.upstream, call.id).reset; client != "CANCEL" || origin != "CANCEL" {
			t.Fatalf("the resets\nwanted:\nCANCEL to the client and the origin\ngot:\n%q to the client, %q to the origin", client, origin)
		}
		res := pairResponse(t, call)
		for _, name := range []string{"request", "response"} {
			if ending := direction(t, res.Metadata, name)["ending"]; ending != "cancelled" {
				t.Errorf("the %s ending\nwanted:\ncancelled\ngot:\n%v", name, ending)
			}
		}
		if err := call.proxy.CancelStreamedPair(req.ID); !errors.Is(err, ErrStreamedPairNotFound) {
			t.Errorf("a second cancel\nwanted:\nErrStreamedPairNotFound\ngot:\n%v", err)
		}
	})

	t.Run("should abandon a held message of the pair it cancels", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		req := callPair(t, call)
		call.client.data(1, false, grpcEnvelope(0, "held"))
		awaitStreamItems(t, call.proxy, 1)
		if err := call.proxy.CancelStreamedPair(req.ID); err != nil {
			t.Fatal(err)
		}
		awaitStreamItems(t, call.proxy, 0)
		call.upstream.await(t, "the origin's reset", func(p *h2TestPeer) bool { return p.streams[call.id].reset != "" })
		if s := arrived(call.upstream, call.id); len(s.data) != 0 || s.reset != "CANCEL" {
			t.Errorf("the origin's stream\nwanted:\nno data, RST_STREAM CANCEL\ngot:\n%q, reset %q", s.data, s.reset)
		}
	})
}

func TestProxy_CancelStreamedPairWhoseOnlyDirectionEnded(t *testing.T) {
	t.Run("should reset both legs of a call whose request ended before the origin answered", func(t *testing.T) {
		call := startGRPCCall(t)
		req := callPair(t, call)
		call.client.data(1, true, grpcEnvelope(0, "only"))
		call.upstream.await(t, "the request to end", func(p *h2TestPeer) bool { return p.streams[call.id].ended })

		if err := call.proxy.CancelStreamedPair(req.ID); err != nil {
			t.Fatal(err)
		}
		call.client.await(t, "the client's reset", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].reset != "" })
		call.upstream.await(t, "the origin's reset", func(p *h2TestPeer) bool { return p.streams[call.id].reset != "" })
		if client, origin := arrived(call.client, 1).reset, arrived(call.upstream, call.id).reset; client != "CANCEL" || origin != "CANCEL" {
			t.Fatalf("the resets\nwanted:\nCANCEL to the client and the origin\ngot:\n%q to the client, %q to the origin", client, origin)
		}
	})
}
