package service

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/upstreamtest"
)

// upstreamService is a capturing service wired in production order: a
// project lifecycle bound to a real listener, a server that shares the
// listener's events, and a temp config directory for the machine list.
type upstreamService struct {
	server    *Server
	lifecycle *ProjectLifecycle
	proxy     *marasi.Proxy
	configDir string
	project   string
	client    *http.Client
	address   string
	origin    *httptest.Server
	// originHits counts requests that reached the origin.
	originHits atomic.Int64
}

func newUpstreamService(t *testing.T, options ...func(*marasi.Proxy) error) *upstreamService {
	t.Helper()
	lifecycle, proxy, configDir := newTestProjectLifecycle(t)
	listener := NewListenerLifecycle(proxy, io.Discard)
	listener.BindProject(lifecycle)
	project := canonicalProjectPath(t, filepath.Join(configDir, "project.marasi"))
	if err := lifecycle.Open(context.Background(), project); err != nil {
		t.Fatalf("opening project: %v", err)
	}
	// Options such as WithTLS need the open project.
	if err := proxy.WithOptions(options...); err != nil {
		t.Fatalf("applying proxy options: %v", err)
	}
	if err := proxy.WithOptions(marasi.WithBasePipeline(), marasi.WithDefaultModifierPipeline()); err != nil {
		t.Fatalf("adding pipelines: %v", err)
	}
	server := NewServer(proxy, listener, lifecycle, func() {}, "dev", "default", project)
	if err := proxy.WithOptions(
		marasi.WithRequestHandler(server.HandleRequest),
		marasi.WithResponseHandler(server.HandleResponse),
		marasi.WithLaunchStartedHandler(server.HandleLaunchStarted),
		marasi.WithLaunchFinishedHandler(server.HandleLaunchFinished),
	); err != nil {
		t.Fatalf("adding handlers: %v", err)
	}
	service := &upstreamService{server: server, lifecycle: lifecycle, proxy: proxy, configDir: configDir, project: project}
	service.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.originHits.Add(1)
		if r.URL.Query().Has("close") {
			w.Header().Set("Connection", "close")
		}
		if r.URL.Path == "/ws" {
			conn, buffered, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
			_ = buffered.Flush()
			return
		}
		_, _ = io.WriteString(w, "origin "+r.URL.Path)
	}))
	t.Cleanup(service.origin.Close)
	status, err := listener.Start(context.Background(), listenerSettings("127.0.0.1", 0))
	if err != nil {
		t.Fatalf("starting listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Shutdown() })
	service.address = statusAddress(t, status)
	// Without compression the client sends no Accept-Encoding, so the bytes
	// an origin receives show anything the proxy added.
	transport := &http.Transport{
		Proxy:              http.ProxyURL(&url.URL{Scheme: "http", Host: service.address}),
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(transport.CloseIdleConnections)
	service.client = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	return service
}

// call sends a control API request and returns the status and body.
func (s *upstreamService) call(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	response := requestControlAPI(s.server, method, path, body)
	return response.Code, response.Body.String()
}

// mustCall sends a control API request that has to succeed.
func (s *upstreamService) mustCall(t *testing.T, method, path, body string) string {
	t.Helper()
	code, response := s.call(t, method, path, body)
	if code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, code, response)
	}
	return response
}

// addUpstreamProxy adds an entry to the machine list.
func (s *upstreamService) addUpstreamProxy(t *testing.T, name, rawURL string) {
	t.Helper()
	s.mustCall(t, http.MethodPost, "/upstream-proxy", fmt.Sprintf(`{"name":%q,"url":%q}`, name, rawURL))
}

// use selects name for the open project, or none when name is "".
func (s *upstreamService) use(t *testing.T, name string) {
	t.Helper()
	s.mustCall(t, http.MethodPut, "/upstream-proxy-selection", fmt.Sprintf(`{"name":%q}`, name))
}

// get sends a GET through the listener and returns the status and body.
func (s *upstreamService) get(t *testing.T, target string) (*http.Response, string) {
	t.Helper()
	response, err := s.client.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", target, err)
	}
	return response, string(body)
}

func (s *upstreamService) originHost() string {
	return strings.TrimPrefix(s.origin.URL, "http://")
}

func TestUpstreamProxyRouting(t *testing.T) {
	t.Run("should send browser traffic through the selected HTTP upstream proxy", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")

		response, body := service.get(t, service.origin.URL+"/through")
		if response.StatusCode != http.StatusOK || body != "origin /through" {
			t.Fatalf("\nwanted:\n200 origin /through\ngot:\n%d %s", response.StatusCode, body)
		}
		if got := fake.Targets(); len(got) != 1 || got[0] != service.originHost() {
			t.Fatalf("\nwanted:\n[%s]\ngot:\n%v", service.originHost(), got)
		}
	})

	t.Run("should tunnel plain HTTP with CONNECT and Basic credentials, adding nothing to the request", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t, upstreamtest.WithBehaviour(upstreamtest.RejectCredentials), upstreamtest.WithCredentials("alice", "s3cret:pw"))
		service.addUpstreamProxy(t, "corp", fake.URL("http", "alice", "s3cret%3Apw"))
		service.use(t, "corp")

		if response, body := service.get(t, service.origin.URL+"/bytes"); response.StatusCode != http.StatusOK || body != "origin /bytes" {
			t.Fatalf("\nwanted:\n200 origin /bytes\ngot:\n%d %s", response.StatusCode, body)
		}
		tunnels := fake.Tunnels()
		if len(tunnels) != 1 {
			t.Fatalf("\nwanted:\none tunnel\ngot:\n%d", len(tunnels))
		}
		tunnel := tunnels[0]
		if tunnel.Request.Method != http.MethodConnect || tunnel.Username != "alice" || tunnel.Password != "s3cret:pw" {
			t.Fatalf("\nwanted:\nCONNECT with alice's credentials\ngot:\n%s %q %q", tunnel.Request.Method, tunnel.Username, tunnel.Password)
		}
		// Origin form, the client's own headers and nothing else: no
		// Proxy-Authorization, Proxy-Connection, Connection or Accept-Encoding.
		want := "GET /bytes HTTP/1.1\r\nHost: " + service.originHost() + "\r\nUser-Agent: Go-http-client/1.1\r\n\r\n"
		if got := string(tunnel.Sent()); got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should run the TLS handshake to an HTTPS origin inside the tunnel", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(origin.Close)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")

		// The test origin's certificate does not verify, so the request
		// fails after the ClientHello went through the tunnel.
		service.get(t, origin.URL+"/secure")
		host := strings.TrimPrefix(origin.URL, "https://")
		tunnels := fake.Tunnels()
		if len(tunnels) != 1 || tunnels[0].Target != host {
			t.Fatalf("\nwanted:\none tunnel to %s\ngot:\n%v", host, fake.Targets())
		}
		if sent := tunnels[0].Sent(); len(sent) == 0 || sent[0] != 0x16 {
			t.Fatalf("\nwanted:\na TLS handshake record\ngot:\n%q", sent)
		}
	})

	t.Run("should ask the upstream proxy for a waypoint's destination", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")
		service.mustCall(t, http.MethodPost, "/waypoint", fmt.Sprintf(`{"hostname":"target.test:80","override":%q}`, service.originHost()))

		if response, body := service.get(t, "http://target.test/waypoint"); response.StatusCode != http.StatusOK || body != "origin /waypoint" {
			t.Fatalf("\nwanted:\n200 origin /waypoint\ngot:\n%d %s", response.StatusCode, body)
		}
		if got := fake.Targets(); len(got) != 1 || got[0] != service.originHost() {
			t.Fatalf("\nwanted:\n[%s]\ngot:\n%v", service.originHost(), got)
		}
	})

	t.Run("should carry a launch, an Armory run, Lua HTTP and a WebSocket connection through it", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")

		// Launch.
		launchpad := decodeJSON[struct {
			ID uuid.UUID `json:"id"`
		}](t, service.mustCall(t, http.MethodPost, "/launchpad", `{"name":"routes"}`))
		startLaunch(t, service.server, launchpad.ID, launchBody(service.origin, "/launch"))

		// Armory.
		if err := os.WriteFile(filepath.Join(service.configDir, "wordlists", "w.txt"), []byte("a\n"), 0600); err != nil {
			t.Fatal(err)
		}
		template := decodeJSON[struct {
			ID uuid.UUID `json:"id"`
		}](t, service.mustCall(t, http.MethodPost, "/armory/template", fmt.Sprintf(`{"name":"t","raw_template":%q}`, "GET /armory/@@x@@ HTTP/1.1\r\nHost: "+service.originHost()+"\r\n\r\n")))
		run := decodeJSON[struct {
			ID uuid.UUID `json:"id"`
		}](t, service.mustCall(t, http.MethodPost, "/armory/run", fmt.Sprintf(`{"template_id":%q,"attack_type":"harpoon","wordlists":["w.txt"],"use_https":false,"max_concurrent":1}`, template.ID)))
		service.mustCall(t, http.MethodPost, "/armory/run/"+run.ID.String()+"/start", "")

		// Lua HTTP.
		lua := fmt.Sprintf(`function fetch()
  local res, err = marasi:builder():set_method("GET"):set_url(%q):send()
  if not res then error(err) end
end`, service.origin.URL+"/lua")
		workshop := workshopExtensionID(t, service.proxy).String()
		service.mustCall(t, http.MethodPost, "/extension/"+workshop, fmt.Sprintf(`{"lua_content":%q}`, lua))
		service.mustCall(t, http.MethodPost, "/extension/"+workshop+"/call", `{"function":"fetch"}`)

		// WebSocket.
		if status := service.upgrade(t, "/ws"); status != http.StatusSwitchingProtocols {
			t.Fatalf("\nwanted:\n101\ngot:\n%d", status)
		}

		for _, path := range []string{"/launch", "/armory/a", "/lua", "/ws"} {
			deadline := time.Now().Add(10 * time.Second)
			for !carried(fake, "GET "+path+" HTTP/1.1\r\n") {
				if time.Now().After(deadline) {
					t.Fatalf("\nwanted:\n%s through the upstream proxy\ngot:\ntargets %v", path, fake.Targets())
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		for _, target := range fake.Targets() {
			if target != service.originHost() {
				t.Fatalf("\nwanted:\nevery tunnel to %s\ngot:\n%s", service.originHost(), target)
			}
		}
		// Every origin hit came through a tunnel.
		if hits, carried := service.originHits.Load(), int64(countCarried(fake, " HTTP/1.1\r\n")); hits != carried {
			t.Fatalf("\nwanted:\n%d origin hits, all through the upstream proxy\ngot:\n%d", carried, hits)
		}
	})
}

// carried reports whether any tunnel through fake carried text.
func carried(fake *upstreamtest.Proxy, text string) bool {
	return countCarried(fake, text) > 0
}

// countCarried counts text across the bytes sent through fake's tunnels.
func countCarried(fake *upstreamtest.Proxy, text string) int {
	count := 0
	for _, tunnel := range fake.Tunnels() {
		count += strings.Count(string(tunnel.Sent()), text)
	}
	return count
}

// upgrade sends a WebSocket upgrade for path on the origin through the
// listener and returns the response status.
func (s *upstreamService) upgrade(t *testing.T, path string) int {
	t.Helper()
	conn, err := net.Dial("tcp", s.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", s.origin.URL+path, s.originHost())
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading the upgrade response: %v", err)
	}
	return response.StatusCode
}

// serviceStatus is the upstream proxy part of GET /service/status.
type serviceStatus struct {
	UpstreamProxy           *string `json:"upstream_proxy"`
	UpstreamProxyConfigured *bool   `json:"upstream_proxy_configured"`
}

func (s *upstreamService) status(t *testing.T) (string, bool) {
	t.Helper()
	status := decodeJSON[serviceStatus](t, s.mustCall(t, http.MethodGet, "/service/status", ""))
	if status.UpstreamProxy == nil || status.UpstreamProxyConfigured == nil {
		t.Fatalf("the status lacks upstream_proxy or upstream_proxy_configured: %+v", status)
	}
	return *status.UpstreamProxy, *status.UpstreamProxyConfigured
}

// upstreamLogs waits until want proxy logs about upstream proxies are stored
// and returns them, failing if more arrive.
func (s *upstreamService) upstreamLogs(t *testing.T, want int) []proxyLogResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var page proxyLogList
		page = decodeJSON[proxyLogList](t, s.mustCall(t, http.MethodGet, "/logs", ""))
		var found []proxyLogResponse
		for _, item := range page.Items {
			if strings.HasPrefix(item.Message, "upstream proxy ") {
				found = append(found, item)
			}
		}
		if len(found) > want {
			t.Fatalf("\nwanted:\n%d upstream proxy logs\ngot:\n%+v", want, found)
		}
		if len(found) == want {
			// Give a duplicate the chance to land.
			time.Sleep(100 * time.Millisecond)
			page = decodeJSON[proxyLogList](t, s.mustCall(t, http.MethodGet, "/logs", ""))
			count := 0
			for _, item := range page.Items {
				if strings.HasPrefix(item.Message, "upstream proxy ") {
					count++
				}
			}
			if count != want {
				t.Fatalf("\nwanted:\n%d upstream proxy logs\ngot:\n%d", want, count)
			}
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("\nwanted:\n%d upstream proxy logs\ngot:\n%+v", want, found)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pairID returns the id of the only stored pair whose path is path.
func (s *upstreamService) pairID(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		list := decodeJSON[struct {
			Items []trafficSummary `json:"items"`
		}](t, s.mustCall(t, http.MethodGet, "/traffic", ""))
		var ids []string
		for _, item := range list.Items {
			if item.Path == path {
				ids = append(ids, item.ID.String())
			}
		}
		if len(ids) == 1 {
			return ids[0]
		}
		if len(ids) > 1 || time.Now().After(deadline) {
			t.Fatalf("\nwanted:\none pair for %s\ngot:\n%v", path, ids)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpstreamProxySelection(t *testing.T) {
	t.Run("should start a project with no selection and connect directly", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))

		response, body := service.get(t, service.origin.URL+"/direct")
		if response.StatusCode != http.StatusOK || body != "origin /direct" {
			t.Fatalf("\nwanted:\n200 origin /direct\ngot:\n%d %s", response.StatusCode, body)
		}
		if got := fake.Targets(); len(got) != 0 {
			t.Fatalf("\nwanted:\nno tunnels\ngot:\n%v", got)
		}
		if name, configured := service.status(t); name != "" || !configured {
			t.Fatalf("\nwanted:\n\"\" true\ngot:\n%q %v", name, configured)
		}
		listing := service.mustCall(t, http.MethodGet, "/upstream-proxy", "")
		if !strings.Contains(listing, `"selected":"","configured":true`) {
			t.Fatalf("\nwanted:\nno selection in %s", listing)
		}
	})

	t.Run("should store the selection under the reserved client, publish it and dial nothing", func(t *testing.T) {
		service := newUpstreamService(t)
		// Nothing listens here, so any dial would be refused and logged.
		fake := upstreamtest.NewHTTP(t, upstreamtest.WithBehaviour(upstreamtest.Refuse))
		service.addUpstreamProxy(t, "corp", fake.URL("http", "user", "secret"))
		subscriber := service.server.events.subscribe()
		defer service.server.events.unsubscribe(subscriber)

		body := service.mustCall(t, http.MethodPut, "/upstream-proxy-selection", `{"name":"corp"}`)
		if body != `{"selected":"corp","configured":true}`+"\n" {
			t.Fatalf("\nwanted:\nthe selection\ngot:\n%s", body)
		}
		event := <-subscriber.events
		if event.name != "upstream_proxy.selected" || string(event.data) != `{"name":"corp"}` {
			t.Fatalf("\nwanted:\nupstream_proxy.selected {\"name\":\"corp\"}\ngot:\n%s %s", event.name, event.data)
		}
		setting := service.mustCall(t, http.MethodGet, "/client-settings/marasi/upstream_proxy.active", "")
		if !strings.Contains(setting, `"value":"corp"`) {
			t.Fatalf("\nwanted:\nthe stored selection\ngot:\n%s", setting)
		}
		if name, configured := service.status(t); name != "corp" || !configured {
			t.Fatalf("\nwanted:\ncorp true\ngot:\n%q %v", name, configured)
		}
		code, refused := service.call(t, http.MethodPut, "/client-settings/marasi/upstream_proxy.active", `{"value":"other"}`)
		if code != http.StatusBadRequest || refused != `{"error":"reserved_client"}`+"\n" {
			t.Fatalf("\nwanted:\n400 reserved_client\ngot:\n%d %s", code, refused)
		}

		body = service.mustCall(t, http.MethodPut, "/upstream-proxy-selection", `{"name":""}`)
		if body != `{"selected":"","configured":true}`+"\n" {
			t.Fatalf("\nwanted:\nno selection\ngot:\n%s", body)
		}
		event = <-subscriber.events
		if event.name != "upstream_proxy.selected" || string(event.data) != `{"name":""}` {
			t.Fatalf("\nwanted:\nupstream_proxy.selected {\"name\":\"\"}\ngot:\n%s %s", event.name, event.data)
		}
		if code, _ := service.call(t, http.MethodGet, "/client-settings/marasi/upstream_proxy.active", ""); code != http.StatusNotFound {
			t.Fatalf("\nwanted:\nthe selection deleted\ngot:\n%d", code)
		}
		service.upstreamLogs(t, 0)
	})

	t.Run("should refuse to select a name this machine does not list", func(t *testing.T) {
		service := newUpstreamService(t)
		code, body := service.call(t, http.MethodPut, "/upstream-proxy-selection", `{"name":"missing"}`)
		if code != http.StatusNotFound || body != `{"error":"not_found"}`+"\n" {
			t.Fatalf("\nwanted:\n404 not_found\ngot:\n%d %s", code, body)
		}
		for _, bad := range []string{``, `{}`, `{"name":null}`, `{"name":"corp","extra":1}`} {
			if code, _ := service.call(t, http.MethodPut, "/upstream-proxy-selection", bad); code != http.StatusBadRequest {
				t.Fatalf("\nwanted:\n400 for %s\ngot:\n%d", bad, code)
			}
		}
	})
}

func TestUpstreamProxyFailsClosed(t *testing.T) {
	cases := []struct {
		name      string
		behaviour upstreamtest.Behaviour
		cause     string
	}{
		{name: "refused", behaviour: upstreamtest.Refuse, cause: "refused"},
		{name: "credentials rejected", behaviour: upstreamtest.RejectCredentials, cause: "auth_rejected"},
		{name: "hanging", behaviour: upstreamtest.Hang, cause: "timeout"},
	}
	for _, tc := range cases {
		t.Run("should fail the request when the upstream proxy is "+tc.name, func(t *testing.T) {
			service := newUpstreamService(t)
			if err := service.proxy.WithOptions(marasi.WithUpstreamProxyTimeout(300 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			fake := upstreamtest.NewHTTP(t, upstreamtest.WithBehaviour(tc.behaviour), upstreamtest.WithCredentials("right", "pass"))
			service.addUpstreamProxy(t, "corp", fake.URL("http", "user", "s3cret-pw"))
			service.use(t, "corp")

			response, _ := service.get(t, service.origin.URL+"/blocked")
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
			}
			if warning := response.Header.Get("Warning"); !strings.Contains(warning, `upstream proxy \"corp\"`) || strings.Contains(warning, "s3cret-pw") {
				t.Fatalf("\nwanted:\na Warning naming corp without credentials\ngot:\n%s", warning)
			}
			if hits := service.originHits.Load(); hits != 0 {
				t.Fatalf("\nwanted:\nthe origin never reached\ngot:\n%d hits", hits)
			}
			id := service.pairID(t, "/blocked")
			logs := service.upstreamLogs(t, 1)
			entry := logs[0]
			if entry.Level != "ERROR" || entry.RequestID == nil || entry.RequestID.String() != id {
				t.Fatalf("\nwanted:\nan ERROR log for pair %s\ngot:\n%+v", id, entry)
			}
			if entry.Context["upstream_proxy"] != "corp" || entry.Context["cause"] != tc.cause || entry.Context["address"] != "http://"+fake.Addr {
				t.Fatalf("\nwanted:\ncontext corp, %s, http://%s\ngot:\n%v", tc.cause, fake.Addr, entry.Context)
			}
			if strings.Contains(entry.Message, "s3cret-pw") || !strings.Contains(entry.Message, `"corp"`) {
				t.Fatalf("\nwanted:\na message naming corp without credentials\ngot:\n%s", entry.Message)
			}
		})
	}

	t.Run("should refuse every request while the selection is missing from this machine's list", func(t *testing.T) {
		service := newUpstreamService(t)
		fake := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", fake.URL("http", "", ""))
		service.use(t, "corp")
		service.mustCall(t, http.MethodDelete, "/upstream-proxy/corp", "")

		for _, path := range []string{"/first", "/second"} {
			if response, _ := service.get(t, service.origin.URL+path); response.StatusCode != http.StatusBadGateway {
				t.Fatalf("\nwanted:\n502 for %s\ngot:\n%d", path, response.StatusCode)
			}
		}
		if hits := service.originHits.Load(); hits != 0 || len(fake.Targets()) != 0 {
			t.Fatalf("\nwanted:\nno origin hit and no tunnel\ngot:\n%d hits, %v", hits, fake.Targets())
		}
		logs := service.upstreamLogs(t, 2)
		for _, entry := range logs {
			if entry.Context["cause"] != "not_configured" || entry.RequestID == nil {
				t.Fatalf("\nwanted:\nnot_configured logs naming pairs\ngot:\n%+v", entry)
			}
		}
		if name, configured := service.status(t); name != "corp" || configured {
			t.Fatalf("\nwanted:\ncorp false\ngot:\n%q %v", name, configured)
		}
	})

	t.Run("should open a project whose selection this machine never listed and keep its traffic readable", func(t *testing.T) {
		service := newUpstreamService(t)
		service.get(t, service.origin.URL+"/before")
		service.pairID(t, "/before")
		elsewhere := canonicalProjectPath(t, filepath.Join(t.TempDir(), "elsewhere.marasi"))
		seedSelection(t, elsewhere, "client-jump")
		service.mustCall(t, http.MethodPost, "/project/open", projectOpenBody(elsewhere))
		seed := service.pairID(t, "/seeded")

		if name, configured := service.status(t); name != "client-jump" || configured {
			t.Fatalf("\nwanted:\nclient-jump false\ngot:\n%q %v", name, configured)
		}
		if code, _ := service.call(t, http.MethodGet, "/traffic/"+seed, ""); code != http.StatusOK {
			t.Fatalf("\nwanted:\nthe stored pair readable\ngot:\n%d", code)
		}
		if response, _ := service.get(t, service.origin.URL+"/refused"); response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
		}
		if hits := service.originHits.Load(); hits != 1 {
			t.Fatalf("\nwanted:\nonly /before reached the origin\ngot:\n%d hits", hits)
		}
		service.upstreamLogs(t, 1)
	})

	t.Run("should apply a hand edit of the list on the next connection", func(t *testing.T) {
		service := newUpstreamService(t)
		first := upstreamtest.NewHTTP(t)
		second := upstreamtest.NewHTTP(t)
		service.addUpstreamProxy(t, "corp", first.URL("http", "", ""))
		service.use(t, "corp")
		service.get(t, service.origin.URL+"/one?close")

		path := filepath.Join(service.configDir, "marasi_config.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		edited := strings.Replace(string(data), first.Addr, second.Addr, 1)
		if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Fatal(err)
		}
		// /one?close closed its origin connection, so /two opens a new one.
		service.get(t, service.origin.URL+"/two")
		if got := second.Targets(); len(got) != 1 || len(first.Targets()) != 1 {
			t.Fatalf("\nwanted:\none tunnel through each\ngot:\nfirst %v, second %v", first.Targets(), got)
		}
	})
}

// seedSelection makes a project at path with one pair at /seeded and the
// upstream proxy selection name, as if another machine had set it.
func seedSelection(t *testing.T, path, name string) {
	t.Helper()
	database, err := db.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	repository := db.NewProxyRepo(database)
	if err := marasi.StoreUpstreamProxySelection(repository, name); err != nil {
		t.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7())
	request := domain.ProxyRequest{ID: id, Scheme: "http", Method: "GET", Host: "seed.test", Path: "/seeded", Raw: []byte("GET /seeded HTTP/1.1\r\nHost: seed.test\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now()}
	if err := repository.InsertRequest(&request); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

// decodeJSON decodes body into a value of type T.
func decodeJSON[T any](t *testing.T, body string) T {
	t.Helper()
	var value T
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return value
}
