package service

// Event stream benchmarks. How to run them, the targets and the measured
// baseline are in docs/agents/benchmarks/events.md.

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// benchmarkTrafficRequest is a captured request as the proxy publishes it,
// with the metadata a real capture carries.
func benchmarkTrafficRequest(index int) domain.ProxyRequest {
	return domain.ProxyRequest{
		ID:     uuid.New(),
		Number: int64(index),
		Scheme: "https",
		Method: "POST",
		Host:   "api.example.com",
		Path:   fmt.Sprintf("/v2/accounts/%d/orders?include=items,shipping&page=%d", index, index%50),
		Metadata: map[string]any{
			"content-type":        "application/json",
			"content-length":      "1834",
			"user-agent":          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
			"cookies":             []string{"session", "csrftoken", "_ga", "_gid"},
			"in_scope":            true,
			"compass":             map[string]any{"matched": []string{"json-api", "auth-bearer"}, "score": 3},
			"extension.tags":      []string{"idor-candidate", "numeric-id"},
			"prettified-request":  strings.Repeat("p", 2048), // stripped before publishing
			"prettified-response": strings.Repeat("p", 2048), // stripped before publishing
		},
		RequestedAt: time.Date(2026, 11, 7, 12, 0, 0, index, time.UTC),
	}
}

// drainSubscribers starts one reader per subscriber and returns a function
// that waits until every queue is empty.
func drainSubscribers(broadcaster *eventBroadcaster, count int) (wait func(), stop func()) {
	subscribers := make([]*eventSubscriber, count)
	var group sync.WaitGroup
	for index := range subscribers {
		subscribers[index] = broadcaster.subscribe()
		group.Add(1)
		go func(subscriber *eventSubscriber) {
			defer group.Done()
			for range subscriber.events {
			}
		}(subscribers[index])
	}
	wait = func() {
		for _, subscriber := range subscribers {
			for len(subscriber.events) > 0 {
				runtime.Gosched()
			}
		}
	}
	stop = func() {
		broadcaster.close()
		group.Wait()
	}
	return wait, stop
}

// benchmarkPublish publishes realistic traffic.request events to count
// draining subscribers. It pauses the timer every 256 publishes until the
// queues drain, so no subscriber overflows.
func benchmarkPublish(b *testing.B, broadcaster *eventBroadcaster, count int) {
	requests := make([]domain.ProxyRequest, 256)
	for index := range requests {
		requests[index] = benchmarkTrafficRequest(index)
	}
	wait, stop := drainSubscribers(broadcaster, count)
	defer stop()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; b.Loop(); index++ {
		broadcaster.publishRequest(requests[index%len(requests)])
		if index%len(requests) == len(requests)-1 {
			b.StopTimer()
			wait()
			b.StartTimer()
		}
	}
}

func BenchmarkEvents(b *testing.B) {
	for _, count := range []int{0, 1, 10, 50} {
		b.Run(fmt.Sprintf("publish/subscribers=%d", count), func(b *testing.B) {
			benchmarkPublish(b, newEventBroadcaster(), count)
		})
	}

	b.Run("publish/buffer=full", func(b *testing.B) {
		broadcaster := newEventBroadcaster()
		for index := range eventReplayEvents {
			broadcaster.publishRequest(benchmarkTrafficRequest(index))
		}
		benchmarkPublish(b, broadcaster, 0)
	})

	for _, behind := range []int{100, 1000, 4096} {
		b.Run(fmt.Sprintf("resume/behind=%d", behind), func(b *testing.B) {
			benchmarkResume(b, behind)
		})
	}

	b.Run("buffer/memory", func(b *testing.B) {
		var held float64
		for b.Loop() {
			held = bufferMemory()
		}
		b.ReportMetric(held/(1<<20), "MiB")
	})
}

// benchmarkResume measures a client that is behind events behind,
// from sending GET /events to reading the last replayed event, over
// loopback. It reports the p95 as well as the mean.
func benchmarkResume(b *testing.B, behind int) {
	server := newTestServer(nil, func() {})
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	total := eventReplayEvents
	for index := range total {
		server.events.publishRequest(benchmarkTrafficRequest(index))
	}
	from := eventID(server.RunID(), uint64(total-behind))
	last := "id: " + eventID(server.RunID(), uint64(total)) + "\n"
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()

	var durations []time.Duration
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/events", nil)
		if err != nil {
			b.Fatal(err)
		}
		request.Header.Set("Last-Event-ID", from)
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		reader := bufio.NewReaderSize(response.Body, 64<<10)
		replayed := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				b.Fatalf("reading replay after %d events: %v", replayed, err)
			}
			if strings.HasPrefix(line, "id: ") {
				replayed++
				if line == last {
					break
				}
			}
		}
		durations = append(durations, time.Since(start))
		response.Body.Close()
		if replayed != behind {
			b.Fatalf("\nwanted:\n%d replayed\ngot:\n%d", behind, replayed)
		}
	}
	b.StopTimer()
	slices.Sort(durations)
	p95 := durations[(len(durations)*95+99)/100-1]
	b.ReportMetric(float64(p95.Microseconds())/1000, "p95-ms")
}

// bufferMemory returns the heap bytes a broadcaster holds once its replay
// buffer is full of realistic traffic events.
func bufferMemory() float64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	broadcaster := newEventBroadcaster()
	for index := range eventReplayEvents + 100 {
		if index%2 == 0 {
			broadcaster.publishRequest(benchmarkTrafficRequest(index))
		} else {
			broadcaster.publishResponse(domain.ProxyResponse{
				ID:          uuid.New(),
				Status:      "200 OK",
				StatusCode:  200,
				ContentType: "application/json; charset=utf-8",
				Length:      "1834",
				Metadata: map[string]any{
					"content-type": "application/json; charset=utf-8",
					"server":       "nginx/1.27.2",
					"set-cookie":   []string{"session", "csrftoken"},
					"timing_ms":    142,
				},
				RespondedAt: time.Date(2026, 11, 7, 12, 0, 1, index, time.UTC),
			})
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(broadcaster)
	return float64(after.HeapAlloc) - float64(before.HeapAlloc)
}
