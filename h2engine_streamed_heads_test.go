package marasi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

// startGRPCWebCall is startGRPCCall for a gRPC-Web call.
func startGRPCWebCall(t *testing.T, configure ...func(*Proxy)) grpcCall {
	t.Helper()
	origin := newH2TestOrigin(t)
	addr, captured, ca := serveH1EngineMITMProxy(t, true, configure...)
	client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
	upstream := origin.accept(t)
	client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/Say",
		"content-type", "application/grpc-web+proto")
	return grpcCall{client: client, upstream: upstream, id: upstream.nthStream(t, 0), proxy: proxyOf(t, addr), captured: captured}
}

// awaitItems waits until n items of kind are held, and returns them oldest
// first.
func awaitItems(t *testing.T, proxy *Proxy, kind string, n int) []domain.CheckpointItem {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var items []domain.CheckpointItem
		for _, item := range proxy.CheckpointItems() {
			if item.Type == kind {
				items = append(items, item)
			}
		}
		if len(items) == n {
			return items
		}
		if time.Now().After(deadline) {
			t.Fatalf("held %s items\nwanted:\n%d\ngot:\n%d %+v", kind, n, len(items), items)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestH2Engine_CheckpointTrailers(t *testing.T) {
	t.Run("should hold a streamed response's trailers as a trailer item, and send and store an edited grpc-status", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, true, "")
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0", "grpc-message", "")

		held := awaitItems(t, call.proxy, domain.CheckpointTypeTrailer, 1)[0]
		if string(held.Raw) != "grpc-status: 0\r\ngrpc-message: \r\n" || held.Direction != "response" || held.Framing != "grpc" || held.PairID == [16]byte{} {
			t.Fatalf("the held trailer item\nwanted:\nthe trailer block of the response, grpc, the pair id\ngot:\n%+v %q", held, held.Raw)
		}
		if s := arrived(call.client, 1); s.ended {
			t.Fatalf("the client's stream while the trailers are held\nwanted:\nnot ended\ngot:\nended, trailers %v", s.trailers)
		}
		edit := []byte("grpc-status: 7\r\ngrpc-message: denied\r\n")
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{Raw: &edit}); err != nil {
			t.Fatal(err)
		}
		call.client.await(t, "the trailers", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].ended })
		response := arrived(call.client, 1)
		if status, _ := response.trailers.get("grpc-status"); status != "7" {
			t.Fatalf("the client's trailers\nwanted:\ngrpc-status 7\ngot:\n%v", response.trailers)
		}
		if message, _ := response.trailers.get("grpc-message"); message != "denied" {
			t.Fatalf("the client's trailers\nwanted:\ngrpc-message denied\ngot:\n%v", response.trailers)
		}

		_, res := streamedResponse(t, call.captured)
		trailers, _ := direction(t, res.Metadata, "response")["trailers"].([]any)
		if len(trailers) != 2 || trailers[0].(map[string]any)["value"] != "7" {
			t.Errorf("the stored trailers\nwanted:\ngrpc-status 7 first\ngot:\n%v", trailers)
		}
	})

	t.Run("should refuse a trailer edit with a line that is not a field, keeping it held", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, true, "")
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0")

		held := awaitItems(t, call.proxy, domain.CheckpointTypeTrailer, 1)[0]
		edit := []byte("grpc-status 7\r\n")
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{Raw: &edit}); !errors.Is(err, ErrTrailerEdit) {
			t.Fatalf("an edit that does not parse\nwanted:\nErrTrailerEdit\ngot:\n%v", err)
		}
		awaitItems(t, call.proxy, domain.CheckpointTypeTrailer, 1)
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		call.client.await(t, "the trailers", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].ended })
		if status, _ := arrived(call.client, 1).trailers.get("grpc-status"); status != "0" {
			t.Fatalf("the client's trailers\nwanted:\ngrpc-status 0, unedited\ngot:\n%v", arrived(call.client, 1).trailers)
		}
	})

	t.Run("should end the response without trailers when a held trailer item is dropped", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, true, "")
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0")

		held := awaitItems(t, call.proxy, domain.CheckpointTypeTrailer, 1)[0]
		if err := call.proxy.DropCheckpoint(held.ID); err != nil {
			t.Fatal(err)
		}
		call.client.await(t, "the end", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].ended })
		if s := arrived(call.client, 1); len(s.trailers) != 0 || s.reset != "" {
			t.Fatalf("the client's stream\nwanted:\nended without trailers\ngot:\ntrailers %v, reset %q", s.trailers, s.reset)
		}
	})

	t.Run("should hold a gRPC-Web trailers frame as a trailer item and send an edit in a trailers frame", func(t *testing.T) {
		call := startGRPCWebCall(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		call.client.data(1, true, "")
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc-web+proto")
		call.upstream.data(call.id, true, grpcEnvelope(0x80, "grpc-status: 0\r\n"))

		held := awaitItems(t, call.proxy, domain.CheckpointTypeTrailer, 1)[0]
		if string(held.Raw) != "grpc-status: 0\r\n" || held.Framing != "grpc-web" || held.Direction != "response" {
			t.Fatalf("the held trailer item\nwanted:\nthe trailers frame's block, grpc-web, response\ngot:\n%+v %q", held, held.Raw)
		}
		edit := []byte("grpc-status: 13\r\n")
		if err := call.proxy.ForwardCheckpoint(held.ID, CheckpointForward{Raw: &edit}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0x80, "grpc-status: 13\r\n"))

		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 1 || string(messages[0].Payload) != "grpc-status: 13\r\n" || messages[0].Metadata["edited"] != true {
			t.Fatalf("the stored trailers frame\nwanted:\n%q edited\ngot:\n%+v", "grpc-status: 13\r\n", messages)
		}
	})
}

func TestH2Engine_CheckpointStreamedHeads(t *testing.T) {
	t.Run("should hold a streamed pair's heads with an empty body, a held response head holding the origin's messages while the client's flow", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, holdEverything(t))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/PingPong",
			"content-type", "application/grpc", "te", "trailers")
		proxy := proxyOf(t, addr)
		request := awaitItems(t, proxy, domain.CheckpointTypeRequest, 1)[0]
		if !strings.HasSuffix(string(request.Raw), "\r\n\r\n") {
			t.Fatalf("the held request head\nwanted:\nan empty body\ngot:\n%q", request.Raw)
		}
		client.data(1, false, grpcEnvelope(0, "early"))
		if err := proxy.ForwardCheckpoint(request.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		call := grpcCall{client: client, upstream: upstream, id: upstream.nthStream(t, 0), proxy: proxy}
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "early"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "reply"))
		response := awaitItems(t, call.proxy, domain.CheckpointTypeResponse, 1)[0]
		if !strings.HasSuffix(string(response.Raw), "\r\n\r\n") {
			t.Fatalf("the held response head\nwanted:\nan empty body\ngot:\n%q", response.Raw)
		}

		call.client.data(1, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "early")+grpcEnvelope(0, "ping"))
		if s := arrived(call.client, 1); len(s.headers) != 0 || len(s.data) != 0 {
			t.Fatalf("the client's stream while the response head is held\nwanted:\nnothing\ngot:\n%d heads, %q", len(s.headers), s.data)
		}

		if err := call.proxy.ForwardCheckpoint(response.ID, CheckpointForward{}); err != nil {
			t.Fatal(err)
		}
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "reply"))
	})
}
