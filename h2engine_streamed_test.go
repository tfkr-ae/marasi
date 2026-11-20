package marasi

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
)

// grpcEnvelope is payload in a gRPC envelope with flags.
func grpcEnvelope(flags byte, payload string) string {
	var prefix [5]byte
	prefix[0] = flags
	binary.BigEndian.PutUint32(prefix[1:], uint32(len(payload)))
	return string(prefix[:]) + payload
}

// grpcCall opens a gRPC call through the proxy: a client and the origin's
// side of the call, the origin stream's ID, and the proxy.
type grpcCall struct {
	client, upstream *h2TestPeer
	id               uint32
	proxy            *Proxy
	captured         *capturedPair
}

func startGRPCCall(t *testing.T, configure ...func(*Proxy)) grpcCall {
	t.Helper()
	origin := newH2TestOrigin(t)
	addr, captured, ca := serveH1EngineMITMProxy(t, true, configure...)
	client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
	upstream := origin.accept(t)
	client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/PingPong",
		"content-type", "application/grpc", "te", "trailers")
	return grpcCall{client: client, upstream: upstream, id: upstream.nthStream(t, 0), proxy: proxyOf(t, addr), captured: captured}
}

// awaitData waits until the data that arrived on stream id is want.
func awaitData(t *testing.T, p *h2TestPeer, who string, id uint32, want string) {
	t.Helper()
	p.await(t, who+" to receive "+strings.ReplaceAll(want, "\x00", ""), func(p *h2TestPeer) bool {
		s := p.streams[id]
		return s != nil && string(s.data) == want
	})
}

// arrived is what arrived on stream id so far.
func arrived(p *h2TestPeer, id uint32) h2TestStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.streams[id]; s != nil {
		return *s
	}
	return h2TestStream{}
}

// streamedResponse waits for the stored response of a streamed pair.
func streamedResponse(t *testing.T, captured *capturedPair) (domain.ProxyRequest, domain.ProxyResponse) {
	t.Helper()
	var req domain.ProxyRequest
	select {
	case req = <-captured.request:
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not captured")
	}
	select {
	case res := <-captured.response:
		return req, res
	case <-time.After(5 * time.Second):
		t.Fatal("the response was not captured")
		return req, domain.ProxyResponse{}
	}
}

// storedMessages are the pair's stored stream messages, in order.
func storedMessages(t *testing.T, proxy *Proxy, pair uuid.UUID) []*domain.StreamMessage {
	t.Helper()
	if err := proxy.flushDBWrites(); err != nil {
		t.Fatal(err)
	}
	messages, _, err := proxy.StreamRepo.ListStreamMessages(pair, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

// direction is one direction's entry in a streamed pair's metadata.
func direction(t *testing.T, metadata map[string]any, name string) map[string]any {
	t.Helper()
	// As stored and as sent in events: through JSON.
	raw, err := json.Marshal(metadata["streamed"])
	if err != nil {
		t.Fatal(err)
	}
	var streamed map[string]any
	if json.Unmarshal(raw, &streamed) != nil || streamed == nil {
		t.Fatalf("metadata.streamed\nwanted:\nan object\ngot:\n%v", metadata)
	}
	d, ok := streamed[name].(map[string]any)
	if !ok {
		t.Fatalf("metadata.streamed.%s\nwanted:\nan object\ngot:\n%v", name, streamed)
	}
	return d
}

func TestH2Engine_StreamedPair(t *testing.T) {
	t.Run("should carry each gRPC message on as it arrives, before either direction ends, and store the pair with empty bodies and each message as its own row", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.await(t, "the request head", func(p *h2TestPeer) bool { return len(p.streams[call.id].headers) == 1 })
		if s := arrived(call.upstream, call.id); s.ended {
			t.Fatal("the origin's request\nwanted:\nthe head with the stream left open\ngot:\nthe stream ended")
		}

		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping 1"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping 1"))
		call.client.data(1, false, grpcEnvelope(0, "pong 1"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "pong 1"))
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping 2"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping 1")+grpcEnvelope(0, "ping 2"))
		call.client.data(1, true, grpcEnvelope(0, "pong 2"))
		request := call.upstream.stream(t, call.id)
		if string(request.data) != grpcEnvelope(0, "pong 1")+grpcEnvelope(0, "pong 2") {
			t.Fatalf("the origin's request body\nwanted:\nboth messages\ngot:\n%q", request.data)
		}
		call.upstream.headers(call.id, true, "grpc-status", "0", "grpc-message", "")
		response := call.client.stream(t, 1)
		if status, _ := response.trailers.get("grpc-status"); status != "0" || response.reset != "" {
			t.Fatalf("the client's trailers\nwanted:\ngrpc-status 0\ngot:\n%v, reset %q", response.trailers, response.reset)
		}

		req, res := streamedResponse(t, call.captured)
		if !strings.HasSuffix(string(req.Raw), "\r\n\r\n") || string(res.Raw) != "HTTP/2 200\r\ncontent-type: application/grpc\r\n\r\n" {
			t.Fatalf("the stored pair\nwanted:\nheads with empty bodies\ngot:\n%q\n%q", req.Raw, res.Raw)
		}
		for name, want := range map[string]float64{"request": 2, "response": 2} {
			d := direction(t, res.Metadata, name)
			if d["framing"] != "grpc" || d["messages"] != want || d["ending"] != "complete" {
				t.Errorf("metadata.streamed.%s\nwanted:\ngrpc, %v messages, complete\ngot:\n%v", name, want, d)
			}
		}
		trailers, _ := direction(t, res.Metadata, "response")["trailers"].([]any)
		if len(trailers) != 2 {
			t.Errorf("the response's trailers in metadata\nwanted:\ngrpc-status and grpc-message\ngot:\n%v", trailers)
		}

		messages := storedMessages(t, call.proxy, req.ID)
		want := []struct{ direction, payload string }{{"response", "ping 1"}, {"request", "pong 1"}, {"response", "ping 2"}, {"request", "pong 2"}}
		if len(messages) != len(want) {
			t.Fatalf("stored messages\nwanted:\n%d\ngot:\n%d", len(want), len(messages))
		}
		for i, w := range want {
			m := messages[i]
			if m.Direction != w.direction || string(m.Payload) != w.payload || m.Framing != "grpc" || m.Flags == nil || *m.Flags != 0 {
				t.Errorf("message %d\nwanted:\n%s %q grpc flags 0\ngot:\n%s %q %s %v", i, w.direction, w.payload, m.Direction, m.Payload, m.Framing, m.Flags)
			}
		}
	})

	t.Run("should pass an origin RST_STREAM partway through a message on with the same code, forwarding and storing what arrived as cut short", func(t *testing.T) {
		call := startGRPCCall(t)
		call.client.data(1, true, grpcEnvelope(0, "hello"))
		call.upstream.stream(t, call.id)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		partial := grpcEnvelope(0, "a long reply")[:9]
		call.upstream.data(call.id, false, partial)
		call.upstream.write(func(f *http2.Framer) error { return f.WriteRSTStream(call.id, http2.ErrCodeInternal) })

		call.client.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].reset != "" })
		response := call.client.stream(t, 1)
		if string(response.data) != partial || response.reset != "INTERNAL_ERROR" {
			t.Fatalf("the client's stream\nwanted:\n%q then RST_STREAM INTERNAL_ERROR\ngot:\n%q, reset %q", partial, response.data, response.reset)
		}
		req, res := streamedResponse(t, call.captured)
		d := direction(t, res.Metadata, "response")
		if d["ending"] != "reset" || d["reset_code"] != "INTERNAL_ERROR" || d["messages"] != float64(1) {
			t.Errorf("metadata.streamed.response\nwanted:\nreset INTERNAL_ERROR, 1 message\ngot:\n%v", d)
		}
		if d := direction(t, res.Metadata, "request"); d["ending"] != "complete" {
			t.Errorf("metadata.streamed.request\nwanted:\ncomplete\ngot:\n%v", d)
		}
		messages := storedMessages(t, call.proxy, req.ID)
		last := messages[len(messages)-1]
		if len(messages) != 2 || last.Direction != "response" || string(last.Payload) != "a lo" || last.Metadata["cut_short"] != true {
			t.Fatalf("stored messages\nwanted:\nthe request's, then %q cut short\ngot:\n%d, last %+v", "a lo", len(messages), last)
		}
	})

	t.Run("should pass the client's RST_STREAM on to the origin with the same code and record the call as cancelled", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping"))
		call.client.write(func(f *http2.Framer) error { return f.WriteRSTStream(1, http2.ErrCodeCancel) })

		call.upstream.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[call.id].reset != "" })
		if reset := arrived(call.upstream, call.id).reset; reset != "CANCEL" {
			t.Fatalf("the origin's stream\nwanted:\nRST_STREAM CANCEL\ngot:\n%q", reset)
		}
		_, res := streamedResponse(t, call.captured)
		for _, name := range []string{"request", "response"} {
			if d := direction(t, res.Metadata, name); d["ending"] != "cancelled" {
				t.Errorf("metadata.streamed.%s\nwanted:\ncancelled\ngot:\n%v", name, d)
			}
		}
	})

	t.Run("should still send the origin a streamed request's head, then the client's reset, when the client resets it while the request chain runs", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		chainRunning := make(chan struct{})
		addr, _, ca := serveH1EngineMITMProxy(t, true, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(func(proxy *Proxy, pair *Pair) error {
				close(chainRunning)
				time.Sleep(200 * time.Millisecond)
				return nil
			})
		})
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/StreamingInputCall",
			"content-type", "application/grpc", "te", "trailers")
		<-chainRunning
		client.write(func(f *http2.Framer) error { return f.WriteRSTStream(1, http2.ErrCodeCancel) })

		id := upstream.nthStream(t, 0)
		upstream.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[id].reset != "" })
		if got := arrived(upstream, id); len(got.headers) != 1 || got.reset != "CANCEL" {
			t.Fatalf("the origin's stream\nwanted:\nthe head, then RST_STREAM CANCEL\ngot:\n%d heads, reset %q", len(got.headers), got.reset)
		}
	})

	t.Run("should record a message over the per-message limit as too large, with the size it declared, and reset the stream on both legs", func(t *testing.T) {
		call := startGRPCCall(t, func(proxy *Proxy) { proxy.Config.StreamMaxMessageSize = 8 })
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "fits")+grpcEnvelope(0, "far too long")[:7])

		call.client.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[1] != nil && p.streams[1].reset != "" })
		call.upstream.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[call.id].reset != "" })
		if got := arrived(call.client, 1); string(got.data) != grpcEnvelope(0, "fits") || got.reset != "ENHANCE_YOUR_CALM" {
			t.Fatalf("the client's stream\nwanted:\nthe message that fits, then RST_STREAM ENHANCE_YOUR_CALM\ngot:\n%q, %q", got.data, got.reset)
		}
		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		last := messages[len(messages)-1]
		if len(messages) != 2 || last.Metadata["too_large"] != true || last.Metadata["size_seen"] != float64(12) {
			t.Fatalf("stored messages\nwanted:\nthe message that fits, then one too large with size_seen 12\ngot:\n%d, last %+v", len(messages), last)
		}
	})

	t.Run("should stop a sender within one message and the window once the receiver stops reading, then carry every message in order once it reads again", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.mu.Lock()
		call.upstream.holdWindow = true
		call.upstream.mu.Unlock()
		message := grpcEnvelope(0, strings.Repeat("x", 16<<10))
		body := strings.Repeat(message, 40)

		// The client sends as far as Marasi's windows let it: the initial
		// 65,535 bytes and what Marasi gives back on the stream.
		sent := sendWithinWindow(t, call.client, 1, body)
		if sent == len(body) {
			t.Fatalf("the client's sending\nwanted:\nstopped by flow control\ngot:\nall %d bytes sent", sent)
		}
		// Marasi forwards no more than the origin's 65,535-byte window
		// and holds at most one message being forwarded, so it gives the
		// client back no more than that.
		if limit := 2*h2DefaultWindow + len(message); sent > limit {
			t.Fatalf("bytes the client could send\nwanted:\nat most %d\ngot:\n%d", limit, sent)
		}
		if got := len(arrived(call.upstream, call.id).data); got > h2DefaultWindow {
			t.Fatalf("bytes the origin received\nwanted:\nat most its window, %d\ngot:\n%d", h2DefaultWindow, got)
		}

		call.upstream.mu.Lock()
		call.upstream.holdWindow = false
		received := uint32(len(call.upstream.streams[call.id].data))
		call.upstream.mu.Unlock()
		call.upstream.write(func(f *http2.Framer) error {
			f.WriteWindowUpdate(0, received)
			return f.WriteWindowUpdate(call.id, received)
		})
		sent += sendWithinWindow(t, call.client, 1, body[sent:])
		if sent != len(body) {
			t.Fatalf("the client's sending once the origin read again\nwanted:\nall %d bytes\ngot:\n%d", len(body), sent)
		}
		call.client.data(1, true, "")
		if got := call.upstream.stream(t, call.id).data; string(got) != body {
			t.Fatalf("the origin's request body\nwanted:\n%d bytes, every message in order\ngot:\n%d bytes", len(body), len(got))
		}
	})

	t.Run("should carry a pair whose heads name no framing as before, read whole", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/", "content-type", "application/proto")
		client.data(1, false, "one ")
		time.Sleep(100 * time.Millisecond)
		upstream.mu.Lock()
		got := len(upstream.order)
		upstream.mu.Unlock()
		if got != 0 {
			t.Fatalf("the origin's requests before the body ended\nwanted:\nnone\ngot:\n%d", got)
		}
		client.data(1, true, "two")
		id := upstream.nthStream(t, 0)
		if body := upstream.stream(t, id).data; string(body) != "one two" {
			t.Fatalf("the origin's request body\nwanted:\n%q\ngot:\n%q", "one two", body)
		}
		upstream.headers(id, false, ":status", "200", "content-type", "text/plain")
		upstream.data(id, true, "done")
		if body := client.stream(t, 1).data; string(body) != "done" {
			t.Fatalf("the client's response body\nwanted:\n%q\ngot:\n%q", "done", body)
		}
		_, res := streamedResponse(t, captured)
		if _, ok := res.Metadata["streamed"]; ok || !strings.HasSuffix(string(res.Raw), "done") {
			t.Fatalf("the stored response\nwanted:\nthe body, no streamed metadata\ngot:\n%q %v", res.Raw, res.Metadata)
		}
	})
}

// sendWithinWindow sends body on stream id in DATA frames as the receive
// window the proxy gives allows, starting from what the peer was given so
// far, and returns how much it sent before the window stayed shut for
// 300ms.
func sendWithinWindow(t *testing.T, p *h2TestPeer, id uint32, body string) int {
	t.Helper()
	sent := 0
	for sent < len(body) {
		p.mu.Lock()
		window := h2DefaultWindow + p.credit[id] - p.sentOn[id]
		changed := p.changed
		p.mu.Unlock()
		if window <= 0 {
			select {
			case <-changed:
				continue
			case <-time.After(300 * time.Millisecond):
				return sent
			}
		}
		n := min(int(window), len(body)-sent, 16384)
		p.mu.Lock()
		p.sentOn[id] += int64(n)
		p.mu.Unlock()
		p.data(id, false, body[sent:sent+n])
		sent += n
	}
	return sent
}

func TestH2Engine_StreamedFramings(t *testing.T) {
	cases := []struct {
		name                 string
		path, requestType    string
		requestMessages      []string // the client's messages; none sends the head with END_STREAM
		responseType         string
		responseMessages     []string
		framing, request     string // the response's framing, and the request's ("" when not streamed)
		flags                []*uint8
		responsePayloadFirst string // the first response message's stored payload
	}{
		{
			name: "gRPC-Web, its trailers frame a stream message", path: "/test.Echo/Say", requestType: "application/grpc-web+proto",
			requestMessages: []string{grpcEnvelope(0, "hi")}, responseType: "application/grpc-web+proto",
			responseMessages: []string{grpcEnvelope(0, "one"), grpcEnvelope(0x80, "grpc-status: 0\r\n")},
			framing:          "grpc-web", request: "grpc-web", responsePayloadFirst: "one",
		},
		{
			name: "Connect streaming, its end-of-stream envelope a stream message", path: "/test.Echo/Say", requestType: "application/connect+json",
			requestMessages: []string{grpcEnvelope(0, `{"a":1}`)}, responseType: "application/connect+json",
			responseMessages: []string{grpcEnvelope(0, `{"b":2}`), grpcEnvelope(0x02, `{}`)},
			framing:          "connect", request: "connect", responsePayloadFirst: `{"b":2}`,
		},
		{
			name: "server-sent events, one event per message", path: "/events",
			responseType: "text/event-stream", responseMessages: []string{"id: 1\ndata: first\n\n", "id: 2\ndata: second\n\n"},
			framing: "sse", responsePayloadFirst: "id: 1\ndata: first\n\n",
		},
		{
			name: "NDJSON, one line per message", path: "/logs",
			responseType: "application/x-ndjson", responseMessages: []string{"{\"n\":1}\n", "{\"n\":2}\n"},
			framing: "ndjson", responsePayloadFirst: "{\"n\":1}\n",
		},
	}
	for _, c := range cases {
		t.Run("should carry "+c.name+" as each message arrives, and store each as its own row", func(t *testing.T) {
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true)
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			head := []string{":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", c.path}
			if c.requestType != "" {
				head = []string{":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", c.path, "content-type", c.requestType}
			}
			client.headers(1, len(c.requestMessages) == 0, head...)
			id := upstream.nthStream(t, 0)
			sent := ""
			for _, m := range c.requestMessages {
				client.data(1, false, m)
				sent += m
				awaitData(t, upstream, "the origin", id, sent)
			}
			upstream.headers(id, false, ":status", "200", "content-type", c.responseType)
			upstream.data(id, false, c.responseMessages[0])
			awaitData(t, client, "the client", 1, c.responseMessages[0])
			if len(c.requestMessages) > 0 {
				client.data(1, true, "")
			}
			upstream.data(id, true, c.responseMessages[1])
			if body := client.stream(t, 1).data; string(body) != strings.Join(c.responseMessages, "") {
				t.Fatalf("the client's response body\nwanted:\n%q\ngot:\n%q", strings.Join(c.responseMessages, ""), body)
			}

			req, res := streamedResponse(t, captured)
			d := direction(t, res.Metadata, "response")
			if d["framing"] != c.framing || d["messages"] != float64(2) || d["ending"] != "complete" {
				t.Errorf("metadata.streamed.response\nwanted:\n%s, 2 messages, complete\ngot:\n%v", c.framing, d)
			}
			if c.request != "" {
				if d := direction(t, res.Metadata, "request"); d["framing"] != c.request || d["messages"] != float64(1) {
					t.Errorf("metadata.streamed.request\nwanted:\n%s, 1 message\ngot:\n%v", c.request, d)
				}
			}
			var responses []*domain.StreamMessage
			for _, m := range storedMessages(t, proxyOf(t, addr), req.ID) {
				if m.Direction == "response" {
					responses = append(responses, m)
				}
			}
			if len(responses) != 2 || string(responses[0].Payload) != c.responsePayloadFirst || responses[0].Framing != c.framing {
				t.Fatalf("stored response messages\nwanted:\n2, the first %q as %s\ngot:\n%d %v", c.responsePayloadFirst, c.framing, len(responses), responses)
			}
			envelope := c.framing == "grpc-web" || c.framing == "connect"
			if (responses[1].Flags != nil) != envelope {
				t.Errorf("the second message's flags\nwanted:\nset only for an envelope framing\ngot:\n%v", responses[1].Flags)
			}
		})
	}

	ordinary := []struct{ name, path, contentType string }{
		{"unary Connect with protobuf", "/test.Echo/Say", "application/proto"},
		{"unary Connect with JSON", "/test.Echo/Say", "application/json"},
		{"a /pkg.Svc/Method path with JSON", "/pkg.Svc/Method", "application/json"},
	}
	for _, c := range ordinary {
		t.Run("should store "+c.name+" as an ordinary pair, its bodies read whole", func(t *testing.T) {
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true)
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", c.path, "content-type", c.contentType)
			client.data(1, true, `{"a":1}`)
			id := upstream.nthStream(t, 0)
			if body := upstream.stream(t, id).data; string(body) != `{"a":1}` {
				t.Fatalf("the origin's request body\nwanted:\n%q\ngot:\n%q", `{"a":1}`, body)
			}
			upstream.headers(id, false, ":status", "200", "content-type", c.contentType)
			upstream.data(id, true, `{"b":2}`)
			client.stream(t, 1)
			req, res := streamedResponse(t, captured)
			if _, ok := res.Metadata["streamed"]; ok || !strings.HasSuffix(string(req.Raw), `{"a":1}`) || !strings.HasSuffix(string(res.Raw), `{"b":2}`) {
				t.Fatalf("the stored pair\nwanted:\nboth bodies, no streamed metadata\ngot:\n%q\n%q\n%v", req.Raw, res.Raw, res.Metadata)
			}
		})
	}
}
