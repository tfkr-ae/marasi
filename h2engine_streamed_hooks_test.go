package marasi

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
	"github.com/tfkr-ae/marasi/stream"
)

// withStreamExtension installs an enabled extension named name with lua.
func withStreamExtension(t *testing.T, name, lua string) func(*Proxy) {
	return func(proxy *Proxy) {
		ext := &domain.Extension{ID: uuid.New(), Name: name, LuaContent: lua, Enabled: true}
		onLog := func(extensions.ExtensionLog) error { return nil }
		if err := proxy.WithOptions(WithExtension(ext, extensions.ExtensionWithLogHandler(onLog))); err != nil {
			t.Fatalf("installing %s: %v", name, err)
		}
	}
}

// proxyLogs records the proxy logs written while the test runs.
type proxyLogs struct {
	mu      sync.Mutex
	entries []domain.Log
}

func (l *proxyLogs) record(proxy *Proxy) {
	proxy.OnLog = func(log domain.Log) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.entries = append(l.entries, log)
		return nil
	}
}

func (l *proxyLogs) find(level, contains string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if entry.Level == level && strings.Contains(entry.Message, contains) {
			return true
		}
	}
	return false
}

func TestH2Engine_StreamMessageHooks(t *testing.T) {
	t.Run("should send and store a payload processStreamMessage rewrote, marked edited", func(t *testing.T) {
		call := startGRPCCall(t, withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				if message:direction() == "request" and message:framing() == "grpc" then
					message:set_metadata({ seen = message:length() })
					message:set_payload(message:payload() .. " (edited)")
				end
			end
		`))
		call.client.data(1, true, grpcEnvelope(0, "hello"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "hello (edited)"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "reply"))
		call.upstream.headers(call.id, true, "grpc-status", "0")
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "reply"))

		req, _ := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 2 {
			t.Fatalf("stored messages\nwanted:\n2\ngot:\n%d", len(messages))
		}
		sent, reply := messages[0], messages[1]
		rewriter, _ := sent.Metadata["rewriter"].(map[string]any)
		if string(sent.Payload) != "hello (edited)" || sent.Metadata["edited"] != true || rewriter["seen"] != float64(5) {
			t.Errorf("the stored request message\nwanted:\n%q, edited, rewriter.seen 5\ngot:\n%q %v", "hello (edited)", sent.Payload, sent.Metadata)
		}
		if string(reply.Payload) != "reply" || reply.Metadata["edited"] != nil {
			t.Errorf("the stored response message\nwanted:\n%q, not edited\ngot:\n%q %v", "reply", reply.Payload, reply.Metadata)
		}
	})

	t.Run("should send a server-sent event processStreamMessage rewrote as the bare event, with no envelope or flags", func(t *testing.T) {
		call := startGRPCCall(t, withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				if message:framing() == "sse" then
					message:set_payload("data: edited\n\n")
				end
			end
		`))
		call.client.data(1, true, grpcEnvelope(0, "hello"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "hello"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "text/event-stream")
		call.upstream.data(call.id, true, "data: first\n\n")
		awaitData(t, call.client, "the client", 1, "data: edited\n\n")

		req, _ := streamedResponse(t, call.captured)
		var event *domain.StreamMessage
		for _, m := range storedMessages(t, call.proxy, req.ID) {
			if m.Direction == "response" {
				event = m
			}
		}
		if event == nil || string(event.Payload) != "data: edited\n\n" || event.Flags != nil || event.Metadata["edited"] != true {
			t.Errorf("the stored event\nwanted:\n%q, edited, no flags\ngot:\n%+v", "data: edited\n\n", event)
		}
	})

	t.Run("should run processStreamMessage on the last NDJSON line, which the end of the body makes whole", func(t *testing.T) {
		call := startGRPCCall(t, withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				if message:framing() == "ndjson" then
					message:set_payload("{\"edited\":true}\n")
				end
			end
		`))
		call.client.data(1, true, grpcEnvelope(0, "hello"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "hello"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/x-ndjson")
		call.upstream.data(call.id, true, "{\"n\":1}\n{\"n\":2}")
		awaitData(t, call.client, "the client", 1, "{\"edited\":true}\n{\"edited\":true}\n")

		req, _ := streamedResponse(t, call.captured)
		var edited int
		for _, m := range storedMessages(t, call.proxy, req.ID) {
			if m.Direction == "response" && m.Metadata["edited"] == true {
				edited++
			}
		}
		if edited != 2 {
			t.Errorf("stored response messages marked edited\nwanted:\n2\ngot:\n%d", edited)
		}
	})

	t.Run("should not forward a message processStreamMessage dropped, and store it as dropped", func(t *testing.T) {
		call := startGRPCCall(t, withStreamExtension(t, "dropper", `
			function processStreamMessage(message)
				if message:payload() == "secret" then
					message:drop()
				end
			end
		`))
		call.client.data(1, false, grpcEnvelope(0, "secret"))
		call.client.data(1, true, grpcEnvelope(0, "public"))
		request := call.upstream.stream(t, call.id)
		if string(request.data) != grpcEnvelope(0, "public") {
			t.Fatalf("the origin's request body\nwanted:\nonly the message not dropped\ngot:\n%q", request.data)
		}
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.headers(call.id, true, "grpc-status", "0")

		req, res := streamedResponse(t, call.captured)
		messages := storedMessages(t, call.proxy, req.ID)
		if len(messages) != 2 || string(messages[0].Payload) != "secret" || messages[0].Metadata["dropped"] != true || messages[1].Metadata["dropped"] != nil {
			t.Fatalf("stored messages\nwanted:\n%q dropped, then %q\ngot:\n%d %+v", "secret", "public", len(messages), messages)
		}
		if d := direction(t, res.Metadata, "request"); d["messages"] != float64(2) || d["ending"] != "complete" {
			t.Errorf("metadata.streamed.request\nwanted:\n2 messages, complete\ngot:\n%v", d)
		}
	})

	t.Run("should not run later extensions on a message processStreamMessage skipped, and still forward it", func(t *testing.T) {
		call := startGRPCCall(t,
			withStreamExtension(t, "skipper", `
				function processStreamMessage(message)
					message:skip()
				end
			`),
			withStreamExtension(t, "later", `
				function processStreamMessage(message)
					message:drop()
				end
			`))
		call.client.data(1, true, grpcEnvelope(0, "hello"))
		if request := call.upstream.stream(t, call.id); string(request.data) != grpcEnvelope(0, "hello") {
			t.Fatalf("the origin's request body\nwanted:\nthe message\ngot:\n%q", request.data)
		}
	})

	t.Run("should ignore and log a change to the length of a direction whose head declared one, and carry an edit that keeps it", func(t *testing.T) {
		logs := &proxyLogs{}
		message := grpcEnvelope(0, "abc")
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true, logs.record, withStreamExtension(t, "editor", `
			function processStreamMessage(message)
				if message:payload() == "abc" then
					message:set_payload("abcdef")
				elseif message:payload() == "xyz" then
					message:drop()
				elseif message:payload() == "keep" then
					message:set_payload("KEEP")
				end
			end
		`))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		body := message + grpcEnvelope(0, "xyz") + grpcEnvelope(0, "keep")
		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/test.Echo/Unary",
			"content-type", "application/grpc", "content-length", strconv.Itoa(len(body)))
		id := upstream.nthStream(t, 0)
		client.data(1, true, body)
		request := upstream.stream(t, id)
		if want := message + grpcEnvelope(0, "xyz") + grpcEnvelope(0, "KEEP"); string(request.data) != want {
			t.Fatalf("the origin's request body\nwanted:\n%q\ngot:\n%q", want, request.data)
		}
		upstream.headers(id, false, ":status", "200", "content-type", "application/grpc")
		upstream.headers(id, true, "grpc-status", "0")
		req, _ := streamedResponse(t, captured)
		proxy := proxyOf(t, addr)
		messages := storedMessages(t, proxy, req.ID)
		if len(messages) != 3 || string(messages[0].Payload) != "abc" || messages[0].Metadata["edited"] != nil ||
			messages[1].Metadata["dropped"] != nil || string(messages[2].Payload) != "KEEP" || messages[2].Metadata["edited"] != true {
			t.Fatalf("stored messages\nwanted:\n%q and %q as they came, %q edited\ngot:\n%d %+v", "abc", "xyz", "KEEP", len(messages), messages)
		}
		if !logs.find("WARN", "an edit that changes its length was ignored") || !logs.find("WARN", "a drop was ignored") {
			t.Errorf("proxy logs\nwanted:\na WARN for the edit and one for the drop\ngot:\n%+v", logs.entries)
		}
	})
}

func TestProcessStreamMessageExtensions(t *testing.T) {
	t.Run("should run enabled extensions in order and not checkpoint", func(t *testing.T) {
		proxy := newTestProxy(t,
			&domain.Extension{ID: uuid.New(), Name: "first", LuaContent: `
				function processStreamMessage(message) message:set_payload("first") end
			`},
			&domain.Extension{ID: testExtensions["checkpoint"].ID, Name: "checkpoint", LuaContent: `
				function processStreamMessage(message) message:drop() end
			`},
			&domain.Extension{ID: uuid.New(), Name: "second", LuaContent: `
				function processStreamMessage(message)
					message:set_payload(message:payload() .. ", second")
				end
			`},
		)
		message := &stream.LiveMessage{Framing: stream.FramingGRPC}

		processStreamMessageExtensions(proxy, uuid.Nil, message)

		if string(message.Payload) != "first, second" || message.Dropped {
			t.Fatalf("\nwanted:\n%q, not dropped\ngot:\n%q, dropped %t", "first, second", message.Payload, message.Dropped)
		}
	})

	t.Run("should drop the message when a hook is interrupted", func(t *testing.T) {
		proxy := newTestProxy(t, &domain.Extension{
			ID: testExtensions["workshop"].ID, Name: "workshop",
			LuaContent: "function processStreamMessage(message) " + blockingHookBody + " end",
		})
		ext, _ := proxy.GetExtension("workshop")
		entered := interruptibleHook(t, ext)
		message := &stream.LiveMessage{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			processStreamMessageExtensions(proxy, uuid.Nil, message)
		}()
		awaitEntered(t, entered, ext.CancelExecution, done)

		if !message.Dropped {
			t.Fatal("wanted the interrupted hook to drop the message")
		}
	})

	t.Run("should leave the message as it was after a Lua error", func(t *testing.T) {
		proxy := newTestProxy(t, &domain.Extension{
			ID: uuid.New(), Name: "broken", LuaContent: `function processStreamMessage(message) error("boom") end`,
		})
		message := &stream.LiveMessage{Payload: []byte("hello")}

		processStreamMessageExtensions(proxy, uuid.Nil, message)

		if message.Dropped || string(message.Payload) != "hello" {
			t.Fatalf("\nwanted:\n%q, not dropped\ngot:\n%q, dropped %t", "hello", message.Payload, message.Dropped)
		}
	})
}
