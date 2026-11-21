package marasi

import (
	"testing"
	"time"
)

// openOriginStreams counts the origin streams the proxy's HTTP/2 client
// connections hold open.
func openOriginStreams(proxy *Proxy) int {
	engine := proxy.engine
	engine.mu.Lock()
	conns := make([]*h2Conn, 0, len(engine.h2conns))
	for h := range engine.h2conns {
		conns = append(conns, h)
	}
	engine.mu.Unlock()
	open := 0
	for _, h := range conns {
		if origin := h.originLeg(); origin != nil {
			origin.mu.Lock()
			open += len(origin.streams)
			origin.mu.Unlock()
		}
	}
	return open
}

// awaitNoOriginStreams waits until the proxy holds no origin stream open.
func awaitNoOriginStreams(t *testing.T, proxy *Proxy) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		open := openOriginStreams(proxy)
		if open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("open origin streams\nwanted:\n0\ngot:\n%d", open)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestH2Engine_StreamedPairEnd(t *testing.T) {
	t.Run("should send the client's request trailers on a streamed request to the origin once, without a second dispatch", func(t *testing.T) {
		call := startGRPCCall(t)
		call.client.data(1, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "ping"))
		call.client.headers(1, true, "x-request-trailer", "yes")

		call.upstream.await(t, "the request trailers", func(p *h2TestPeer) bool { return p.streams[call.id].ended })
		request := arrived(call.upstream, call.id)
		if value, _ := request.trailers.get("x-request-trailer"); value != "yes" || request.reset != "" {
			t.Fatalf("the origin's request trailers\nwanted:\nx-request-trailer: yes\ngot:\n%v, reset %q", request.trailers, request.reset)
		}

		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "pong"))
		call.upstream.headers(call.id, true, "grpc-status", "0")
		response := call.client.stream(t, 1)
		if status, _ := response.trailers.get("grpc-status"); status != "0" || response.reset != "" {
			t.Fatalf("the client's response\nwanted:\ngrpc-status 0\ngot:\n%v, reset %q", response.trailers, response.reset)
		}
		call.upstream.mu.Lock()
		streams := len(call.upstream.order)
		call.upstream.mu.Unlock()
		if streams != 1 {
			t.Fatalf("origin streams opened\nwanted:\n1\ngot:\n%d", streams)
		}
	})

	t.Run("should close each finished streamed origin stream on a reused origin connection", func(t *testing.T) {
		call := startGRPCCall(t)

		// Bidirectional: both directions streamed, the response ending
		// with trailers after the request ended.
		call.client.data(1, true, grpcEnvelope(0, "ping"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "ping"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "pong"))
		call.upstream.headers(call.id, true, "grpc-status", "0")
		call.client.stream(t, 1)
		awaitNoOriginStreams(t, call.proxy)

		// The response ends while the request is still open: the
		// request's carry ends with the client's stream, and only then
		// does the origin stream close.
		call.client.headers(3, false, ":method", "POST", ":scheme", "https", ":authority", "origin", ":path", "/test.Echo/PingPong",
			"content-type", "application/grpc", "te", "trailers")
		second := call.upstream.nthStream(t, 1)
		call.client.data(3, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.upstream, "the origin", second, grpcEnvelope(0, "ping"))
		call.upstream.headers(second, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(second, true, "grpc-status", "0")
		call.client.await(t, "the trailers", func(p *h2TestPeer) bool { return p.streams[3] != nil && p.streams[3].ended })
		awaitNoOriginStreams(t, call.proxy)

		// Unidirectional: a server-sent event stream whose request has
		// no body, ending on DATA with END_STREAM.
		call.client.headers(5, true, ":method", "GET", ":scheme", "https", ":authority", "origin", ":path", "/events")
		third := call.upstream.nthStream(t, 2)
		call.upstream.headers(third, false, ":status", "200", "content-type", "text/event-stream")
		call.upstream.data(third, true, "data: one\n\n")
		call.client.stream(t, 5)
		awaitNoOriginStreams(t, call.proxy)
	})
}
