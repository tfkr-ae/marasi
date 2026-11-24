package marasi

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// h1ScriptedOrigin is an HTTP/1.1 origin the test drives by hand: each
// connection it accepts is handed over once its request head is read.
type h1ScriptedOrigin struct {
	addr  string
	conns chan h1OriginConn
}

// h1OriginConn is one origin connection and the request head read on it.
type h1OriginConn struct {
	net.Conn
	br   *bufio.Reader
	head *h1.RequestHead
}

func newH1ScriptedOrigin(t *testing.T) *h1ScriptedOrigin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	origin := &h1ScriptedOrigin{addr: l.Addr().String(), conns: make(chan h1OriginConn, 4)}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
			br := bufio.NewReader(conn)
			head, err := h1.ReadRequestHead(br)
			if err != nil {
				conn.Close()
				continue
			}
			origin.conns <- h1OriginConn{Conn: conn, br: br, head: head}
		}
	}()
	return origin
}

// accept waits for the next request to reach the origin.
func (origin *h1ScriptedOrigin) accept(t *testing.T) h1OriginConn {
	t.Helper()
	select {
	case conn := <-origin.conns:
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("no request reached the origin")
		return h1OriginConn{}
	}
}

// chunk is data as one chunk of a chunked body.
func chunk(data string) string { return fmt.Sprintf("%x\r\n%s\r\n", len(data), data) }

// readWithin reads len(want) bytes within a second and compares them with
// want, so a proxy that holds them back until the body ends fails.
func readWithin(t *testing.T, conn net.Conn, br *bufio.Reader, who, want string) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	got := make([]byte, len(want))
	n, err := io.ReadFull(br, got)
	if err != nil || string(got) != want {
		t.Fatalf("%s\nwanted, before the body ended:\n%q\ngot:\n%q (%v)", who, want, got[:n], err)
	}
}

func TestH1Engine_StreamedPair(t *testing.T) {
	t.Run("should carry each server-sent event to the client as it arrives, before the origin ends the response", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		_, addr, _ := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		io.WriteString(client, "GET http://"+origin.addr+"/events HTTP/1.1\r\nHost: "+origin.addr+"\r\nAccept: text/event-stream\r\n\r\n")

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk("id: 1\ndata: first\n\n"))
		readWithin(t, client, br, "the client", head+chunk("id: 1\ndata: first\n\n"))

		io.WriteString(upstream, chunk("id: 2\ndata: second\n\n")+"0\r\n\r\n")
		readWithin(t, client, br, "the client", chunk("id: 2\ndata: second\n\n")+"0\r\n\r\n")
	})

	t.Run("should store the pair with an empty response body and each event as its own message, then how the response ended", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		io.WriteString(client, "GET http://"+origin.addr+"/events HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n")

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk("data: one\n\ndata: tw")+chunk("o\n\n")+"0\r\nX-Done: yes\r\n\r\n")
		readWithin(t, client, br, "the client", head+chunk("data: one\n\n")+chunk("data: two\n\n")+"0\r\nX-Done: yes\r\n\r\n")

		req, res := streamedResponse(t, captured)
		if string(res.Raw) != head {
			t.Fatalf("the stored response\nwanted:\n%q\ngot:\n%q", head, res.Raw)
		}
		d := direction(t, res.Metadata, "response")
		if d["framing"] != "sse" || d["messages"] != float64(2) || d["ending"] != "complete" {
			t.Errorf("metadata.streamed.response\nwanted:\nsse, 2 messages, complete\ngot:\n%v", d)
		}
		messages := storedMessages(t, proxy, req.ID)
		if len(messages) != 2 || string(messages[0].Payload) != "data: one\n\n" || string(messages[1].Payload) != "data: two\n\n" || messages[0].Flags != nil {
			t.Fatalf("stored messages\nwanted:\nthe two events, no flags\ngot:\n%+v", messages)
		}
	})

	t.Run("should carry a gRPC-Web request and response a message at a time in both directions, each answered before either ends", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		const reqHead = "POST /test.Echo/PingPong HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"
		fmt.Fprintf(client, "POST http://%s/test.Echo/PingPong HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n", origin.addr, origin.addr)
		io.WriteString(client, chunk(grpcEnvelope(0, "ping 1")))

		upstream := origin.accept(t)
		if got := string(upstream.head.Bytes()); got != fmt.Sprintf(reqHead, origin.addr) {
			t.Fatalf("the origin's request head\nwanted:\n%q\ngot:\n%q", fmt.Sprintf(reqHead, origin.addr), got)
		}
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "ping 1")))
		const resHead = "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, resHead+chunk(grpcEnvelope(0, "pong 1")))
		readWithin(t, client, br, "the client", resHead+chunk(grpcEnvelope(0, "pong 1")))

		io.WriteString(client, chunk(grpcEnvelope(0, "ping 2"))+"0\r\n\r\n")
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "ping 2"))+"0\r\n\r\n")
		trailers := grpcEnvelope(0x80, "grpc-status: 0\r\n")
		io.WriteString(upstream, chunk(grpcEnvelope(0, "pong 2"))+chunk(trailers)+"0\r\n\r\n")
		readWithin(t, client, br, "the client", chunk(grpcEnvelope(0, "pong 2"))+chunk(trailers)+"0\r\n\r\n")

		req, res := streamedResponse(t, captured)
		if want := fmt.Sprintf(reqHead, origin.addr); string(req.Raw) != want {
			t.Fatalf("the stored request\nwanted:\n%q\ngot:\n%q", want, req.Raw)
		}
		for name, want := range map[string]float64{"request": 2, "response": 3} {
			if d := direction(t, res.Metadata, name); d["framing"] != "grpc-web" || d["messages"] != want || d["ending"] != "complete" {
				t.Errorf("metadata.streamed.%s\nwanted:\ngrpc-web, %v messages, complete\ngot:\n%v", name, want, d)
			}
		}
		messages := storedMessages(t, proxy, req.ID)
		want := []struct{ direction, payload string }{{"request", "ping 1"}, {"response", "pong 1"}, {"request", "ping 2"}, {"response", "pong 2"}, {"response", "grpc-status: 0\r\n"}}
		if len(messages) != len(want) {
			t.Fatalf("stored messages\nwanted:\n%d\ngot:\n%d", len(want), len(messages))
		}
		for i, w := range want {
			if m := messages[i]; m.Direction != w.direction || string(m.Payload) != w.payload {
				t.Errorf("message %d\nwanted:\n%s %q\ngot:\n%s %q", i, w.direction, w.payload, m.Direction, m.Payload)
			}
		}

		// Both connections carry the next request.
		fmt.Fprintf(client, "GET http://%s/next HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		next, err := h1.ReadRequestHead(upstream.br)
		if err != nil || next.Target != "/next" {
			t.Fatalf("the next request on the origin connection\nwanted:\n/next\ngot:\n%v, %v", next, err)
		}
	})

	t.Run("should forward what arrived of a message the origin cut short, store it as cut short and close the client connection", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk("data: one\n\n")+chunk("data: tw"))
		readWithin(t, client, br, "the client", head+chunk("data: one\n\n"))
		upstream.Close()
		readAllUntilClose(t, client, br, chunk("data: tw"))

		req, res := streamedResponse(t, captured)
		if d := direction(t, res.Metadata, "response"); d["ending"] != "cut short" || d["messages"] != float64(2) {
			t.Errorf("metadata.streamed.response\nwanted:\ncut short, 2 messages\ngot:\n%v", d)
		}
		messages := storedMessages(t, proxy, req.ID)
		if last := messages[len(messages)-1]; len(messages) != 2 || string(last.Payload) != "data: tw" || last.Metadata["cut_short"] != true {
			t.Fatalf("stored messages\nwanted:\none, then %q cut short\ngot:\n%+v", "data: tw", messages)
		}
	})

	t.Run("should close the origin connection when the client leaves partway through, and record the call as cancelled", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		_, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "POST http://%s/test.Echo/PingPong HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc-web\r\nTransfer-Encoding: chunked\r\n\r\n", origin.addr, origin.addr)
		io.WriteString(client, chunk(grpcEnvelope(0, "ping")))

		upstream := origin.accept(t)
		readWithin(t, upstream, upstream.br, "the origin", chunk(grpcEnvelope(0, "ping")))
		const resHead = "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, resHead+chunk(grpcEnvelope(0, "pong")))
		readWithin(t, client, br, "the client", resHead+chunk(grpcEnvelope(0, "pong")))
		client.Close()

		upstream.SetReadDeadline(time.Now().Add(5 * time.Second))
		if rest, err := io.ReadAll(upstream.br); err != nil {
			t.Fatalf("the origin connection\nwanted:\nclosed by the proxy\ngot:\n%q, %v", rest, err)
		}
		_, res := streamedResponse(t, captured)
		for _, name := range []string{"request", "response"} {
			if d := direction(t, res.Metadata, name); d["ending"] != "cancelled" {
				t.Errorf("metadata.streamed.%s\nwanted:\ncancelled\ngot:\n%v", name, d)
			}
		}
	})

	t.Run("should close the origin connection when the client of an event stream leaves while the origin sends nothing", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		_, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk("data: one\n\n"))
		readWithin(t, client, br, "the client", head+chunk("data: one\n\n"))
		client.Close()

		upstream.SetReadDeadline(time.Now().Add(5 * time.Second))
		if rest, err := io.ReadAll(upstream.br); err != nil {
			t.Fatalf("the origin connection\nwanted:\nclosed by the proxy\ngot:\n%q, %v", rest, err)
		}
		_, res := streamedResponse(t, captured)
		if d := direction(t, res.Metadata, "response"); d["ending"] != "cancelled" {
			t.Errorf("metadata.streamed.response\nwanted:\ncancelled\ngot:\n%v", d)
		}
	})

	t.Run("should carry a pipelined request that arrived while an event stream was carried, once it ends", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		_, addr, _ := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk("data: one\n\n"))
		readWithin(t, client, br, "the client", head+chunk("data: one\n\n"))
		fmt.Fprintf(client, "GET http://%s/next HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		time.Sleep(200 * time.Millisecond)
		io.WriteString(upstream, chunk("data: two\n\n")+"0\r\n\r\n")
		readWithin(t, client, br, "the client", chunk("data: two\n\n")+"0\r\n\r\n")

		next, err := h1.ReadRequestHead(upstream.br)
		if err != nil || next.Target != "/next" {
			t.Fatalf("the next request on the origin connection\nwanted:\n/next\ngot:\n%v, %v", next, err)
		}
	})

	t.Run("should carry a response read until the origin closes and then close the client connection", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		_, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/logs HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: application/x-ndjson\r\n\r\n"
		io.WriteString(upstream, head+"{\"n\":1}\n")
		readWithin(t, client, br, "the client", head+"{\"n\":1}\n")
		io.WriteString(upstream, "{\"n\":2}\n")
		readWithin(t, client, br, "the client", "{\"n\":2}\n")
		upstream.Close()
		readAllUntilClose(t, client, br, "")

		_, res := streamedResponse(t, captured)
		if d := direction(t, res.Metadata, "response"); d["framing"] != "ndjson" || d["ending"] != "complete" || d["messages"] != float64(2) {
			t.Errorf("metadata.streamed.response\nwanted:\nndjson, complete, 2 messages\ngot:\n%v", d)
		}
	})

	t.Run("should stop the origin through TCP once the client stops reading, then carry every message in order once it reads again", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		_, addr, _ := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/watch HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head)
		// 32 MiB of 1 MiB messages, while the client reads nothing.
		event := grpcEnvelope(0, strings.Repeat("x", 1<<20-5))
		const events = 32
		var written atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			upstream.SetWriteDeadline(time.Now().Add(30 * time.Second))
			for i := 0; i < events; i++ {
				if _, err := io.WriteString(upstream, chunk(event)); err != nil {
					return
				}
				written.Add(1)
			}
			io.WriteString(upstream, "0\r\n\r\n")
		}()
		time.Sleep(2 * time.Second)
		if n := int(written.Load()); n*len(event) > 16<<20 {
			t.Fatalf("what the origin could send while the client read nothing\nwanted:\nat most a few socket buffers and one message\ngot:\n%d of %d messages", n, events)
		}

		// Once the client reads, everything goes on.
		client.SetReadDeadline(time.Now().Add(20 * time.Second))
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		got, err := io.ReadAll(res.Body)
		if err != nil || len(got) != events*len(event) {
			t.Fatalf("the client's body\nwanted:\n%d bytes\ngot:\n%d (%v)", events*len(event), len(got), err)
		}
		<-done
	})

	t.Run("should store a message over the limit as too large and close both connections", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) { proxy.Config.StreamMaxMessageSize = 8 })
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "POST http://%s/test.Echo/Say HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc-web\r\nContent-Length: 17\r\n\r\n", origin.addr, origin.addr)
		io.WriteString(client, grpcEnvelope(0, "a long reply"))

		upstream := origin.accept(t)
		readAllUntilClose(t, client, br, "")
		upstream.SetReadDeadline(time.Now().Add(5 * time.Second))
		if rest, err := io.ReadAll(upstream.br); err != nil || len(rest) != 0 {
			t.Fatalf("the origin connection\nwanted:\nclosed with nothing of the message\ngot:\n%q, %v", rest, err)
		}
		req, res := streamedResponse(t, captured)
		if d := direction(t, res.Metadata, "request"); d["ending"] != "reset" || d["reset_code"] != "ENHANCE_YOUR_CALM" {
			t.Errorf("metadata.streamed.request\nwanted:\nreset ENHANCE_YOUR_CALM\ngot:\n%v", d)
		}
		messages := storedMessages(t, proxy, req.ID)
		if len(messages) != 1 || messages[0].Metadata["too_large"] != true || messages[0].Metadata["size_seen"] != float64(12) {
			t.Fatalf("stored messages\nwanted:\none too large, size 12\ngot:\n%+v", messages)
		}
	})
}

func TestH2Engine_StreamedPairTranslatedToHTTP1(t *testing.T) {
	t.Run("should keep a gRPC response's grpc-status in its trailers through the HTTP/1.1 origin's chunked encoding", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h1Origin := newScriptedTLSOrigin(t, func(conn net.Conn) {
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/grpc\r\nTransfer-Encoding: chunked\r\n\r\n"+
				chunk(grpcEnvelope(0, "pong"))+"0\r\nGrpc-Status: 0\r\nGrpc-Message: \r\n\r\n")
		})
		addr, _, ca := serveH1EngineMITMProxy(t, true)
		proxyOf(t, addr).AddRequestPairModifier(redirectPaths("/test.Echo", h1Origin.addr))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2", "http/1.1")
		origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/Say",
			"content-type", "application/grpc", "te", "trailers")
		client.data(1, true, grpcEnvelope(0, "ping"))

		if got := h1Origin.received(t); h1Payload(t, got) != grpcEnvelope(0, "ping") {
			t.Fatalf("the HTTP/1.1 origin's request\nwanted:\nthe message as its body\ngot:\n%q", got)
		}
		response := client.stream(t, 1)
		if status, _ := response.trailers.get("grpc-status"); status != "0" || string(response.data) != grpcEnvelope(0, "pong") {
			t.Fatalf("the client's response\nwanted:\nthe message, then trailers with grpc-status 0\ngot:\n%q, trailers %v", response.data, response.trailers)
		}
	})
}

func TestH1Engine_StreamMessageHooks(t *testing.T) {
	t.Run("should send a chunked message processStreamMessage rewrote in a chunk of its own, and ignore a length change when the head declares a length", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		logs := &proxyLogs{}
		proxy, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			logs.record(proxy)
			withStreamExtension(t, "rewriter", `
				function processStreamMessage(message)
					message:set_payload(message:payload() .. " (edited)")
				end
			`)(proxy)
		})
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "POST http://%s/test.Echo/Say HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc-web\r\nContent-Length: 10\r\n\r\n", origin.addr, origin.addr)
		io.WriteString(client, grpcEnvelope(0, "hello"))

		upstream := origin.accept(t)
		readWithin(t, upstream, upstream.br, "the origin, with the length its head declares", grpcEnvelope(0, "hello"))
		const head = "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk(grpcEnvelope(0, "reply"))+"0\r\n\r\n")
		readWithin(t, client, br, "the client", head+chunk(grpcEnvelope(0, "reply (edited)"))+"0\r\n\r\n")
		if err := proxy.flushDBWrites(); err != nil {
			t.Fatal(err)
		}
		if !logs.find("WARN", "declares a length") {
			t.Error("the proxy logs\nwanted:\na WARN naming the declared length\ngot:\nnone")
		}
	})
}

func TestH1Engine_CheckpointStreamMessages(t *testing.T) {
	t.Run("should hold a gRPC response's chunked trailers as a trailer item and send and store an edited grpc-status", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "POST http://%s/test.Echo/Say HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc\r\nTE: trailers\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: application/grpc\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+"0\r\ngrpc-status: 0\r\n\r\n")
		held := awaitItems(t, proxy, domain.CheckpointTypeTrailer, 1)[0]
		if string(held.Raw) != "grpc-status: 0\r\n" || held.Direction != "response" || held.Framing != "grpc" {
			t.Fatalf("the held trailer item\nwanted:\nthe chunked trailers of the response\ngot:\n%+v %q", held, held.Raw)
		}
		edit := []byte("grpc-status: 7\r\n")
		if err := proxy.ForwardCheckpoint(held.ID, CheckpointForward{Raw: &edit}); err != nil {
			t.Fatal(err)
		}
		readWithin(t, client, br, "the client", head+"0\r\ngrpc-status: 7\r\n\r\n")

		_, res := streamedResponse(t, captured)
		trailers, _ := direction(t, res.Metadata, "response")["trailers"].([]any)
		if len(trailers) != 1 || trailers[0].(map[string]any)["value"] != "7" {
			t.Errorf("the stored trailers\nwanted:\ngrpc-status 7\ngot:\n%v", trailers)
		}
	})

	t.Run("should send a held event the user edited, and store it edited", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) { proxy.SetStreamIntercept(true) })
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk("data: one\n\n")+"0\r\n\r\n")
		held := awaitStreamItems(t, proxy, 1)[0]
		edited := []byte("data: edited\n\n")
		if err := proxy.ForwardCheckpoint(held.ID, CheckpointForward{Payload: &edited}); err != nil {
			t.Fatalf("forwarding the held event: %v", err)
		}
		readWithin(t, client, br, "the client", head+chunk("data: edited\n\n")+"0\r\n\r\n")

		req, _ := streamedResponse(t, captured)
		messages := storedMessages(t, proxy, req.ID)
		if len(messages) != 1 || string(messages[0].Payload) != "data: edited\n\n" || messages[0].Metadata["edited"] != true {
			t.Fatalf("stored messages\nwanted:\nthe edited event\ngot:\n%+v", messages)
		}
	})

	t.Run("should drop a held event as abandoned and close the origin connection when the client leaves", func(t *testing.T) {
		var mu sync.Mutex
		var abandoned []domain.CheckpointItem
		origin := newH1ScriptedOrigin(t)
		proxy, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			proxy.SetStreamIntercept(true)
			proxy.OnCheckpointAbandoned = func(item domain.CheckpointItem) error {
				mu.Lock()
				defer mu.Unlock()
				abandoned = append(abandoned, item)
				return nil
			}
		})
		client, br := dialProxy(t, addr)
		fmt.Fprintf(client, "GET http://%s/events HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk("data: one\n\n"))
		readWithin(t, client, br, "the client", head)
		held := awaitStreamItems(t, proxy, 1)[0]
		client.Close()

		awaitStreamItems(t, proxy, 0)
		mu.Lock()
		if len(abandoned) != 1 || abandoned[0].ID != held.ID {
			t.Errorf("abandoned items\nwanted:\nthe held event\ngot:\n%+v", abandoned)
		}
		mu.Unlock()
		upstream.SetReadDeadline(time.Now().Add(5 * time.Second))
		if rest, err := io.ReadAll(upstream.br); err != nil {
			t.Fatalf("the origin connection\nwanted:\nclosed by the proxy\ngot:\n%q, %v", rest, err)
		}
	})
}

func TestH1Engine_StreamedPairKeepAliveAfterCutShortMessage(t *testing.T) {
	intercepted := make(chan domain.CheckpointItem, 8)
	proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) {
		proxy.OnIntercept = func(item domain.CheckpointItem) error {
			intercepted <- item
			return nil
		}
	})
	forwardNext := func(t *testing.T, wantDirection string) domain.CheckpointItem {
		t.Helper()
		select {
		case item := <-intercepted:
			if item.Direction != wantDirection {
				t.Fatalf("held item direction\nwanted:\n%s\ngot:\n%s (%+v)", wantDirection, item.Direction, item)
			}
			if err := proxy.ForwardCheckpoint(item.ID, CheckpointForward{}); err != nil {
				t.Fatalf("forwarding held %s message: %v", wantDirection, err)
			}
			return item
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for held %s message", wantDirection)
			return domain.CheckpointItem{}
		}
	}

	for _, tc := range []struct {
		name     string
		resHead  string
		resBody  string
		wantWire string
	}{
		{
			name:     "chunked Connect response with incomplete envelope",
			resHead:  "HTTP/1.1 200 OK\r\nContent-Type: application/connect+json\r\nTransfer-Encoding: chunked\r\n\r\n",
			resBody:  chunk("\x00\x00\x00\x00\x10short") + "0\r\n\r\n",
			wantWire: chunk("\x00\x00\x00\x00\x10short") + "0\r\n\r\n",
		},
		{
			name:     "length-framed Connect response with incomplete envelope prefix",
			resHead:  "HTTP/1.1 200 OK\r\nContent-Type: application/connect+json\r\nContent-Length: 3\r\n\r\n",
			resBody:  "\x00\x00\x00",
			wantWire: "\x00\x00\x00",
		},
	} {
		t.Run("should keep the connection alive after a "+tc.name+", so the next Connect server-stream call receives its end-of-stream envelope under Checkpoint", func(t *testing.T) {
			proxy.SetStreamIntercept(true)
			defer proxy.SetStreamIntercept(false)
			origin := newH1ScriptedOrigin(t)
			client, br := dialProxy(t, addr)

			// First call: valid HTTP/1.1 framing carrying a truncated Connect envelope.
			req1Body := grpcEnvelope(0, "{}")
			fmt.Fprintf(client, "POST http://%s/connectrpc.conformance.v1.ConformanceService/ServerStream HTTP/1.1\r\nHost: %s\r\nContent-Type: application/connect+json\r\nTransfer-Encoding: chunked\r\n\r\n", origin.addr, origin.addr)
			io.WriteString(client, chunk(req1Body)+"0\r\n\r\n")

			upstream := origin.accept(t)
			forwardNext(t, "request")
			readWithin(t, upstream, upstream.br, "the origin (call 1)", chunk(req1Body)+"0\r\n\r\n")

			io.WriteString(upstream, tc.resHead+tc.resBody)
			readWithin(t, client, br, "the client (call 1)", tc.resHead+tc.wantWire)

			// Wait for endStreams and finish of call 1 so any erroneous close has already happened.
			req1, res1 := streamedResponse(t, captured)
			if d := direction(t, res1.Metadata, "response"); d["ending"] != "cut short" || d["messages"] != float64(1) {
				t.Fatalf("call 1 metadata.streamed.response\nwanted:\ncut short, 1 message\ngot:\n%v", d)
			}
			msgs1 := storedMessages(t, proxy, req1.ID)
			if len(msgs1) != 2 || msgs1[1].Direction != "response" || msgs1[1].Metadata["cut_short"] != true {
				t.Fatalf("call 1 stored messages\nwanted:\nrequest message, then cut-short response message\ngot:\n%+v", msgs1)
			}

			// Second call on the SAME keep-alive connection: Connect server-stream
			// error response carrying only the 0x02 end-of-stream envelope.
			req2Body := grpcEnvelope(0, `{"error":{"code":"invalid_argument"}}`)
			if _, err := fmt.Fprintf(client, "POST http://%s/connectrpc.conformance.v1.ConformanceService/ServerStream HTTP/1.1\r\nHost: %s\r\nContent-Type: application/connect+json\r\nTransfer-Encoding: chunked\r\n\r\n%s0\r\n\r\n", origin.addr, origin.addr, chunk(req2Body)); err != nil {
				t.Fatalf("writing call 2 on the kept-alive client connection: %v", err)
			}

			nextHead, err := h1.ReadRequestHead(upstream.br)
			if err != nil || nextHead.Target != "/connectrpc.conformance.v1.ConformanceService/ServerStream" {
				t.Fatalf("call 2 on the kept-alive origin connection\nwanted:\nServerStream request\ngot:\n%v, %v", nextHead, err)
			}
			forwardNext(t, "request")
			readWithin(t, upstream, upstream.br, "the origin (call 2)", chunk(req2Body)+"0\r\n\r\n")

			endStreamEnv := grpcEnvelope(0x02, `{"error":{"code":"invalid_argument","message":"bad input"}}`)
			const res2Head = "HTTP/1.1 200 OK\r\nContent-Type: application/connect+json\r\nTransfer-Encoding: chunked\r\n\r\n"
			io.WriteString(upstream, res2Head+chunk(endStreamEnv)+"0\r\n\r\n")
			heldEnd := forwardNext(t, "response")
			if heldEnd.Flags == nil || *heldEnd.Flags != 0x02 {
				t.Fatalf("held end-of-stream item\nwanted:\nflags 0x02\ngot:\n%+v", heldEnd)
			}
			readWithin(t, client, br, "the client (call 2)", res2Head+chunk(endStreamEnv)+"0\r\n\r\n")

			_, res2 := streamedResponse(t, captured)
			if d := direction(t, res2.Metadata, "response"); d["ending"] != "complete" || d["messages"] != float64(1) {
				t.Fatalf("call 2 metadata.streamed.response\nwanted:\ncomplete, 1 message\ngot:\n%v", d)
			}
		})
	}

	t.Run("should keep the connection alive when a cleanly framed streamed request ends inside an incomplete stream message", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		client, br := dialProxy(t, addr)

		const partialReq = "\x00\x00\x00\x00\x10short"
		fmt.Fprintf(client, "POST http://%s/connectrpc.conformance.v1.ConformanceService/ServerStream HTTP/1.1\r\nHost: %s\r\nContent-Type: application/connect+json\r\nTransfer-Encoding: chunked\r\n\r\n%s0\r\n\r\n", origin.addr, origin.addr, chunk(partialReq))

		upstream := origin.accept(t)
		readWithin(t, upstream, upstream.br, "the origin (partial request)", chunk(partialReq)+"0\r\n\r\n")
		endStreamEnv := grpcEnvelope(0x02, `{"error":{"code":"invalid_argument"}}`)
		const resHead = "HTTP/1.1 200 OK\r\nContent-Type: application/connect+json\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, resHead+chunk(endStreamEnv)+"0\r\n\r\n")
		readWithin(t, client, br, "the client (call 1)", resHead+chunk(endStreamEnv)+"0\r\n\r\n")

		req1, res1 := streamedResponse(t, captured)
		if d := direction(t, res1.Metadata, "request"); d["ending"] != "cut short" || d["messages"] != float64(1) {
			t.Fatalf("call 1 metadata.streamed.request\nwanted:\ncut short, 1 message\ngot:\n%v", d)
		}
		msgs1 := storedMessages(t, proxy, req1.ID)
		if len(msgs1) < 1 || msgs1[0].Direction != "request" || msgs1[0].Metadata["cut_short"] != true {
			t.Fatalf("call 1 stored request message\nwanted:\ncut_short true\ngot:\n%+v", msgs1)
		}

		// Both connections must remain usable for the next request.
		fmt.Fprintf(client, "GET http://%s/next HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
		nextHead, err := h1.ReadRequestHead(upstream.br)
		if err != nil || nextHead.Target != "/next" {
			t.Fatalf("the next request on the origin connection\nwanted:\n/next\ngot:\n%v, %v", nextHead, err)
		}
		const okRes = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
		io.WriteString(upstream, okRes)
		readWithin(t, client, br, "the client (next)", okRes)
		// Drain the non-streamed /next pair from captured so subsequent subtests see their own pairs.
		streamedResponse(t, captured)
	})

	t.Run("should keep the connection alive after cleanly framed gRPC-Web and SSE responses whose final message was cut short", func(t *testing.T) {
		var gzBuf bytes.Buffer
		gz := gzip.NewWriter(&gzBuf)
		gz.Write([]byte("data: partial"))
		gz.Close()

		for _, tc := range []struct {
			name     string
			framing  string
			resHead  string
			resBody  string
			readHTTP bool
		}{
			{
				name:    "gRPC-Web incomplete envelope",
				framing: "grpc-web",
				resHead: "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n",
				resBody: chunk("\x00\x00\x00\x00\x09hi") + "0\r\n\r\n",
			},
			{
				name:    "SSE incomplete event without blank line",
				framing: "sse",
				resHead: "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n",
				resBody: chunk("data: partial") + "0\r\n\r\n",
			},
			{
				name:     "gzipped SSE incomplete event without blank line",
				framing:  "sse",
				resHead:  "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n",
				resBody:  chunk(gzBuf.String()) + "0\r\n\r\n",
				readHTTP: true,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				origin := newH1ScriptedOrigin(t)
				client, br := dialProxy(t, addr)
				fmt.Fprintf(client, "GET http://%s/stream HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)

				upstream := origin.accept(t)
				io.WriteString(upstream, tc.resHead+tc.resBody)
				if tc.readHTTP {
					client.SetReadDeadline(time.Now().Add(2 * time.Second))
					res, err := http.ReadResponse(br, nil)
					if err != nil {
						t.Fatalf("reading gzipped response: %v", err)
					}
					if _, err := io.ReadAll(res.Body); err != nil {
						t.Fatalf("reading gzipped body: %v", err)
					}
					res.Body.Close()
				} else {
					readWithin(t, client, br, "the client (call 1)", tc.resHead+tc.resBody)
				}

				_, res1 := streamedResponse(t, captured)
				if d := direction(t, res1.Metadata, "response"); d["framing"] != tc.framing || d["ending"] != "cut short" {
					t.Fatalf("call 1 metadata.streamed.response\nwanted:\n%s, cut short\ngot:\n%v", tc.framing, d)
				}

				fmt.Fprintf(client, "GET http://%s/next HTTP/1.1\r\nHost: %s\r\n\r\n", origin.addr, origin.addr)
				nextHead, err := h1.ReadRequestHead(upstream.br)
				if err != nil || nextHead.Target != "/next" {
					t.Fatalf("the next request on the origin connection\nwanted:\n/next\ngot:\n%v, %v", nextHead, err)
				}
				const okRes = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
				io.WriteString(upstream, okRes)
				readWithin(t, client, br, "the client (next)", okRes)
				streamedResponse(t, captured)
			})
		}
	})
}
