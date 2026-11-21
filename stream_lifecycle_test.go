package marasi

import (
	"context"
	"io"
	"testing"
	"time"
)

// closeWithin closes the proxy's transport and fails unless the close
// returns within a few seconds.
func closeWithin(t *testing.T, proxy *Proxy, closeTransport func(*Proxy) error) {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- closeTransport(proxy) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("closing the proxy\nwanted:\nno error\ngot:\n%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the proxy\nwanted:\nthe live streamed pair cancelled and the close returned\ngot:\nthe close still waiting")
	}
}

func TestProxy_CloseCancelsStreamedPairs(t *testing.T) {
	closes := map[string]func(*Proxy) error{
		"Close":                 func(proxy *Proxy) error { return proxy.Close() },
		"CloseTransportContext": func(proxy *Proxy) error { return proxy.CloseTransportContext(context.Background()) },
	}
	for name, closeTransport := range closes {
		t.Run("should cancel a live HTTP/2 gRPC call and return from "+name, func(t *testing.T) {
			call := startGRPCCall(t)
			call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
			call.upstream.data(call.id, false, grpcEnvelope(0, "ping"))
			awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping"))

			closeWithin(t, call.proxy, closeTransport)
			_, res := streamedResponse(t, call.captured)
			if d := direction(t, res.Metadata, "response"); d["ending"] != "cancelled" {
				t.Fatalf("metadata.streamed.response\nwanted:\ncancelled\ngot:\n%v", d)
			}
			if _, live := call.proxy.liveStreamedPair(res.ID); live {
				t.Fatal("the streamed pair\nwanted:\nfinished\ngot:\nstill live")
			}
		})

		t.Run("should cancel a live HTTP/1.1 server-sent event stream and return from "+name, func(t *testing.T) {
			origin := newH1ScriptedOrigin(t)
			proxy, addr, captured := serveFramingProxy(t, nil)
			client, br := dialProxy(t, addr)
			io.WriteString(client, "GET http://"+origin.addr+"/events HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n")
			upstream := origin.accept(t)
			const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
			io.WriteString(upstream, head+chunk("data: one\n\n"))
			readWithin(t, client, br, "the client", head+chunk("data: one\n\n"))

			closeWithin(t, proxy, closeTransport)
			_, res := streamedResponse(t, captured)
			if d := direction(t, res.Metadata, "response"); d["ending"] != "cancelled" {
				t.Fatalf("metadata.streamed.response\nwanted:\ncancelled\ngot:\n%v", d)
			}
		})
	}
}

func TestProxy_CloseLiveTrafficAndFlush(t *testing.T) {
	t.Run("should cancel a live streamed pair, wait until it finished and store its messages before returning", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping"))

		if err := call.proxy.CloseLiveTrafficAndFlush(); err != nil {
			t.Fatalf("closing live traffic\nwanted:\nno error\ngot:\n%v", err)
		}
		live := 0
		call.proxy.streamedPairs.Range(func(any, any) bool { live++; return true })
		if live != 0 {
			t.Fatalf("live streamed pairs\nwanted:\n0\ngot:\n%d", live)
		}
		req, _ := streamedResponse(t, call.captured)
		messages, _, err := call.proxy.StreamRepo.ListStreamMessages(req.ID, nil, 100)
		if err != nil || len(messages) != 1 || string(messages[0].Payload) != "ping" {
			t.Fatalf("stored messages\nwanted:\nping, stored before the call returned\ngot:\n%d (%v)", len(messages), err)
		}
		call.client.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].reset != "" })
		if reset := arrived(call.client, 1).reset; reset != "CANCEL" {
			t.Fatalf("the client's stream\nwanted:\nRST_STREAM CANCEL\ngot:\n%q", reset)
		}
	})
}
