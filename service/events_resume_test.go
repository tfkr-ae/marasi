package service

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// streamFrame is one parsed SSE frame.
type streamFrame struct {
	id   string
	name string
	data string
}

// connectResumedStream opens GET /events with an optional Last-Event-ID
// header and last_event_id query parameter, and reads the connected comment.
// An empty argument is left out.
func connectResumedStream(t *testing.T, base, header, query string) (*http.Response, *bufio.Reader) {
	t.Helper()
	target := base + "/events"
	if query != "" {
		target += "?last_event_id=" + url.QueryEscape(query)
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("creating events request: %v", err)
	}
	if header != "" {
		request.Header.Set("Last-Event-ID", header)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("connecting to events: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("\nwanted:\n200\ngot:\n%d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	if got := readRawEventFrame(t, reader); got != ": connected\n\n" {
		response.Body.Close()
		t.Fatalf("\nwanted:\n: connected\ngot:\n%s", got)
	}
	return response, reader
}

// readStreamFrame reads and parses one SSE frame.
func readStreamFrame(t *testing.T, reader *bufio.Reader) streamFrame {
	t.Helper()
	var frame streamFrame
	for _, line := range strings.Split(strings.TrimSuffix(readRawEventFrame(t, reader), "\n\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "id: "):
			frame.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			frame.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			frame.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return frame
}

// publishNumbered publishes count "n" events whose data is {"n":first..}.
func publishNumbered(broadcaster *eventBroadcaster, first, count int) {
	for index := range count {
		broadcaster.publish("n", map[string]int{"n": first + index})
	}
}

// requireNumbered reads count frames and requires them to be the "n" events
// first.. in order, each with the id of its own seq.
func requireNumbered(t *testing.T, reader *bufio.Reader, runID string, first, count int) streamFrame {
	t.Helper()
	var frame streamFrame
	for index := range count {
		frame = readStreamFrame(t, reader)
		want := streamFrame{id: eventID(runID, uint64(first+index)), name: "n", data: fmt.Sprintf(`{"n":%d}`, first+index)}
		if frame != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, frame)
		}
	}
	return frame
}

// gatedEventWrites serves handler but blocks every write after the first
// (the connected comment) until gate closes.
func gatedEventWrites(handler http.Handler, gate <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&gatedWriter{ResponseWriter: w, gate: gate}, r)
	})
}

// gatedWriter is an http.ResponseWriter whose writes after the first wait for gate.
type gatedWriter struct {
	http.ResponseWriter
	gate   <-chan struct{}
	writes int
}

func (w *gatedWriter) Write(data []byte) (int, error) {
	if w.writes++; w.writes > 1 {
		<-w.gate
	}
	return w.ResponseWriter.Write(data)
}

func (w *gatedWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

func TestEventStreamResume(t *testing.T) {
	t.Run("should replay every missed event in order and then continue live", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		publishNumbered(server.events, 1, 3)
		last := requireNumbered(t, reader, server.RunID(), 1, 3)
		response.Body.Close()
		waitForSubscriberCount(t, server.events, 0)

		publishNumbered(server.events, 4, 5)
		response, reader = connectResumedStream(t, httpServer.URL, last.id, "")
		defer response.Body.Close()
		requireNumbered(t, reader, server.RunID(), 4, 5)
		publishNumbered(server.events, 9, 1)
		requireNumbered(t, reader, server.RunID(), 9, 1)
	})

	t.Run("should replay nothing for the newest id", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		publishNumbered(server.events, 1, 2)

		response, reader := connectResumedStream(t, httpServer.URL, eventID(server.RunID(), 2), "")
		defer response.Body.Close()
		publishNumbered(server.events, 3, 1)
		requireNumbered(t, reader, server.RunID(), 3, 1)
	})

	t.Run("should resume two clients from different positions", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		publishNumbered(server.events, 1, 10)

		early, earlyReader := connectResumedStream(t, httpServer.URL, eventID(server.RunID(), 2), "")
		defer early.Body.Close()
		late, lateReader := connectResumedStream(t, httpServer.URL, eventID(server.RunID(), 7), "")
		defer late.Body.Close()
		publishNumbered(server.events, 11, 1)

		requireNumbered(t, earlyReader, server.RunID(), 3, 9)
		requireNumbered(t, lateReader, server.RunID(), 8, 4)
	})

	t.Run("should take the header over the query parameter", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		publishNumbered(server.events, 1, 5)

		response, reader := connectResumedStream(t, httpServer.URL, eventID(server.RunID(), 3), eventID(server.RunID(), 1))
		defer response.Body.Close()
		requireNumbered(t, reader, server.RunID(), 4, 2)
	})

	t.Run("should resume from the query parameter without a header", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		publishNumbered(server.events, 1, 5)

		response, reader := connectResumedStream(t, httpServer.URL, "", eventID(server.RunID(), 4))
		defer response.Body.Close()
		requireNumbered(t, reader, server.RunID(), 5, 1)
	})

	for _, test := range []struct {
		name   string
		id     func(server *Server) string
		reason string
	}{
		{name: "an id older than the buffer", reason: "expired", id: func(server *Server) string { return eventID(server.RunID(), 1) }},
		{name: "an id from another run", reason: "restarted", id: func(*Server) string { return "0193802f-f0e7-73d9-a764-06d21e367809.4100" }},
		{name: "an id without a sequence", reason: "invalid", id: func(*Server) string { return "garbage" }},
		{name: "an id with a bad sequence", reason: "invalid", id: func(server *Server) string { return server.RunID() + ".x" }},
		{name: "an id that was never issued", reason: "invalid", id: func(server *Server) string { return eventID(server.RunID(), 99999) }},
	} {
		t.Run("should reset without a partial replay for "+test.name, func(t *testing.T) {
			server := newTestServer(nil, func() {})
			httpServer := httptest.NewServer(server)
			defer httpServer.Close()
			total := eventReplayEvents + 2
			publishNumbered(server.events, 1, total)

			response, reader := connectResumedStream(t, httpServer.URL, test.id(server), "")
			defer response.Body.Close()
			reset := readStreamFrame(t, reader)
			want := streamFrame{id: eventID(server.RunID(), uint64(total)), name: "stream.reset", data: `{"reason":"` + test.reason + `"}`}
			if reset != want {
				t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, reset)
			}
			publishNumbered(server.events, total+1, 1)
			requireNumbered(t, reader, server.RunID(), total+1, 1)

			// The reset's id is a position the client can resume from.
			response.Body.Close()
			response, reader = connectResumedStream(t, httpServer.URL, reset.id, "")
			defer response.Body.Close()
			requireNumbered(t, reader, server.RunID(), total+1, 1)
		})
	}

	t.Run("should reset with an id before anything was published", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()

		response, reader := connectResumedStream(t, httpServer.URL, "other.1", "")
		defer response.Body.Close()
		want := streamFrame{id: eventID(server.RunID(), 0), name: "stream.reset", data: `{"reason":"restarted"}`}
		if got := readStreamFrame(t, reader); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should send no reset without an id", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		publishNumbered(server.events, 1, 3)

		response, reader := connectResumedStream(t, httpServer.URL, "", "")
		defer response.Body.Close()
		publishNumbered(server.events, 4, 1)
		requireNumbered(t, reader, server.RunID(), 4, 1)
	})

	t.Run("should drop an overflowing client with a final event and let it resume", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		subscriber, _ := server.events.resume("")
		total := eventQueueSize + 10
		publishNumbered(server.events, 1, total)

		var received []serviceEvent
		for event := range subscriber.events {
			received = append(received, event)
		}
		if got := len(received); got != eventQueueSize {
			t.Fatalf("\nwanted:\n%d queued events\ngot:\n%d", eventQueueSize, got)
		}
		dropped := received[len(received)-1]
		if dropped.name != "stream.dropped" || string(dropped.data) != `{"reason":"overflow"}` || dropped.seq != 0 {
			t.Fatalf("\nwanted:\nstream.dropped {\"reason\":\"overflow\"} without an id\ngot:\n%s %s seq %d", dropped.name, dropped.data, dropped.seq)
		}
		lastSeen := received[len(received)-2].seq
		if lastSeen != eventQueueSize-1 {
			t.Fatalf("\nwanted:\nlast seq %d\ngot:\n%d", eventQueueSize-1, lastSeen)
		}

		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectResumedStream(t, httpServer.URL, eventID(server.RunID(), lastSeen), "")
		defer response.Body.Close()
		requireNumbered(t, reader, server.RunID(), int(lastSeen)+1, total-int(lastSeen))
	})

	t.Run("should write the drop event without an id and close the stream", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		gate := make(chan struct{})
		httpServer := httptest.NewServer(gatedEventWrites(server, gate))
		defer httpServer.Close()
		response, reader := connectResumedStream(t, httpServer.URL, "", "")
		defer response.Body.Close()
		// The handler blocks writing the first event, so its queue overflows.
		total := eventQueueSize + 200
		publishNumbered(server.events, 1, total)
		close(gate)
		var last streamFrame
		for {
			frame := readStreamFrame(t, reader)
			if frame.name == "stream.dropped" {
				if frame.id != "" || frame.data != `{"reason":"overflow"}` {
					t.Fatalf("\nwanted:\nstream.dropped without an id\ngot:\n%+v", frame)
				}
				break
			}
			last = frame
		}
		if _, err := reader.ReadString('\n'); err == nil {
			t.Fatal("\nwanted:\nclosed stream after stream.dropped\ngot:\nmore data")
		}
		response.Body.Close()
		lastSeq, err := strconv.Atoi(strings.TrimPrefix(last.id, server.RunID()+"."))
		if err != nil || lastSeq >= total {
			t.Fatalf("\nwanted:\nan id before %d\ngot:\n%q", total, last.id)
		}

		response, reader = connectResumedStream(t, httpServer.URL, last.id, "")
		defer response.Body.Close()
		for seq := lastSeq + 1; seq <= total; seq++ {
			if got := readStreamFrame(t, reader); got.id != eventID(server.RunID(), uint64(seq)) {
				t.Fatalf("\nwanted:\nid %s\ngot:\nid %s", eventID(server.RunID(), uint64(seq)), got.id)
			}
		}
	})
}

func TestEventBroadcasterResume(t *testing.T) {
	t.Run("should neither lose nor repeat events when a resume races publishes", func(t *testing.T) {
		const total = 5000
		for attempt := range 20 {
			broadcaster := newEventBroadcaster()
			// Resume while publishing is under way, at a varying point.
			resumeAt := uint64(100 + attempt*37)
			// received counts events the client holds, starting at the
			// resume point, so the publisher can run past it before the
			// resume and still stays inside the live queue after it.
			var received atomic.Int64
			received.Store(int64(resumeAt))
			published := make(chan struct{})
			go func() {
				defer close(published)
				for seq := 1; seq <= total; seq++ {
					// Stay inside the live queue so the test checks ordering, not overflow.
					for int64(seq)-received.Load() > eventQueueSize/2 {
						runtime.Gosched()
					}
					broadcaster.publish("n", seq)
				}
			}()
			for {
				broadcaster.mu.Lock()
				seq := broadcaster.seq
				broadcaster.mu.Unlock()
				if seq >= resumeAt+50 {
					break
				}
				runtime.Gosched()
			}
			subscriber, resume := broadcaster.resume(eventID(broadcaster.runID, resumeAt))
			if resume.reset != "" {
				t.Fatalf("\nwanted:\nno reset\ngot:\n%s", resume.reset)
			}
			want := resumeAt + 1
			for _, event := range resume.replay {
				if event.seq != want {
					t.Fatalf("\nwanted:\nreplayed seq %d\ngot:\n%d", want, event.seq)
				}
				want++
				received.Add(1)
			}
			for want <= total {
				select {
				case event := <-subscriber.events:
					if event.seq != want {
						t.Fatalf("\nwanted:\nlive seq %d\ngot:\n%d (%s)", want, event.seq, event.name)
					}
					want++
					received.Add(1)
				case <-time.After(5 * time.Second):
					t.Fatalf("timed out at seq %d", want)
				}
			}
			<-published
			broadcaster.close()
		}
	})

	t.Run("should evict the oldest events past the byte limit", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		chunk := strings.Repeat("x", 1<<20)
		for range 20 {
			broadcaster.publish("big", chunk)
		}
		broadcaster.mu.Lock()
		count, bytes := broadcaster.replay.count, broadcaster.replay.bytes
		broadcaster.mu.Unlock()
		if bytes > eventReplayBytes || count != eventReplayBytes/(len(chunk)+2) {
			t.Fatalf("\nwanted:\n%d events within %d bytes\ngot:\n%d events, %d bytes", eventReplayBytes/(len(chunk)+2), eventReplayBytes, count, bytes)
		}
		_, resume := broadcaster.resume(eventID(broadcaster.runID, uint64(20-count-1)))
		if resume.reset != resetExpired {
			t.Fatalf("\nwanted:\nexpired\ngot:\n%q", resume.reset)
		}
		_, resume = broadcaster.resume(eventID(broadcaster.runID, uint64(20-count)))
		if resume.reset != "" || len(resume.replay) != count {
			t.Fatalf("\nwanted:\n%d replayed\ngot:\nreset %q, %d replayed", count, resume.reset, len(resume.replay))
		}
	})

	t.Run("should keep the newest event even past the byte limit", func(t *testing.T) {
		broadcaster := newEventBroadcaster()
		broadcaster.publish("small", 1)
		broadcaster.publish("huge", strings.Repeat("x", eventReplayBytes+1))
		_, resume := broadcaster.resume(eventID(broadcaster.runID, 1))
		if resume.reset != "" || len(resume.replay) != 1 || resume.replay[0].name != "huge" {
			t.Fatalf("\nwanted:\nthe huge event replayed\ngot:\nreset %q, %d replayed", resume.reset, len(resume.replay))
		}
	})
}
