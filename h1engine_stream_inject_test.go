package marasi

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

// h1PairOf waits for the request of the one pair the HTTP/1.1 proxy carries.
func h1PairOf(t *testing.T, captured *capturedPair) domain.ProxyRequest {
	t.Helper()
	select {
	case req := <-captured.request:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not captured")
		return domain.ProxyRequest{}
	}
}

func TestH1Engine_InjectAndCancel(t *testing.T) {
	const reqHead = "POST http://%s/test.Echo/PingPong HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"
	const resHead = "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"

	t.Run("should send an injected message to the origin in a chunk of its own between the client's messages, and one to the client between the origin's", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, reqHead, origin.addr, origin.addr)
		io.WriteString(client, chunk(grpcEnvelope(0, "ping 1")))
		upstream := origin.accept(t)
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "ping 1")))
		req := h1PairOf(t, captured)

		injectOnceCarried(t, proxy, req.ID, domain.StreamDirectionRequest, "injected", StreamInjectOptions{})
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "injected")))
		io.WriteString(client, chunk(grpcEnvelope(0, "ping 2")))
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "ping 2")))

		io.WriteString(upstream, resHead+chunk(grpcEnvelope(0, "pong 1")))
		readWithin(t, client, br, "the client", resHead+chunk(grpcEnvelope(0, "pong 1")))
		toClient := injectOnceCarried(t, proxy, req.ID, domain.StreamDirectionResponse, "to the client", StreamInjectOptions{})
		readWithin(t, client, br, "the client", chunk(grpcEnvelope(0, "to the client")))
		if toClient.Metadata["injected"] != true || toClient.Direction != "response" {
			t.Fatalf("the message injected toward the client\nwanted:\nfrom the response, injected\ngot:\n%+v", toClient)
		}
	})

	t.Run("should close both connections when the pair is cancelled, and record both directions cancelled", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, reqHead, origin.addr, origin.addr)
		io.WriteString(client, chunk(grpcEnvelope(0, "ping")))
		upstream := origin.accept(t)
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "ping")))
		io.WriteString(upstream, resHead+chunk(grpcEnvelope(0, "pong")))
		readWithin(t, client, br, "the client", resHead+chunk(grpcEnvelope(0, "pong")))
		req := h1PairOf(t, captured)

		if err := proxy.CancelStreamedPair(req.ID); err != nil {
			t.Fatal(err)
		}
		readAllUntilClose(t, client, br, "")
		upstream.SetReadDeadline(time.Now().Add(5 * time.Second))
		if rest, err := io.ReadAll(upstream.br); err != nil || len(rest) != 0 {
			t.Fatalf("the origin connection\nwanted:\nclosed by the proxy, nothing more\ngot:\n%q, %v", rest, err)
		}
		select {
		case res := <-captured.response:
			for _, name := range []string{"request", "response"} {
				if d := direction(t, res.Metadata, name); d["ending"] != "cancelled" {
					t.Errorf("metadata.streamed.%s\nwanted:\ncancelled\ngot:\n%v", name, d)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
	})
}
