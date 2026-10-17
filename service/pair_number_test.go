package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// capturingService is a service whose real proxy captures into a real
// project, with a client that sends traffic through the proxy.
type capturingService struct {
	server     *Server
	repository *db.Repository
	client     *http.Client
	origin     *httptest.Server
	mu         sync.Mutex
	// events are the traffic.request event bodies, by pair id.
	events map[uuid.UUID]json.RawMessage
}

func newCapturingService(t *testing.T) *capturingService {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	t.Cleanup(origin.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}
	repository := db.NewProxyRepo(database)
	extensions, err := repository.GetExtensions()
	if err != nil {
		t.Fatalf("getting default extensions: %v", err)
	}
	proxy, err := marasi.New(
		marasi.WithLogger(logger),
		marasi.WithDefaultRepositories(repository),
		marasi.WithExtensions(extensions),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
	)
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	service := &capturingService{
		server:     newTestServer(proxy, func() {}),
		repository: repository,
		origin:     origin,
		events:     map[uuid.UUID]json.RawMessage{},
	}
	subscriber := service.server.events.subscribe()
	err = proxy.WithOptions(marasi.WithResponseHandler(func(domain.ProxyResponse) error { return nil }), marasi.WithRequestHandler(func(request domain.ProxyRequest) error {
		// Held across the publish, so the event read is this request's.
		service.mu.Lock()
		defer service.mu.Unlock()
		err := service.server.HandleRequest(request)
		event := <-subscriber.events
		service.events[request.ID] = event.data
		return err
	}))
	if err != nil {
		t.Fatalf("setting the request handler: %v", err)
	}
	lifecycle := NewListenerLifecycle(proxy, io.Discard)
	t.Cleanup(func() {
		if err := lifecycle.Shutdown(); err != nil {
			t.Fatalf("shutting down lifecycle: %v", err)
		}
	})
	started, err := lifecycle.Start(context.Background(), listenerSettings("127.0.0.1", 0))
	if err != nil {
		t.Fatalf("starting listener: %v", err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: statusAddress(t, started)}), MaxIdleConnsPerHost: 64}
	t.Cleanup(transport.CloseIdleConnections)
	// No per-request timeout: the proxy's writer queue holds 10 writes, so
	// once it is full each capture waits for the writer. Under -race on a
	// loaded machine 1000 pairs take over 10 s, and a request queued behind
	// them is slow, not lost. captureBudget bounds the whole test instead.
	service.client = &http.Client{Transport: transport}
	return service
}

// captureBudget bounds how long a capturing test may take to send its
// traffic and see it stored.
const captureBudget = 5 * time.Minute

// capture sends one request through the proxy.
func (s *capturingService) capture(ctx context.Context, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.origin.URL+path, nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	return err
}

// waitForPairs waits until the project holds n pairs and n events arrived.
func (s *capturingService) waitForPairs(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(captureBudget)
	for {
		summaries, err := s.repository.GetRequestResponseSummary()
		s.mu.Lock()
		events := len(s.events)
		s.mu.Unlock()
		if err == nil && len(summaries) == n && events == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %d pairs: %d stored, %d events, %v", n, len(summaries), events, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// eventNumber returns the number in a pair's traffic.request event.
func (s *capturingService) eventNumber(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	s.mu.Lock()
	data := s.events[id]
	s.mu.Unlock()
	var body struct {
		Number *int64 `json:"number"`
	}
	if err := json.Unmarshal(data, &body); err != nil || body.Number == nil {
		t.Fatalf("traffic.request event %s has no number: %v", data, err)
	}
	return *body.Number
}

func TestPairNumbers(t *testing.T) {
	t.Run("should carry the same number in the request event, the list row and the single read", func(t *testing.T) {
		service := newCapturingService(t)
		for _, path := range []string{"/first", "/second"} {
			if err := service.capture(t.Context(), path); err != nil {
				t.Fatalf("capturing %s: %v", path, err)
			}
		}
		service.waitForPairs(t, 2)

		response := requestListener(t, service.server, http.MethodGet, "/traffic?limit=10", "")
		var page struct {
			Items []struct {
				ID     uuid.UUID `json:"id"`
				Path   string    `json:"path"`
				Number *int64    `json:"number"`
			} `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Items) != 2 {
			t.Fatalf("GET /traffic = %d %s, %v", response.Code, response.Body.String(), err)
		}
		for i, item := range page.Items {
			if want := int64(i + 1); item.Number == nil || *item.Number != want {
				t.Errorf("GET /traffic number of %s = %v, want %d", item.Path, item.Number, want)
			}
			if got := service.eventNumber(t, item.ID); item.Number != nil && got != *item.Number {
				t.Errorf("traffic.request number of %s = %d, want %d", item.Path, got, *item.Number)
			}
			detail := requestListener(t, service.server, http.MethodGet, "/traffic/"+item.ID.String(), "")
			var body struct {
				Number *int64 `json:"number"`
			}
			if err := json.Unmarshal(detail.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET /traffic/%s = %d %s", item.ID, detail.Code, detail.Body.String())
			}
			if body.Number == nil || item.Number == nil || *body.Number != *item.Number {
				t.Errorf("GET /traffic/%s number = %v, want %v", item.ID, body.Number, item.Number)
			}
		}
	})

	t.Run("should give 1000 concurrently captured pairs unique numbers that match their events", func(t *testing.T) {
		service := newCapturingService(t)
		ctx, cancel := context.WithTimeout(t.Context(), captureBudget)
		defer cancel()
		const pairs = 1000
		paths := make(chan string, pairs)
		for i := range pairs {
			paths <- fmt.Sprintf("/%d", i)
		}
		close(paths)
		errs := make(chan error, pairs)
		var wg sync.WaitGroup
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for path := range paths {
					errs <- service.capture(ctx, path)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("capturing: %v", err)
			}
		}
		service.waitForPairs(t, pairs)

		summaries, err := service.repository.GetRequestResponseSummary()
		if err != nil {
			t.Fatalf("GetRequestResponseSummary() error = %v", err)
		}
		seen := make(map[int64]bool, pairs)
		for _, summary := range summaries {
			if seen[summary.Number] {
				t.Fatalf("number %d stored twice", summary.Number)
			}
			seen[summary.Number] = true
			if summary.Number < 1 || summary.Number > pairs {
				t.Errorf("number %d is outside 1..%d", summary.Number, pairs)
			}
			if got := service.eventNumber(t, summary.ID); got != summary.Number {
				t.Errorf("event number of %s = %d, stored %d", summary.ID, got, summary.Number)
			}
		}
	})
}
