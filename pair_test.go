package marasi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/core"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestModifierGroup(t *testing.T) {
	t.Run("should run modifiers in the order they were added", func(t *testing.T) {
		var ran []string
		group := NewModifierGroup()
		for _, name := range []string{"first", "second", "third"} {
			group.Add(func(*Proxy, *Pair) error {
				ran = append(ran, name)
				return nil
			})
		}

		pair := NewTestPair(httptest.NewRequest(http.MethodGet, "http://marasi.app", nil))
		if err := group.Modify(&Proxy{}, pair); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		want := []string{"first", "second", "third"}
		if !reflect.DeepEqual(want, ran) {
			t.Fatalf("wanted:\n%v\ngot:\n%v", want, ran)
		}
	})

	t.Run("should stop at the first error and return it", func(t *testing.T) {
		broke := errors.New("broke")
		var ran []string
		group := NewModifierGroup()
		group.Add(func(*Proxy, *Pair) error { ran = append(ran, "first"); return nil })
		group.Add(func(*Proxy, *Pair) error { ran = append(ran, "second"); return broke })
		group.Add(func(*Proxy, *Pair) error { ran = append(ran, "third"); return nil })

		pair := NewTestPair(httptest.NewRequest(http.MethodGet, "http://marasi.app", nil))
		if err := group.Modify(&Proxy{}, pair); !errors.Is(err, broke) {
			t.Fatalf("wanted: %v\ngot: %v", broke, err)
		}

		want := []string{"first", "second"}
		if !reflect.DeepEqual(want, ran) {
			t.Fatalf("wanted:\n%v\ngot:\n%v", want, ran)
		}
	})

	t.Run("should change nothing when the group is empty", func(t *testing.T) {
		pair := NewTestPair(httptest.NewRequest(http.MethodGet, "http://marasi.app", nil))
		if err := NewModifierGroup().Modify(&Proxy{}, pair); err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
	})
}

func TestNewTestPair(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://marasi.app", nil)
	pair := NewTestPair(req)

	if pair.Request != req {
		t.Fatalf("wanted the pair to carry the request it was built from")
	}
	if pair.ID.Version() != 7 {
		t.Fatalf("wanted a version 7 pair UUID\ngot: %v", pair.ID)
	}
	if pair.Metadata == nil {
		t.Fatalf("wanted metadata ready to write")
	}
	if pair.Timing.Requested.IsZero() {
		t.Fatalf("wanted the request time set")
	}
	if pair.Route == nil {
		t.Fatalf("wanted a route record")
	}
	if pair.Context() != req.Context() {
		t.Fatalf("wanted the pair's context to be the request's")
	}
	if pair.Response != nil || pair.LaunchpadID != uuid.Nil || pair.ArmoryRunID != uuid.Nil {
		t.Fatalf("wanted no response and no launch or Armory correlation\ngot: %+v", pair)
	}
}

// servePairProxy serves a proxy whose chains are built by configure, and returns
// a client that sends through it.
func servePairProxy(t *testing.T, configure func(*Proxy)) *http.Client {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(database)
	proxy, err := New(
		WithLogger(logger),
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithBasePipeline(),
	)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	configure(proxy)
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- proxy.Serve(listener) }()
	t.Cleanup(func() {
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
			t.Error("proxy serve did not stop")
		}
	})
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(proxy.Addr, proxy.Port)}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 10 * time.Second}
}

func TestPairModifierAdapter(t *testing.T) {
	t.Run("should run old and new modifiers in one chain in their registered order, sharing the pair's values", func(t *testing.T) {
		originSaw := make(chan http.Header, 1)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originSaw <- r.Header.Clone()
			_, _ = io.WriteString(w, "origin")
		}))
		defer origin.Close()

		runID := uuid.Must(uuid.NewV7())
		var mu sync.Mutex
		var ran []string
		record := func(name string) {
			mu.Lock()
			defer mu.Unlock()
			ran = append(ran, name)
		}
		var setupID, requestPairID, responsePairID uuid.UUID
		var requestErrs []string
		fail := func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			requestErrs = append(requestErrs, fmt.Sprintf(format, args...))
		}

		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestModifier(func(proxy *Proxy, req *http.Request) error {
				record("old setup")
				if err := SetupRequestModifier(proxy, req); err != nil {
					return err
				}
				setupID, _ = core.RequestIDFromContext(req.Context())
				return nil
			})
			proxy.AddRequestPairModifier(func(proxy *Proxy, pair *Pair) error {
				record("new request")
				requestPairID = pair.ID
				if pair.Metadata == nil || pair.Route == nil || pair.Timing.Requested.IsZero() {
					fail("pair not loaded from the request: %+v", pair)
				}
				pair.Metadata["new"] = "request"
				pair.ArmoryRunID = runID
				pair.Request.Header.Set("X-New-Request", "true")
				return nil
			})
			proxy.AddRequestModifier(func(proxy *Proxy, req *http.Request) error {
				record("old request")
				if metadata, _ := core.MetadataFromContext(req.Context()); metadata["new"] != "request" {
					fail("old modifier metadata: %v", metadata)
				}
				if got, _ := core.ArmoryRunIDFromContext(req.Context()); got != runID {
					fail("old modifier Armory run: %v", got)
				}
				return nil
			})
			proxy.AddResponseModifier(func(proxy *Proxy, res *http.Response) error {
				record("old response")
				res.Request = core.ContextWithResponseTime(res.Request, time.Now())
				return nil
			})
			proxy.AddResponsePairModifier(func(proxy *Proxy, pair *Pair) error {
				record("new response")
				responsePairID = pair.ID
				if pair.Response == nil || pair.Timing.Responded.IsZero() || pair.ArmoryRunID != runID {
					fail("pair not loaded from the response: %+v", pair)
				}
				pair.Response.Header.Set("X-New-Response", "true")
				return nil
			})
			proxy.AddResponseModifier(func(proxy *Proxy, res *http.Response) error {
				record("old response after")
				return nil
			})
		})

		response, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("sending through proxy: %v", err)
		}
		defer response.Body.Close()
		if _, err := io.ReadAll(response.Body); err != nil {
			t.Fatalf("reading response: %v", err)
		}

		if got := (<-originSaw).Get("X-New-Request"); got != "true" {
			t.Fatalf("wanted the origin to see the new modifier's header\ngot: %q", got)
		}
		if got := response.Header.Get("X-New-Response"); got != "true" {
			t.Fatalf("wanted the client to see the new modifier's header\ngot: %q", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(requestErrs) > 0 {
			t.Fatalf("modifiers saw the wrong values:\n%s", strings.Join(requestErrs, "\n"))
		}
		want := []string{"old setup", "new request", "old request", "old response", "new response", "old response after"}
		if !reflect.DeepEqual(want, ran) {
			t.Fatalf("wanted:\n%v\ngot:\n%v", want, ran)
		}
		if setupID == uuid.Nil || requestPairID != setupID || responsePairID != setupID {
			t.Fatalf("wanted one pair UUID on both chains\nsetup: %v\nrequest pair: %v\nresponse pair: %v", setupID, requestPairID, responsePairID)
		}
	})

	t.Run("should send a request the new modifier replaces upstream with the pair's values kept", func(t *testing.T) {
		originSaw := make(chan string, 1)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originSaw <- r.URL.Path
		}))
		defer origin.Close()

		var oldSawID uuid.UUID
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestModifier(SetupRequestModifier)
			proxy.AddRequestPairModifier(func(proxy *Proxy, pair *Pair) error {
				replaced := pair.Request.Clone(context.Background())
				replaced.URL.Path = "/replaced"
				pair.Request = replaced
				return nil
			})
			proxy.AddRequestModifier(func(proxy *Proxy, req *http.Request) error {
				oldSawID, _ = core.RequestIDFromContext(req.Context())
				return nil
			})
		})

		response, err := client.Get(origin.URL + "/original")
		if err != nil {
			t.Fatalf("sending through proxy: %v", err)
		}
		response.Body.Close()
		if got := <-originSaw; got != "/replaced" {
			t.Fatalf("wanted: /replaced\ngot: %s", got)
		}
		if oldSawID == uuid.Nil {
			t.Fatalf("wanted the pair UUID kept on the replaced request")
		}
	})
}

func TestOverrideWaypointsModifier_ThroughDefaultPipeline(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "waypoint origin")
	}))
	defer origin.Close()
	destination := strings.TrimPrefix(origin.URL, "http://")

	captured := make(chan domain.ProxyRequest, 1)
	client := servePairProxy(t, func(proxy *Proxy) {
		defaults, err := proxy.ExtensionRepo.GetExtensions()
		if err != nil {
			t.Fatalf("getting default extensions: %v", err)
		}
		if err := proxy.WithOptions(
			WithExtensions(defaults),
			WithRequestHandler(func(request domain.ProxyRequest) error {
				captured <- request
				return nil
			}),
			WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
			WithDefaultModifierPipeline(),
		); err != nil {
			t.Fatalf("configuring proxy: %v", err)
		}
		proxy.Waypoints = map[string]string{"waypoint.example:80": destination}
	})

	response, err := client.Get("http://waypoint.example/")
	if err != nil {
		t.Fatalf("sending through proxy: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if string(body) != "waypoint origin" {
		t.Fatalf("wanted: waypoint origin\ngot: %s", body)
	}

	select {
	case request := <-captured:
		if request.Metadata["original_host"] != "waypoint.example:80" || request.Metadata["override_host"] != destination {
			t.Fatalf("wanted the captured pair to record the waypoint\ngot: %v", request.Metadata)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request was not captured")
	}
}
