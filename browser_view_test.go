package marasi

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// viewHarness serves a real proxy, with the base and default pipelines and a
// CA certificate, over an in-memory project.
type viewHarness struct {
	proxy      *Proxy
	repository *db.Repository
	captured   chan domain.ProxyRequest
}

type viewHarnessConfig struct {
	listen       string // the listener address, "127.0.0.1" by default
	secureOrigin bool   // trust any upstream certificate, for an httptest TLS origin
	options      []func(*Proxy) error
}

func startViewHarness(t *testing.T, config viewHarnessConfig) *viewHarness {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repository := newViewRepository(t, logger)
	defaultExtensions, err := repository.GetExtensions()
	if err != nil {
		t.Fatalf("getting default extensions: %v", err)
	}
	captured := make(chan domain.ProxyRequest, 32)
	options := []func(*Proxy) error{
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithTLS(),
		WithExtensions(defaultExtensions),
		WithRequestHandler(func(req domain.ProxyRequest) error { captured <- req; return nil }),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithLaunchFinishedHandler(func(domain.Launch) {}),
	}
	options = append(options, config.options...)
	options = append(options, WithBasePipeline(), WithDefaultModifierPipeline())
	proxy, err := New(options...)
	if err != nil {
		t.Fatalf("creating proxy: %v", err)
	}
	listen := config.listen
	if listen == "" {
		listen = "127.0.0.1"
	}
	if listen == "all" {
		listen = ""
	}
	proxyListener, err := proxy.GetListener(listen, "0")
	if err != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	if config.secureOrigin {
		// As Serve does, with an upstream transport that trusts the origin's
		// self-signed certificate.
		roundTripper := newMarasiTransport(proxy.dialTransport)
		roundTripper.(*marasiRoundTripper).base.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		proxy.martianProxy.SetRoundTripper(roundTripper)
		proxy.dbWriterStarted.Store(true)
		go proxy.WriteToDB()
		go func() { serveResult <- proxy.martianProxy.Serve(proxyListener) }()
	} else {
		go func() { serveResult <- proxy.Serve(proxyListener) }()
	}
	t.Cleanup(func() {
		proxyListener.Close()
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
		}
	})
	return &viewHarness{proxy: proxy, repository: repository, captured: captured}
}

func newViewRepository(t *testing.T, logger *slog.Logger) *db.Repository {
	t.Helper()
	dbConnection, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	return db.NewProxyRepo(dbConnection)
}

// client returns a browser-like client that reaches the proxy listener at
// host and does not follow redirects.
func (h *viewHarness) client(t *testing.T, host string) *http.Client {
	t.Helper()
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, h.proxy.Port)}
	roots := x509.NewCertPool()
	roots.AddCert(h.proxy.Cert)
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:              http.ProxyURL(proxyURL),
			TLSClientConfig:    &tls.Config{RootCAs: roots},
			DisableCompression: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// capture sends req through client and returns the pair it was stored as.
func (h *viewHarness) capture(t *testing.T, client *http.Client, req *http.Request) domain.ProxyRequest {
	t.Helper()
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("capturing %s %s: %v", req.Method, req.URL, err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	select {
	case captured := <-h.captured:
		h.flush(t)
		return captured
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s %s to be captured", req.Method, req.URL)
		return domain.ProxyRequest{}
	}
}

func (h *viewHarness) flush(t *testing.T) {
	t.Helper()
	if err := h.proxy.flushDBWrites(); err != nil {
		t.Fatalf("flushing database writes: %v", err)
	}
}

// pairCount returns how many pairs the open project holds.
func (h *viewHarness) pairCount(t *testing.T) int {
	t.Helper()
	h.flush(t)
	summary, err := h.repository.GetRequestResponseSummary()
	if err != nil {
		t.Fatalf("listing traffic: %v", err)
	}
	return len(summary)
}

// assertNothingCaptured checks that no request event fired and the project
// still holds want pairs.
func (h *viewHarness) assertNothingCaptured(t *testing.T, want int) {
	t.Helper()
	if got := h.pairCount(t); got != want {
		t.Errorf("pairs in the project\nwanted:\n%d\ngot:\n%d", want, got)
	}
	select {
	case req := <-h.captured:
		t.Errorf("request event\nwanted:\nnone\ngot:\n%s %s%s", req.Method, req.Host, req.Path)
	default:
	}
}

// get sends a GET through client and returns the response with its body read.
func get(t *testing.T, client *http.Client, target string) (*http.Response, []byte) {
	t.Helper()
	res, err := client.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the body of %s: %v", target, err)
	}
	return res, body
}

// viewOrigin counts the requests it receives per path. Every answer names
// its visit number, so a stored answer can be told from a live one.
type viewOrigin struct {
	*httptest.Server
	mu    sync.Mutex
	hits  map[string]int
	total int
}

func startViewOrigin(t *testing.T, secure bool) *viewOrigin {
	t.Helper()
	origin := &viewOrigin{hits: make(map[string]int)}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin.mu.Lock()
		origin.hits[r.URL.Path]++
		origin.total++
		visit := origin.total
		origin.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/moved":
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
			fmt.Fprintf(w, "moved, visit %d", visit)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Set-Cookie", fmt.Sprintf("session=visit-%d; Path=/; HttpOnly", visit))
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			fmt.Fprintf(w, "<html>%s %s, visit %d %s</html>", r.Method, r.URL.RequestURI(), visit, body)
		}
	})
	if secure {
		origin.Server = httptest.NewTLSServer(handler)
	} else {
		origin.Server = httptest.NewServer(handler)
	}
	t.Cleanup(origin.Close)
	return origin
}

func (origin *viewOrigin) hitsTo(path string) int {
	origin.mu.Lock()
	defer origin.mu.Unlock()
	return origin.hits[path]
}

// openView opens the browser view of id through client and returns the URL
// it redirects to.
func openView(t *testing.T, client *http.Client, id uuid.UUID) string {
	t.Helper()
	res, body := get(t, client, "http://marasi/view/"+id.String())
	if res.StatusCode != http.StatusFound {
		t.Fatalf("browser view status\nwanted:\n%d\ngot:\n%d %s", http.StatusFound, res.StatusCode, body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("browser view Cache-Control\nwanted:\nno-store\ngot:\n%q", got)
	}
	return res.Header.Get("Location")
}

// storedResponse returns the raw response stored for id.
func (h *viewHarness) storedResponse(t *testing.T, id uuid.UUID) []byte {
	t.Helper()
	stored, err := h.repository.GetResponse(id)
	if err != nil {
		t.Fatalf("getting the stored response of %s: %v", id, err)
	}
	return stored.Raw
}

// assertServedStored checks that res is the stored raw response, byte for byte.
func assertServedStored(t *testing.T, res *http.Response, body []byte, stored []byte) {
	t.Helper()
	res.Body = io.NopCloser(bytes.NewReader(body))
	got, err := httputil.DumpResponse(res, true)
	if err != nil {
		t.Fatalf("dumping the served response: %v", err)
	}
	if !bytes.Equal(got, stored) {
		t.Errorf("served response\nwanted:\n%s\ngot:\n%s", stored, got)
	}
}

func TestProxy_MarasiPages(t *testing.T) {
	for _, target := range []string{"http://marasi/cert", "https://marasi/cert"} {
		t.Run("should serve the CA certificate at "+target+" without capturing it", func(t *testing.T) {
			h := startViewHarness(t, viewHarnessConfig{})

			res, body := get(t, h.client(t, "127.0.0.1"), target)

			if res.StatusCode != http.StatusOK {
				t.Fatalf("status\nwanted:\n%d\ngot:\n%d %s", http.StatusOK, res.StatusCode, body)
			}
			if got := res.Header.Get("Content-Type"); got != "application/x-x509-ca-cert" {
				t.Errorf("Content-Type\nwanted:\napplication/x-x509-ca-cert\ngot:\n%q", got)
			}
			if got, want := res.Header.Get("Content-Disposition"), `attachment; filename="marasi-cert.der"`; got != want {
				t.Errorf("Content-Disposition\nwanted:\n%s\ngot:\n%q", want, got)
			}
			if !bytes.Equal(body, h.proxy.Cert.Raw) {
				t.Errorf("body\nwanted:\nthe DER CA certificate\ngot:\n%q", body)
			}
			h.assertNothingCaptured(t, 0)
		})
	}

	t.Run("should answer an unknown path on marasi with a plain-text 404", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})

		res, body := get(t, h.client(t, "127.0.0.1"), "http://marasi/nothing-here")

		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status\nwanted:\n%d\ngot:\n%d", http.StatusNotFound, res.StatusCode)
		}
		if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Errorf("Content-Type\nwanted:\ntext/plain\ngot:\n%q (%s)", got, body)
		}
		h.assertNothingCaptured(t, 0)
	})

	t.Run("should serve the certificate when Compass would exclude it", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		h.proxy.Scope.SetDefaultAllow(false)

		res, _ := get(t, h.client(t, "127.0.0.1"), "http://marasi/cert")

		if res.StatusCode != http.StatusOK {
			t.Errorf("status\nwanted:\n%d\ngot:\n%d", http.StatusOK, res.StatusCode)
		}
	})

	t.Run("should not hold a certificate request with Checkpoint armed", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		h.proxy.SetIntercept(true)

		res, _ := get(t, h.client(t, "127.0.0.1"), "http://marasi/cert")

		if res.StatusCode != http.StatusOK {
			t.Errorf("status\nwanted:\n%d\ngot:\n%d", http.StatusOK, res.StatusCode)
		}
		if h.proxy.HasPendingCheckpoint() {
			t.Error("Checkpoint\nwanted:\nnothing held\ngot:\nan item held")
		}
	})

	t.Run("should not show a certificate request to extensions", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		h.workshop(t, `function processRequest(request) request:drop() end
function processResponse(response) response:drop() end`)

		res, _ := get(t, h.client(t, "127.0.0.1"), "http://marasi/cert")

		if res.StatusCode != http.StatusOK {
			t.Errorf("status\nwanted:\n%d\ngot:\n%d", http.StatusOK, res.StatusCode)
		}
	})

	t.Run("should still capture ordinary traffic", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)

		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		h.capture(t, h.client(t, "127.0.0.1"), req)

		if got := h.pairCount(t); got != 1 {
			t.Errorf("pairs in the project\nwanted:\n1\ngot:\n%d", got)
		}
	})
}

func (h *viewHarness) workshop(t *testing.T, script string) {
	t.Helper()
	workshop, ok := h.proxy.GetExtension("workshop")
	if !ok {
		t.Fatal("getting workshop extension")
	}
	if err := workshop.ExecuteLua(script); err != nil {
		t.Fatalf("updating workshop: %v", err)
	}
}

func TestProxy_BrowserView(t *testing.T) {
	t.Run("should redirect to the original URL and serve the stored response there once", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page?a=1&b=%2F", nil)
		pair := h.capture(t, browser, req)
		stored := h.storedResponse(t, pair.ID)

		location := openView(t, browser, pair.ID)
		if want := origin.URL + "/page?a=1&b=%2F"; location != want {
			t.Fatalf("Location\nwanted:\n%s\ngot:\n%s", want, location)
		}
		res, body := get(t, browser, location)

		assertServedStored(t, res, body, stored)
		for _, header := range []string{"Set-Cookie", "Strict-Transport-Security", "Cache-Control"} {
			if res.Header.Get(header) == "" {
				t.Errorf("%s\nwanted:\nthe stored value\ngot:\nnone", header)
			}
		}
		if got := origin.hitsTo("/page"); got != 1 {
			t.Errorf("requests at the origin\nwanted:\n1 (the capture)\ngot:\n%d", got)
		}
		h.assertNothingCaptured(t, 1)

		t.Run("and send the next request to the URL to the origin", func(t *testing.T) {
			_, body := get(t, browser, location)
			if !strings.Contains(string(body), "visit 2") {
				t.Errorf("second request\nwanted:\na live answer (visit 2)\ngot:\n%s", body)
			}
			if got := h.pairCount(t); got != 2 {
				t.Errorf("pairs in the project\nwanted:\n2\ngot:\n%d", got)
			}
		})
	})

	t.Run("should serve a stored POST response to the GET that follows the redirect", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodPost, origin.URL+"/submit", strings.NewReader("name=marasi"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		pair := h.capture(t, browser, req)

		res, body := get(t, browser, openView(t, browser, pair.ID))

		assertServedStored(t, res, body, h.storedResponse(t, pair.ID))
		if want := "<html>POST /submit, visit 1 name=marasi</html>"; string(body) != want {
			t.Errorf("body\nwanted:\n%s\ngot:\n%s", want, body)
		}
		if got := origin.hitsTo("/submit"); got != 1 {
			t.Errorf("requests at the origin\nwanted:\n1\ngot:\n%d", got)
		}
	})

	t.Run("should serve a stored 3xx unchanged", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/moved", nil)
		pair := h.capture(t, browser, req)

		res, body := get(t, browser, openView(t, browser, pair.ID))

		assertServedStored(t, res, body, h.storedResponse(t, pair.ID))
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/elsewhere" {
			t.Errorf("served redirect\nwanted:\n302 to /elsewhere\ngot:\n%d to %q", res.StatusCode, res.Header.Get("Location"))
		}
		if got := origin.hitsTo("/moved"); got != 1 {
			t.Errorf("requests at the origin\nwanted:\n1\ngot:\n%d", got)
		}
	})

	t.Run("should send what the page loads next to the origin and capture it", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)
		get(t, browser, openView(t, browser, pair.ID))

		get(t, browser, origin.URL+"/style.css")

		if got := origin.hitsTo("/style.css"); got != 1 {
			t.Errorf("subresource requests at the origin\nwanted:\n1\ngot:\n%d", got)
		}
		if got := h.pairCount(t); got != 2 {
			t.Errorf("pairs in the project\nwanted:\n2\ngot:\n%d", got)
		}
	})

	t.Run("should serve the stored response of a TLS origin through CONNECT", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		origin := startViewOrigin(t, true)
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "https://"))
		target := "https://localhost:" + port + "/secure"
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		pair := h.capture(t, browser, req)

		location := openView(t, browser, pair.ID)
		if location != target {
			t.Fatalf("Location\nwanted:\n%s\ngot:\n%s", target, location)
		}
		res, body := get(t, browser, location)

		assertServedStored(t, res, body, h.storedResponse(t, pair.ID))
		if got := origin.hitsTo("/secure"); got != 1 {
			t.Errorf("requests at the origin\nwanted:\n1\ngot:\n%d", got)
		}
		h.assertNothingCaptured(t, 1)
	})

	t.Run("should open a browser view over HTTPS to marasi", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)

		res, _ := get(t, browser, "https://marasi/view/"+pair.ID.String())

		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != origin.URL+"/page" {
			t.Errorf("browser view\nwanted:\n302 to %s/page\ngot:\n%d to %q", origin.URL, res.StatusCode, res.Header.Get("Location"))
		}
	})

	t.Run("should not hold the browser view or the stored response with Checkpoint armed", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)
		h.proxy.SetIntercept(true)

		res, body := get(t, browser, openView(t, browser, pair.ID))

		assertServedStored(t, res, body, h.storedResponse(t, pair.ID))
		if h.proxy.HasPendingCheckpoint() {
			t.Error("Checkpoint\nwanted:\nnothing held\ngot:\nan item held")
		}
	})

	t.Run("should not show the browser view or the stored response to extensions", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)
		h.workshop(t, `function processRequest(request) request:drop() end
function processResponse(response) response:drop() end`)

		res, body := get(t, browser, openView(t, browser, pair.ID))

		assertServedStored(t, res, body, h.storedResponse(t, pair.ID))
	})

	t.Run("should answer errors in plain text and register nothing", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		browser := h.client(t, "127.0.0.1")
		held := uuid.Must(uuid.NewV7())
		upgrade := uuid.Must(uuid.NewV7())
		h.seedPair(t, held, "http", "example.test", "/held", "")
		h.seedPair(t, upgrade, "http", "example.test", "/socket",
			"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")

		tests := []struct {
			name string
			id   string
			want int
		}{
			{name: "unknown UUID", id: uuid.Must(uuid.NewV7()).String(), want: http.StatusNotFound},
			{name: "pair number", id: "1", want: http.StatusNotFound},
			{name: "pair with no response", id: held.String(), want: http.StatusConflict},
			{name: "WebSocket upgrade", id: upgrade.String(), want: http.StatusConflict},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				res, body := get(t, browser, "http://marasi/view/"+tt.id)

				if res.StatusCode != tt.want {
					t.Errorf("status\nwanted:\n%d\ngot:\n%d %s", tt.want, res.StatusCode, body)
				}
				if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
					t.Errorf("Content-Type\nwanted:\ntext/plain\ngot:\n%q", got)
				}
				if got := res.Header.Get("Location"); got != "" {
					t.Errorf("Location\nwanted:\nnone\ngot:\n%q", got)
				}
				t.Logf("%d %s", res.StatusCode, body)
			})
		}
		h.assertNothingCaptured(t, 2)
	})
}

// seedPair stores a pair directly. An empty response leaves it without one.
func (h *viewHarness) seedPair(t *testing.T, id uuid.UUID, scheme, host, path, response string) {
	t.Helper()
	err := h.repository.InsertRequest(&domain.ProxyRequest{
		ID:          id,
		Scheme:      scheme,
		Method:      http.MethodGet,
		Host:        host,
		Path:        path,
		Raw:         domain.RawField("GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\n\r\n"),
		Metadata:    map[string]any{},
		RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("seeding request %s: %v", id, err)
	}
	if response == "" {
		return
	}
	err = h.repository.InsertResponse(&domain.ProxyResponse{
		ID:          id,
		Status:      "101 Switching Protocols",
		StatusCode:  http.StatusSwitchingProtocols,
		Raw:         domain.RawField(response),
		Metadata:    map[string]any{},
		RespondedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("seeding response %s: %v", id, err)
	}
}

func TestProxy_PendingBrowserView(t *testing.T) {
	// The listener takes every address, so a client can come from 127.0.0.1
	// or from ::1, two client addresses.
	t.Run("should serve only the client that opened it", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{listen: "all"})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		other := h.client(t, "::1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)
		location := openView(t, browser, pair.ID)

		_, otherBody := get(t, other, location)
		res, body := get(t, browser, location)

		if !strings.Contains(string(otherBody), "visit 2") {
			t.Errorf("another client's request\nwanted:\na live answer (visit 2)\ngot:\n%s", otherBody)
		}
		assertServedStored(t, res, body, h.storedResponse(t, pair.ID))
		if got := h.pairCount(t); got != 2 {
			t.Errorf("pairs in the project\nwanted:\n2 (the capture and the other client's request)\ngot:\n%d", got)
		}
	})

	t.Run("should not serve a launch and should stay pending for the browser", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)
		location := openView(t, browser, pair.ID)
		launchpadID, err := h.repository.CreateLaunchpad("launches", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}

		launched, err := h.proxy.LaunchSync(t.Context(), rawGet(origin.Server, "/page"), launchpadID.String(), false)
		if err != nil {
			t.Fatalf("launching: %v", err)
		}
		res, body := get(t, browser, location)

		if launched.Status != domain.LaunchCompleted {
			t.Errorf("launch\nwanted:\ncompleted\ngot:\n%+v", launched)
		}
		if got := origin.hitsTo("/page"); got != 2 {
			t.Errorf("requests at the origin\nwanted:\n2 (the capture and the launch)\ngot:\n%d", got)
		}
		assertServedStored(t, res, body, h.storedResponse(t, pair.ID))
	})

	t.Run("should expire unused", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{options: []func(*Proxy) error{WithBrowserViewExpiry(50 * time.Millisecond)}})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)
		location := openView(t, browser, pair.ID)

		time.Sleep(100 * time.Millisecond)
		_, body := get(t, browser, location)

		if !strings.Contains(string(body), "visit 2") {
			t.Errorf("request after the expiry\nwanted:\na live answer (visit 2)\ngot:\n%s", body)
		}
		if got := h.pairCount(t); got != 2 {
			t.Errorf("pairs in the project\nwanted:\n2\ngot:\n%d", got)
		}
	})

	t.Run("should be replaced by a newer browser view of the same URL", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		first, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		older := h.capture(t, browser, first)
		second, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		newer := h.capture(t, browser, second)

		openView(t, browser, older.ID)
		res, body := get(t, browser, openView(t, browser, newer.ID))

		assertServedStored(t, res, body, h.storedResponse(t, newer.ID))
		if !strings.Contains(string(body), "visit 2") {
			t.Errorf("body\nwanted:\nthe newer pair's response (visit 2)\ngot:\n%s", body)
		}
	})

	t.Run("should be cleared when the open project changes", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		origin := startViewOrigin(t, false)
		browser := h.client(t, "127.0.0.1")
		req, _ := http.NewRequest(http.MethodGet, origin.URL+"/page", nil)
		pair := h.capture(t, browser, req)
		location := openView(t, browser, pair.ID)

		next := newViewRepository(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
		h.flush(t)
		h.proxy.SetProjectResources(ProjectResources{Repository: next, Extensions: h.proxy.Extensions, Scope: h.proxy.Scope})
		h.repository = next
		_, body := get(t, browser, location)

		if !strings.Contains(string(body), "visit 2") {
			t.Errorf("request after the project changed\nwanted:\na live answer (visit 2)\ngot:\n%s", body)
		}
		if got := h.pairCount(t); got != 1 {
			t.Errorf("pairs in the new project\nwanted:\n1\ngot:\n%d", got)
		}
	})
}
