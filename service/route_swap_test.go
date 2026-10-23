package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
)

// connectionOrigin is an origin that remembers which connection served each
// path, and holds /slow until released.
type connectionOrigin struct {
	server  *httptest.Server
	mu      sync.Mutex
	served  map[string]string
	entered chan struct{}
	release chan struct{}
}

func newConnectionOrigin(t *testing.T) *connectionOrigin {
	t.Helper()
	origin := &connectionOrigin{
		served:  map[string]string{},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	origin.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin.mu.Lock()
		origin.served[r.URL.Path] = r.RemoteAddr
		origin.mu.Unlock()
		if r.URL.Path == "/slow" {
			close(origin.entered)
			<-origin.release
		}
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	origin.server.Start()
	t.Cleanup(origin.server.Close)
	return origin
}

func (o *connectionOrigin) connection(path string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.served[path]
}

// get sends one request through the proxy and returns the body.
func (s *capturingService) get(ctx context.Context, url string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return string(body), err
}

func TestRouteSwap(t *testing.T) {
	t.Run("should open a new connection after a swap while a request in flight on an old connection completes", func(t *testing.T) {
		service := newCapturingService(t)
		origin := newConnectionOrigin(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		// One old-route connection carries /slow through the swap, and
		// another sits idle across it.
		slow := make(chan error, 1)
		go func() {
			body, err := service.get(ctx, origin.server.URL+"/slow")
			if err == nil && body != "/slow" {
				err = fmt.Errorf("wanted:\n/slow\ngot:\n%s", body)
			}
			slow <- err
		}()
		select {
		case <-origin.entered:
		case <-ctx.Done():
			t.Fatal("/slow never reached the origin")
		}
		if _, err := service.get(ctx, origin.server.URL+"/idle"); err != nil {
			t.Fatalf("sending /idle: %v", err)
		}

		service.server.proxy.SetRoute(marasi.DirectRoute())

		// /fresh asks the transport for a connection after the swap and
		// before /slow finishes, so the transport stops closing connections
		// that turn idle on its own.
		if _, err := service.get(ctx, origin.server.URL+"/fresh"); err != nil {
			t.Fatalf("sending /fresh: %v", err)
		}
		close(origin.release)
		if err := <-slow; err != nil {
			t.Fatalf("the request in flight across the swap failed: %v", err)
		}
		if _, err := service.get(ctx, origin.server.URL+"/next"); err != nil {
			t.Fatalf("sending /next: %v", err)
		}

		old := []string{origin.connection("/slow"), origin.connection("/idle")}
		for _, path := range []string{"/fresh", "/next"} {
			got := origin.connection(path)
			for _, stale := range old {
				if got == stale {
					t.Fatalf("\nwanted:\n%s on a connection opened after the swap\ngot:\n%s on old connection %s", path, path, stale)
				}
			}
		}
	})
}
