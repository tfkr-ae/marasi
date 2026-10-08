package service

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// delayingWaypointRepository returns one snapshot, then waits, so an older
// reload can publish after a newer update on the unsynchronized path.
type delayingWaypointRepository struct {
	domain.WaypointRepository
	delayNext atomic.Bool
	entered   chan struct{}
	delay     time.Duration
}

func (repo *delayingWaypointRepository) GetWaypoints() ([]*domain.Waypoint, error) {
	items, err := repo.WaypointRepository.GetWaypoints()
	if repo.delayNext.CompareAndSwap(true, false) {
		close(repo.entered)
		time.Sleep(repo.delay)
	}
	return items, err
}

func TestProxiedTrafficFollowsPersistedWaypointDuringConcurrentUpdates(t *testing.T) {
	const hostname = "routed.example:80"
	origins := map[string]string{}
	for _, token := range []string{"origin-a", "origin-b", "origin-c"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, token)
		}))
		t.Cleanup(server.Close)
		parsed, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parsing origin URL: %v", err)
		}
		origins[parsed.Host] = token
	}
	address := func(token string) string {
		t.Helper()
		for host, candidate := range origins {
			if candidate == token {
				return host
			}
		}
		t.Fatalf("missing origin %s", token)
		return ""
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), logger)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}
	repository := db.NewProxyRepo(database)
	delaying := &delayingWaypointRepository{
		WaypointRepository: repository,
		entered:            make(chan struct{}),
		delay:              300 * time.Millisecond,
	}
	proxy, err := marasi.New(
		marasi.WithLogger(logger),
		marasi.WithDefaultRepositories(repository),
		marasi.WithWaypointRepository(delaying),
		marasi.WithBasePipeline(),
	)
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	proxy.AddRequestModifier(marasi.SetupRequestModifier)
	proxy.AddRequestModifier(marasi.OverrideWaypointsModifier)
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		_ = proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- proxy.Serve(listener) }()
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Errorf("closing proxy: %v", err)
		}
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})

	control := newTestServer(proxy, func() {})
	create := requestWaypoint(control, http.MethodPost, fmt.Sprintf(`{"hostname":%q,"override":%q}`, hostname, address("origin-a")))
	assertWaypointResponse(t, create, http.StatusOK, fmt.Sprintf("{\"items\":[{\"hostname\":%q,\"override\":%q}]}\n", hostname, address("origin-a")))
	if got := proxyWaypointBody(t, proxy, hostname); got != "origin-a" {
		t.Fatalf("\nwanted:\ninitial route origin-a\ngot:\n%s", got)
	}

	delaying.delayNext.Store(true)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- requestWaypointPath(control, http.MethodPost, "/waypoint/update", fmt.Sprintf(`{"hostname":%q,"override":%q}`, hostname, address("origin-b")))
	}()
	select {
	case <-delaying.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first waypoint update did not reload routes")
	}

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondDone <- requestWaypointPath(control, http.MethodPost, "/waypoint/update", fmt.Sprintf(`{"hostname":%q,"override":%q}`, hostname, address("origin-c")))
	}()
	stopRouting := make(chan struct{})
	routingDone := make(chan struct{})
	var routesMu sync.Mutex
	var routed []string
	routeErr := make(chan error, 1)
	go func() {
		defer close(routingDone)
		client := waypointProxyClient(proxy)
		for !waypointRoutingStopped(stopRouting) {
			response, err := client.Get("http://routed.example/ping")
			if err != nil {
				routeErr <- err
				return
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK {
				routeErr <- fmt.Errorf("proxied status %d: %s", response.StatusCode, body)
				return
			}
			token := string(body)
			if token != "origin-a" && token != "origin-b" && token != "origin-c" {
				routeErr <- fmt.Errorf("proxied request reached %q", token)
				return
			}
			routesMu.Lock()
			routed = append(routed, token)
			routesMu.Unlock()
		}
	}()

	var first, second *httptest.ResponseRecorder
	select {
	case first = <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first waypoint update did not finish")
	}
	select {
	case second = <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second waypoint update did not finish")
	}
	close(stopRouting)
	<-routingDone
	select {
	case err := <-routeErr:
		t.Fatalf("routing during updates: %v", err)
	default:
	}
	routesMu.Lock()
	seen := append([]string(nil), routed...)
	routesMu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no proxied request completed while updates ran")
	}
	rank := map[string]int{"origin-a": 1, "origin-b": 2, "origin-c": 3}
	previous := 0
	for _, token := range seen {
		if rank[token] < previous {
			t.Fatalf("\nwanted:\nroutes move only to a newer persisted override while updates run\ngot:\n%v", seen)
		}
		previous = rank[token]
	}
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("\nwanted:\nboth updates accepted\ngot:\n%d %s\n%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}

	stored, err := repository.GetWaypoints()
	if err != nil {
		t.Fatalf("reading persisted waypoints: %v", err)
	}
	if len(stored) != 1 || stored[0].Hostname != hostname {
		t.Fatalf("\nwanted:\npersisted %s\ngot:\n%v", hostname, stored)
	}
	want := origins[stored[0].Override]
	if got := proxyWaypointBody(t, proxy, hostname); got != want {
		t.Fatalf("\nwanted:\nproxied request routed to persisted override %s (%s)\ngot:\n%s", stored[0].Override, want, got)
	}
}

func proxyWaypointBody(t *testing.T, proxy *marasi.Proxy, hostname string) string {
	t.Helper()
	host, _, err := net.SplitHostPort(hostname)
	if err != nil {
		t.Fatalf("splitting waypoint hostname: %v", err)
	}
	response, err := waypointProxyClient(proxy).Get("http://" + host + "/ping")
	if err != nil {
		t.Fatalf("proxying request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading proxied response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("\nwanted:\n200 from routed origin\ngot:\n%d %s", response.StatusCode, body)
	}
	return string(body)
}

func waypointRoutingStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func waypointProxyClient(proxy *marasi.Proxy) *http.Client {
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(proxy.Addr, proxy.Port)}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   2 * time.Second,
	}
}
