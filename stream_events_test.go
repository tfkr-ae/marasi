package marasi

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

// streamEvents records what a proxy announces about its streamed pairs, in
// the order it announced it.
type streamEvents struct {
	mu     sync.Mutex
	events []any
	times  []time.Time
}

func (e *streamEvents) add(event any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	e.times = append(e.times, time.Now())
}

// install sets the proxy's streamed pair handlers to record into e.
func (e *streamEvents) install(t *testing.T) func(*Proxy) {
	return func(proxy *Proxy) {
		if err := proxy.WithOptions(
			WithStreamedPairStartedHandler(func(started domain.StreamedPairStarted) { e.add(started) }),
			WithStreamMessageHandler(func(message domain.StreamMessage) { e.add(message) }),
			WithStreamMessageProgressHandler(func(progress domain.StreamMessageProgress) { e.add(progress) }),
			WithStreamedPairEndedHandler(func(ended domain.StreamedPairEnded) { e.add(ended) }),
		); err != nil {
			t.Fatal(err)
		}
	}
}

// await waits until an announcement satisfies done, and returns them all.
func (e *streamEvents) await(t *testing.T, what string, done func(any) bool) []any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		events := append([]any(nil), e.events...)
		e.mu.Unlock()
		for _, event := range events {
			if done(event) {
				return events
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s\nwanted:\nannounced\ngot:\n%s", what, describeStreamEvents(e.events))
	return nil
}

func describeStreamEvents(events []any) string {
	lines := make([]string, len(events))
	for i, event := range events {
		switch event := event.(type) {
		case domain.StreamMessage:
			lines[i] = fmt.Sprintf("message %s %q", event.Direction, event.Payload)
		default:
			lines[i] = fmt.Sprintf("%T %+v", event, event)
		}
	}
	return strings.Join(lines, "\n")
}

func isEnded(event any) bool { _, ok := event.(domain.StreamedPairEnded); return ok }

func TestStreamedPairEvents(t *testing.T) {
	t.Run("should announce a gRPC call's start, each message in order and its end with the trailers", func(t *testing.T) {
		var events streamEvents
		call := startGRPCCall(t, events.install(t))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping 1"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping 1"))
		call.client.data(1, false, grpcEnvelope(0, "pong 1"))
		awaitData(t, call.upstream, "the origin", call.id, grpcEnvelope(0, "pong 1"))
		call.client.data(1, true, grpcEnvelope(0, "pong 2"))
		call.upstream.stream(t, call.id)
		call.upstream.headers(call.id, true, "grpc-status", "0", "grpc-message", "")
		call.client.stream(t, 1)
		req, _ := streamedResponse(t, call.captured)

		got := events.await(t, "the end of the call", isEnded)
		want := []string{"started", "response ping 1", "request pong 1", "request pong 2", "ended"}
		if len(got) != len(want) {
			t.Fatalf("announcements\nwanted:\n%v\ngot:\n%s", want, describeStreamEvents(got))
		}
		started, ok := got[0].(domain.StreamedPairStarted)
		if !ok || started.PairID != req.ID || started.Framings[domain.StreamDirectionRequest] != "grpc" {
			t.Fatalf("the first announcement\nwanted:\nstarted, pair %s, request framing grpc\ngot:\n%+v", req.ID, got[0])
		}
		for i, w := range want[1:4] {
			m, ok := got[i+1].(domain.StreamMessage)
			if !ok || m.PairID != req.ID || m.Direction+" "+string(m.Payload) != w || m.Framing != "grpc" {
				t.Errorf("announcement %d\nwanted:\nmessage %s\ngot:\n%+v", i+1, w, got[i+1])
			}
		}
		ended := got[4].(domain.StreamedPairEnded)
		response := ended.Directions[domain.StreamDirectionResponse]
		request := ended.Directions[domain.StreamDirectionRequest]
		if ended.PairID != req.ID || response.Ending != "complete" || request.Ending != "complete" || response.Messages != 1 || request.Messages != 2 {
			t.Fatalf("the end\nwanted:\nboth directions complete, 1 and 2 messages\ngot:\n%+v", ended)
		}
		if len(response.Trailers) != 2 || response.Trailers[0] != (domain.StreamTrailer{Name: "grpc-status", Value: "0"}) {
			t.Fatalf("the response's trailers\nwanted:\ngrpc-status 0, grpc-message\ngot:\n%+v", response.Trailers)
		}
	})

	t.Run("should announce a large message arriving slowly with byte counts at most every 100ms, then the message", func(t *testing.T) {
		var events streamEvents
		call := startGRPCCall(t, events.install(t))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		message := grpcEnvelope(0, strings.Repeat("x", 100<<10))
		for rest := message; len(rest) > 0; {
			n := min(len(rest), 4<<10)
			call.upstream.data(call.id, false, rest[:n])
			rest = rest[n:]
			time.Sleep(20 * time.Millisecond)
		}
		events.await(t, "the message", func(event any) bool { _, ok := event.(domain.StreamMessage); return ok })

		events.mu.Lock()
		defer events.mu.Unlock()
		var progress []domain.StreamMessageProgress
		var times []time.Time
		for i, event := range events.events {
			switch event := event.(type) {
			case domain.StreamMessageProgress:
				progress = append(progress, event)
				times = append(times, events.times[i])
			case domain.StreamMessage:
				if len(event.Payload) != 100<<10 {
					t.Fatalf("the message\nwanted:\n%d bytes\ngot:\n%d", 100<<10, len(event.Payload))
				}
			}
		}
		// 25 chunks 20ms apart: about 500ms of arriving.
		if len(progress) < 2 || len(progress) > 6 {
			t.Fatalf("progress announcements\nwanted:\n2 to 6\ngot:\n%d: %+v", len(progress), progress)
		}
		for i, p := range progress {
			if p.Direction != domain.StreamDirectionResponse || p.Framing != "grpc" || p.Bytes <= 0 || p.Bytes >= int64(len(message)) {
				t.Errorf("progress %d\nwanted:\nresponse grpc, 0 < bytes < %d\ngot:\n%+v", i, len(message), p)
			}
			if i == 0 {
				continue
			}
			// The handler runs a moment after the check it passed.
			if gap := times[i].Sub(times[i-1]); gap < 95*time.Millisecond || p.Bytes <= progress[i-1].Bytes {
				t.Errorf("progress %d\nwanted:\nat least 100ms after the last, more bytes\ngot:\n%s after, %d bytes after %d", i, gap, p.Bytes, progress[i-1].Bytes)
			}
		}
	})
}
