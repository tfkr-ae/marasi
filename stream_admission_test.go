package marasi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
)

// testGate is a project gate a test blocks and reopens, as a project
// switch does: work that arrives while it is blocked waits, and is admitted
// in the generation open once it reopens.
type testGate struct {
	mu         sync.Mutex
	generation uint64
	blocked    chan struct{}
	// waiting is signalled each time work waits on the block.
	waiting chan struct{}
}

func newTestGate() *testGate {
	return &testGate{generation: 1, waiting: make(chan struct{}, 16)}
}

func (g *testGate) admit(ctx context.Context, _ bool) (func(), uint64, error) {
	g.mu.Lock()
	blocked, generation := g.blocked, g.generation
	g.mu.Unlock()
	if blocked != nil {
		g.waiting <- struct{}{}
		select {
		case <-blocked:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
		g.mu.Lock()
		generation = g.generation
		g.mu.Unlock()
	}
	return func() {}, generation, nil
}

// block blocks admission.
func (g *testGate) block() {
	g.mu.Lock()
	g.blocked = make(chan struct{})
	g.mu.Unlock()
}

// reopen admits work again, in the next generation when switched.
func (g *testGate) reopen(switched bool) {
	g.mu.Lock()
	if switched {
		g.generation++
	}
	close(g.blocked)
	g.blocked = nil
	g.mu.Unlock()
}

// awaitWaiting waits until work waits on the block.
func (g *testGate) awaitWaiting(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.waiting:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s\nwanted:\nwaiting for admission\ngot:\nnot waiting", what)
	}
}

// streamCounts counts the streamed-pair events a proxy announces.
type streamCounts struct {
	started, messages, ended atomic.Int32
}

func (e *streamCounts) configure(proxy *Proxy) {
	proxy.OnStreamedPairStarted = func(domain.StreamedPairStarted) { e.started.Add(1) }
	proxy.OnStreamMessage = func(domain.StreamMessage) { e.messages.Add(1) }
	proxy.OnStreamedPairEnded = func(domain.StreamedPairEnded) { e.ended.Add(1) }
}

// closeLiveTrafficWithin runs CloseLiveTrafficAndFlush and fails unless it
// returns within a few seconds.
func closeLiveTrafficWithin(t *testing.T, proxy *Proxy) {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- proxy.CloseLiveTrafficAndFlush() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("closing live traffic\nwanted:\nno error\ngot:\n%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing live traffic\nwanted:\nreturned\ngot:\nwaiting on a streamed response blocked behind the gate")
	}
}

// storedMessageCount is how many stream messages the proxy stored for pair.
func storedMessageCount(t *testing.T, proxy *Proxy, pair uuid.UUID) int {
	t.Helper()
	return len(storedMessages(t, proxy, pair))
}

func TestStreamedResponseAdmission(t *testing.T) {
	const sseHead = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"

	t.Run("should not wait on an HTTP/1.1 server-sent event stream whose head waits behind the block, and refuse it once the project changed", func(t *testing.T) {
		gate := newTestGate()
		events := &streamCounts{}
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.admitWork = gate.admit
			events.configure(proxy)
		})
		client, br := dialProxy(t, addr)
		io.WriteString(client, "GET http://"+origin.addr+"/events HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n")
		upstream := origin.accept(t)
		req := <-captured.request

		gate.block()
		io.WriteString(upstream, sseHead+chunk("data: one\n\n"))
		gate.awaitWaiting(t, "the response head")
		closeLiveTrafficWithin(t, proxy)
		gate.reopen(true)

		client.SetReadDeadline(time.Now().Add(5 * time.Second))
		res, err := http.ReadResponse(br, nil)
		if err != nil || res.StatusCode != http.StatusBadGateway {
			t.Fatalf("the client's response\nwanted:\nMarasi's 502\ngot:\n%v, %v", res, err)
		}
		if _, err := upstream.Read(make([]byte, 1)); err == nil {
			t.Fatal("the origin connection\nwanted:\nclosed\ngot:\nstill open")
		}
		if n := storedMessageCount(t, proxy, req.ID); n != 0 || events.started.Load() != 0 || events.messages.Load() != 0 {
			t.Fatalf("the stale pair's streaming\nwanted:\nnone stored or announced\ngot:\n%d stored, %d started, %d messages", n, events.started.Load(), events.messages.Load())
		}
		if _, live := proxy.liveStreamedPair(req.ID); live {
			t.Fatal("the stale pair\nwanted:\nnot live\ngot:\nlive")
		}
	})

	t.Run("should carry an HTTP/1.1 server-sent event stream whose head waited behind a block that reopened on the same project", func(t *testing.T) {
		gate := newTestGate()
		events := &streamCounts{}
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.admitWork = gate.admit
			events.configure(proxy)
		})
		client, br := dialProxy(t, addr)
		io.WriteString(client, "GET http://"+origin.addr+"/events HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n")
		upstream := origin.accept(t)
		req := <-captured.request

		gate.block()
		io.WriteString(upstream, sseHead+chunk("data: one\n\n"))
		gate.awaitWaiting(t, "the response head")
		gate.reopen(false)
		readWithin(t, client, br, "the client", sseHead+chunk("data: one\n\n"))
		io.WriteString(upstream, "0\r\n\r\n")
		readWithin(t, client, br, "the client", "0\r\n\r\n")
		select {
		case <-captured.response:
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
		if n := storedMessageCount(t, proxy, req.ID); n != 1 || events.started.Load() != 1 {
			t.Fatalf("the pair's streaming\nwanted:\n1 stored, started once\ngot:\n%d stored, %d started", n, events.started.Load())
		}
	})

	t.Run("should end the admission wait of an HTTP/1.1 bidirectional pair's response head when live traffic is closed", func(t *testing.T) {
		gate := newTestGate()
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) { proxy.admitWork = gate.admit })
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "POST http://%s/test.Echo/PingPong HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n", origin.addr, origin.addr)
		io.WriteString(client, chunk(grpcEnvelope(0, "ping")))
		upstream := origin.accept(t)
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "ping")))
		req := <-captured.request
		if _, live := proxy.liveStreamedPair(req.ID); !live {
			t.Fatal("the pair\nwanted:\nlive, its request streamed\ngot:\nnot live")
		}

		gate.block()
		io.WriteString(upstream, "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n")
		gate.awaitWaiting(t, "the response head")
		closeLiveTrafficWithin(t, proxy)
		gate.reopen(true)
		client.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadAll(bufio.NewReader(br)); err != nil {
			t.Fatalf("the client connection\nwanted:\nclosed\ngot:\n%v", err)
		}
	})

	t.Run("should not wait on an HTTP/2 server-sent event stream whose head waits behind the block, and refuse it once the project changed", func(t *testing.T) {
		gate := newTestGate()
		events := &streamCounts{}
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true, func(proxy *Proxy) {
			proxy.admitWork = gate.admit
			events.configure(proxy)
		})
		proxy := proxyOf(t, addr)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
		id := upstream.nthStream(t, 0)
		req := <-captured.request

		gate.block()
		upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream")
		upstream.data(id, false, "data: one\n\n")
		gate.awaitWaiting(t, "the response head")
		closeLiveTrafficWithin(t, proxy)
		gate.reopen(true)

		response := client.stream(t, 1)
		if len(response.headers) != 1 {
			t.Fatalf("the client's response\nwanted:\nMarasi's 502\ngot:\n%v", response.headers)
		}
		if status, _ := response.headers[0].get(":status"); status != "502" {
			t.Fatalf("the client's response\nwanted:\nMarasi's 502\ngot:\n%s", status)
		}
		upstream.await(t, "the origin stream's reset", func(p *h2TestPeer) bool { return p.streams[id].reset != "" })
		if n := storedMessageCount(t, proxy, req.ID); n != 0 || events.started.Load() != 0 || events.messages.Load() != 0 {
			t.Fatalf("the stale pair's streaming\nwanted:\nnone stored or announced\ngot:\n%d stored, %d started, %d messages", n, events.started.Load(), events.messages.Load())
		}
	})

	t.Run("should queue a streamed pair's response and announce its end before closing live traffic returns", func(t *testing.T) {
		ended := make(chan struct{}, 1)
		call := startGRPCCall(t, func(proxy *Proxy) {
			proxy.OnStreamedPairEnded = func(domain.StreamedPairEnded) {
				// A slow handler: the drain must still wait for it.
				time.Sleep(50 * time.Millisecond)
				ended <- struct{}{}
			}
		})
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping"))

		closeLiveTrafficWithin(t, call.proxy)
		select {
		case <-ended:
		default:
			t.Fatal("the pair's end\nwanted:\nannounced before closing live traffic returned\ngot:\nnot yet")
		}
		select {
		case <-call.captured.response:
		default:
			t.Fatal("the pair's response\nwanted:\nstored before closing live traffic returned\ngot:\nnot yet")
		}
	})
}
