package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/internal/upstreamtest"
)

// upstreamProxyCheckResponse is POST /upstream-proxy/{name}/check.
type upstreamProxyCheckResponse struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Target  string `json:"target"`
	Outcome string `json:"outcome"`
	Message string `json:"message"`
}

// check runs a check of name against target and returns its result.
func (s *upstreamProxyService) check(t *testing.T, name, target string) upstreamProxyCheckResponse {
	t.Helper()
	return decodeJSON[upstreamProxyCheckResponse](t, s.mustCall(t, http.MethodPost, "/upstream-proxy/"+name+"/check", fmt.Sprintf(`{"target":%q}`, target)))
}

func TestUpstreamProxyCheck(t *testing.T) {
	t.Run("should report ok after opening a tunnel to the target through an entry that is not selected", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))

		got := service.check(t, "corp", service.origin.URL+"/ignored")
		if got.Outcome != "ok" || got.Name != "corp" || got.Address != "http://"+fake.Addr || got.Target != service.originHost() || got.Message == "" {
			t.Fatalf("\nwanted:\nok for corp at http://%s to %s with a message\ngot:\n%+v", fake.Addr, service.originHost(), got)
		}
		if targets := fake.Targets(); len(targets) != 1 || targets[0] != service.originHost() {
			t.Fatalf("\nwanted:\n[%s]\ngot:\n%v", service.originHost(), targets)
		}
		if name, _ := service.status(t); name != "" {
			t.Fatalf("\nwanted:\nthe selection left empty\ngot:\n%q", name)
		}
	})

	t.Run("should check through a SOCKS5 upstream proxy with the same dial as traffic", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		if err := service.proxy.WithOptions(marasi.WithUpstreamProxyTimeout(300 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		good := upstreamtest.NewSOCKS5(t)
		rejecting := upstreamtest.NewSOCKS5(t, upstreamtest.WithBehaviour(upstreamtest.RejectCredentials), upstreamtest.WithCredentials("right", "pass"))
		service.addUpstreamProxy(t, "jump", good.URL("socks5", "", ""))
		service.addUpstreamProxy(t, "locked", rejecting.URL("socks5", "user", "s3cret-pw"))

		if got := service.check(t, "jump", service.origin.URL); got.Outcome != "ok" || got.Address != "socks5://"+good.Addr {
			t.Fatalf("\nwanted:\nok through socks5://%s\ngot:\n%+v", good.Addr, got)
		}
		if targets := good.Targets(); len(targets) != 1 || targets[0] != service.originHost() {
			t.Fatalf("\nwanted:\n[%s]\ngot:\n%v", service.originHost(), targets)
		}
		if got := service.check(t, "locked", service.origin.URL); got.Outcome != "auth_rejected" || strings.Contains(got.Message, "s3cret-pw") {
			t.Fatalf("\nwanted:\nauth_rejected without credentials\ngot:\n%+v", got)
		}
	})

	failures := []struct {
		name      string
		behaviour upstreamtest.Behaviour
		outcome   string
	}{
		{name: "refuses the connection", behaviour: upstreamtest.Refuse, outcome: "refused"},
		{name: "rejects the credentials", behaviour: upstreamtest.RejectCredentials, outcome: "auth_rejected"},
		{name: "never answers", behaviour: upstreamtest.Hang, outcome: "timeout"},
	}
	for _, tc := range failures {
		t.Run("should report "+tc.outcome+" when the upstream proxy "+tc.name+", without a secret, event, pair or log", func(t *testing.T) {
			service := newUpstreamProxyService(t)
			if err := service.proxy.WithOptions(marasi.WithUpstreamProxyTimeout(300 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			fake := upstreamtest.NewHTTP(t, upstreamtest.WithBehaviour(tc.behaviour), upstreamtest.WithCredentials("right", "pass"))
			service.addUpstreamProxy(t, "corp", fake.URL("http", "user", "s3cret-pw"))
			subscriber := service.server.events.subscribe()
			defer service.server.events.unsubscribe(subscriber)

			code, body := service.call(t, http.MethodPost, "/upstream-proxy/corp/check", fmt.Sprintf(`{"target":%q}`, service.origin.URL))
			if code != http.StatusOK || strings.Contains(body, "s3cret-pw") {
				t.Fatalf("\nwanted:\n200 without credentials\ngot:\n%d %s", code, body)
			}
			got := decodeJSON[upstreamProxyCheckResponse](t, body)
			if got.Outcome != tc.outcome || !strings.Contains(got.Message, `upstream proxy "corp"`) {
				t.Fatalf("\nwanted:\n%s with a message naming corp\ngot:\n%+v", tc.outcome, got)
			}
			if hits := service.originHits.Load(); hits != 0 {
				t.Fatalf("\nwanted:\nthe origin never reached\ngot:\n%d hits", hits)
			}
			assertNoChromeEvent(t, subscriber)
			service.upstreamProxyLogs(t, 0)
			pairs := decodeJSON[struct {
				Items []trafficSummary `json:"items"`
			}](t, service.mustCall(t, http.MethodGet, "/traffic", ""))
			if len(pairs.Items) != 0 {
				t.Fatalf("\nwanted:\nno pair\ngot:\n%+v", pairs.Items)
			}
		})
	}

	t.Run("should report tls_verification when the target's certificate does not verify through the tunnel", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(origin.Close)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))

		got := service.check(t, "corp", origin.URL)
		host := strings.TrimPrefix(origin.URL, "https://")
		if got.Outcome != "tls_verification" || got.Target != host {
			t.Fatalf("\nwanted:\ntls_verification for %s\ngot:\n%+v", host, got)
		}
		if targets := fake.Targets(); len(targets) != 1 || targets[0] != host {
			t.Fatalf("\nwanted:\n[%s]\ngot:\n%v", host, targets)
		}
	})

	t.Run("should report not found for a name this machine does not list", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		code, body := service.call(t, http.MethodPost, "/upstream-proxy/missing/check", `{"target":"http://example.test"}`)
		if code != http.StatusNotFound || !strings.Contains(body, `"not_found"`) {
			t.Fatalf("\nwanted:\n404 not_found\ngot:\n%d %s", code, body)
		}
	})

	t.Run("should refuse a target that is not an http or https URL with a host", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		for _, body := range []string{`{"target":"ftp://example.test"}`, `{"target":"http://"}`, `{"target":"example.test:80"}`, `{"target":"%zz"}`} {
			code, response := service.call(t, http.MethodPost, "/upstream-proxy/corp/check", body)
			if code != http.StatusBadRequest || !strings.Contains(response, `"invalid_target"`) {
				t.Fatalf("\nwanted:\n400 invalid_target for %s\ngot:\n%d %s", body, code, response)
			}
		}
		for _, body := range []string{`{}`, `{"target":1}`, `{"target":"http://a.test","extra":true}`} {
			if code, response := service.call(t, http.MethodPost, "/upstream-proxy/corp/check", body); code != http.StatusBadRequest {
				t.Fatalf("\nwanted:\n400 for %s\ngot:\n%d %s", body, code, response)
			}
		}
		if targets := fake.Targets(); len(targets) != 0 {
			t.Fatalf("\nwanted:\nnothing dialled\ngot:\n%v", targets)
		}
	})
}
