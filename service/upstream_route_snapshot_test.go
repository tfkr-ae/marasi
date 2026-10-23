package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/upstreamtest"
)

// pairRoute is the upstream_proxy field of a pair read.
type pairRoute struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// respondedPair is the part of GET /traffic/{id} the route tests read.
type respondedPair struct {
	UpstreamProxy *pairRoute `json:"upstream_proxy"`
	Response      struct {
		RespondedAt *time.Time `json:"responded_at"`
	} `json:"response"`
}

// route waits until the only pair at path has its response stored and
// returns the pair's id and route.
func (s *upstreamService) route(t *testing.T, path string) (string, *pairRoute) {
	t.Helper()
	id := s.pairID(t, path)
	deadline := time.Now().Add(10 * time.Second)
	for {
		pair := decodeJSON[respondedPair](t, s.mustCall(t, http.MethodGet, "/traffic/"+id, ""))
		if pair.Response.RespondedAt != nil {
			return id, pair.UpstreamProxy
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pair at %s got no response", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// wantRoute fails unless got is the route name at address.
func wantRoute(t *testing.T, got *pairRoute, name, address string) {
	t.Helper()
	if got == nil || got.Name != name || got.Address != address {
		t.Fatalf("\nwanted:\n%q %q\ngot:\n%+v", name, address, got)
	}
}

func TestPairRouteSnapshot(t *testing.T) {
	t.Run("should record the upstream proxy that carried a pair, without credentials, and direct as an empty name", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t, upstreamtest.WithCredentials("alice", "s3cret"))
		service.addUpstreamProxy(t, "corp", fake.URL("http", "alice", "s3cret"))

		service.get(t, service.origin.URL+"/direct?close")
		service.use(t, "corp")
		service.get(t, service.origin.URL+"/through")

		_, direct := service.route(t, "/direct?close")
		wantRoute(t, direct, "", "")
		_, through := service.route(t, "/through")
		wantRoute(t, through, "corp", fmt.Sprintf("http://%s", fake.Addr))
	})

	t.Run("should record a SOCKS5 upstream proxy's route without its credentials", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := socksService(t, service, func(fake *upstreamtest.Proxy) string { return fake.URL("socks5", "alice", "s3cret-pw") },
			upstreamtest.WithCredentials("alice", "s3cret-pw"))

		if response, _ := service.get(t, "http://"+service.internalHost()+"/socks"); response.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d", response.StatusCode)
		}
		id, route := service.route(t, "/socks")
		wantRoute(t, route, "jump", "socks5://"+fake.Addr)
		if body := service.mustCall(t, http.MethodGet, "/traffic/"+id, ""); strings.Contains(body, "s3cret-pw") || strings.Contains(body, "alice") {
			t.Fatalf("\nwanted:\nno credentials\ngot:\n%s", body)
		}
	})

	t.Run("should record the upstream proxy that was tried when connecting through it failed", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t, upstreamtest.WithBehaviour(upstreamtest.RejectCredentials), upstreamtest.WithCredentials("right", "pass"))
		service.addUpstreamProxy(t, "corp", fake.URL("http", "user", "s3cret-pw"))
		service.use(t, "corp")

		if response, _ := service.get(t, service.origin.URL+"/blocked"); response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
		}
		_, route := service.route(t, "/blocked")
		wantRoute(t, route, "corp", "http://"+fake.Addr)
	})

	t.Run("should record the selected name when this machine does not list it", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")
		service.mustCall(t, http.MethodDelete, "/upstream-proxy/corp", "")

		service.get(t, service.origin.URL+"/missing")
		_, route := service.route(t, "/missing")
		wantRoute(t, route, "corp", "")
	})

	t.Run("should record the new route for a request captured just before use that goes out on a new connection", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		captured := make(chan struct{})
		release := make(chan struct{})
		// The request handler runs after the pair is captured and before the
		// round trip, so holding it there holds a captured request.
		service.proxy.OnRequest = func(request domain.ProxyRequest) error {
			if request.Path == "/held" {
				close(captured)
				<-release
			}
			return service.server.HandleRequest(request)
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			response, err := service.client.Get(service.origin.URL + "/held")
			if err == nil {
				response.Body.Close()
			}
		}()
		<-captured
		service.use(t, "corp")
		close(release)
		<-done

		_, route := service.route(t, "/held")
		wantRoute(t, route, "corp", "http://"+fake.Addr)
	})

	t.Run("should keep a snapshot when its upstream proxy is renamed, changed or removed", func(t *testing.T) {
		service := newUpstreamService(t)
		first := upstreamtest.NewHTTP(t)
		second := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", first.URL("http", "", ""))
		service.use(t, "corp")
		service.get(t, service.origin.URL+"/kept")
		id, _ := service.route(t, "/kept")

		service.mustCall(t, http.MethodPost, "/upstream-proxy/corp", fmt.Sprintf(`{"url":%q}`, second.URL("http", "", "")))
		path := filepath.Join(service.configDir, "marasi_config.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(data), "name: corp", "name: renamed", 1)), 0600); err != nil {
			t.Fatal(err)
		}
		if listing := service.mustCall(t, http.MethodGet, "/upstream-proxy", ""); !strings.Contains(listing, `"renamed"`) {
			t.Fatalf("the hand edit did not rename the entry: %s", listing)
		}
		service.mustCall(t, http.MethodDelete, "/upstream-proxy/renamed", "")

		pair := decodeJSON[respondedPair](t, service.mustCall(t, http.MethodGet, "/traffic/"+id, ""))
		wantRoute(t, pair.UpstreamProxy, "corp", "http://"+first.Addr)
	})

	t.Run("should keep the route out of metadata so replacing metadata leaves it", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")
		service.get(t, service.origin.URL+"/meta")
		id, _ := service.route(t, "/meta")

		service.mustCall(t, http.MethodPut, "/traffic/"+id+"/metadata", `{}`)

		body := service.mustCall(t, http.MethodGet, "/traffic/"+id, "")
		pair := decodeJSON[struct {
			respondedPair
			Metadata map[string]any `json:"metadata"`
		}](t, body)
		wantRoute(t, pair.UpstreamProxy, "corp", "http://"+fake.Addr)
		if _, ok := pair.Metadata["upstream_proxy"]; ok {
			t.Fatalf("\nwanted:\nno upstream_proxy in metadata\ngot:\n%v", pair.Metadata)
		}
	})

	t.Run("should never store credentials in a snapshot", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t, upstreamtest.WithCredentials("alice", "s3cret-pw"))
		service.addUpstreamProxy(t, "corp", fake.URL("http", "alice", "s3cret-pw"))
		service.use(t, "corp")
		service.get(t, service.origin.URL+"/secret")
		id, _ := service.route(t, "/secret")

		for _, body := range []string{
			service.mustCall(t, http.MethodGet, "/traffic/"+id, ""),
			service.mustCall(t, http.MethodGet, "/traffic?q="+url.QueryEscape(`upstream_proxy = "corp"`), ""),
		} {
			if strings.Contains(body, "s3cret-pw") || strings.Contains(body, "alice") {
				t.Fatalf("\nwanted:\nno credentials\ngot:\n%s", body)
			}
		}
	})
}

func TestPairRouteEvents(t *testing.T) {
	t.Run("should carry upstream_proxy on traffic.response and not on traffic.request", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")
		subscriber := service.server.events.subscribe()
		defer service.server.events.unsubscribe(subscriber)

		service.get(t, service.origin.URL+"/evented")

		events := map[string]map[string]json.RawMessage{}
		timeout := time.After(10 * time.Second)
		for events["traffic.request"] == nil || events["traffic.response"] == nil {
			select {
			case event := <-subscriber.events:
				if event.name == "traffic.request" || event.name == "traffic.response" {
					events[event.name] = decodeJSON[map[string]json.RawMessage](t, string(event.data))
				}
			case <-timeout:
				t.Fatalf("missing traffic events: %v", events)
			}
		}
		if _, ok := events["traffic.request"]["upstream_proxy"]; ok {
			t.Fatalf("\nwanted:\nno upstream_proxy on traffic.request\ngot:\n%v", events["traffic.request"])
		}
		route := decodeJSON[*pairRoute](t, string(events["traffic.response"]["upstream_proxy"]))
		wantRoute(t, route, "corp", "http://"+fake.Addr)
	})
}

func TestPairRouteQuery(t *testing.T) {
	t.Run("should find pairs by upstream proxy name and direct pairs by an empty name", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.get(t, service.origin.URL+"/plain?close")
		service.use(t, "corp")
		service.get(t, service.origin.URL+"/tunnelled")
		service.route(t, "/plain?close")
		service.route(t, "/tunnelled")

		for query, want := range map[string]string{
			`upstream_proxy = "corp"`: "/tunnelled",
			`upstream_proxy = ""`:     "/plain?close",
		} {
			list := decodeJSON[struct {
				Items []trafficSummary `json:"items"`
			}](t, service.mustCall(t, http.MethodGet, "/traffic?q="+url.QueryEscape(query), ""))
			if len(list.Items) != 1 || list.Items[0].Path != want {
				t.Fatalf("\nwanted:\n%s for %s\ngot:\n%+v", want, query, list.Items)
			}
		}
	})
}
