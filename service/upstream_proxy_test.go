package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
)

func TestUpstreamProxyControlRoutes(t *testing.T) {
	t.Run("should add and list an entry redacted with an exact response and event", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		assertChromeResponse(t, requestChrome(server, http.MethodGet, "/upstream-proxy", ""), http.StatusOK, `{"items":[],"selected":"","configured":true}`+"\n")
		assertNoChromeEvent(t, subscriber)

		body := upstreamProxyBody(t, map[string]any{"name": "burp", "url": "http://alice:s3cret@127.0.0.1:8080", "ca": testUpstreamCAPEM(t)})
		want := `{"items":[{"name":"burp","scheme":"http","host":"127.0.0.1","port":8080,"has_credentials":true,"has_ca":true}]}`
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/upstream-proxy", body), http.StatusOK, want+"\n")
		assertChromeEvent(t, subscriber, "upstream_proxy.added", want)
		assertChromeResponse(t, requestChrome(server, http.MethodGet, "/upstream-proxy", ""), http.StatusOK, listing(want))
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should accept http https and socks5 with and without credentials", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		for _, entry := range []struct{ name, url string }{
			{"plain", "http://proxy.internal:3128"},
			{"tls", "https://bob@proxy.example:443"},
			{"jump", "socks5://user:pass@[::1]:1080"},
		} {
			response := requestChrome(server, http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": entry.name, "url": entry.url}))
			if response.Code != http.StatusOK {
				t.Fatalf("\nwanted:\n200 for %s\ngot:\n%d %s", entry.url, response.Code, response.Body.String())
			}
		}
		want := `{"items":[` +
			`{"name":"plain","scheme":"http","host":"proxy.internal","port":3128,"has_credentials":false,"has_ca":false},` +
			`{"name":"tls","scheme":"https","host":"proxy.example","port":443,"has_credentials":true,"has_ca":false},` +
			`{"name":"jump","scheme":"socks5","host":"::1","port":1080,"has_credentials":true,"has_ca":false}]}` + "\n"
		assertChromeResponse(t, requestChrome(server, http.MethodGet, "/upstream-proxy", ""), http.StatusOK, listing(strings.TrimSuffix(want, "\n")))
	})

	t.Run("should refuse invalid names urls and CAs without an event", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		for _, body := range []map[string]any{
			{"name": "Burp", "url": "http://127.0.0.1:8080"},
			{"name": "-burp", "url": "http://127.0.0.1:8080"},
			{"name": "", "url": "http://127.0.0.1:8080"},
			{"name": "burp", "url": "ftp://127.0.0.1:21"},
			{"name": "burp", "url": "socks4://127.0.0.1:1080"},
			{"name": "burp", "url": "http://127.0.0.1"},
			{"name": "burp", "url": "http://:8080"},
			{"name": "burp", "url": "http://127.0.0.1:0"},
			{"name": "burp", "url": "http://127.0.0.1:99999"},
			{"name": "burp", "url": "http://127.0.0.1:8080/path"},
			{"name": "burp", "url": "http://127.0.0.1:8080?x=1"},
			{"name": "burp", "url": "127.0.0.1:8080"},
			{"name": "burp", "url": ""},
			{"name": "burp", "url": "http://127.0.0.1:8080", "ca": "not a pem"},
			{"name": "burp", "url": "http://127.0.0.1:8080", "ca": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"},
		} {
			response := requestChrome(server, http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, body))
			assertUpstreamProxyRefusal(t, response, http.StatusBadRequest, "invalid_upstream_proxy")
		}
		for _, body := range []string{``, `null`, `{}`, `{"name":"burp"}`, `{"url":"http://127.0.0.1:8080"}`, `{"name":"burp","url":"http://127.0.0.1:8080","extra":1}`, `{"name":"burp","url":"http://127.0.0.1:8080"} {}`} {
			assertUpstreamProxyRefusal(t, requestChrome(server, http.MethodPost, "/upstream-proxy", body), http.StatusBadRequest, "invalid_upstream_proxy")
		}
		assertNoChromeEvent(t, subscriber)
		assertChromeResponse(t, requestChrome(server, http.MethodGet, "/upstream-proxy", ""), http.StatusOK, `{"items":[],"selected":"","configured":true}`+"\n")
	})

	t.Run("should refuse a duplicate name", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		body := upstreamProxyBody(t, map[string]any{"name": "burp", "url": "http://127.0.0.1:8080"})
		if response := requestChrome(server, http.MethodPost, "/upstream-proxy", body); response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		other := upstreamProxyBody(t, map[string]any{"name": "burp", "url": "socks5://127.0.0.1:1080"})
		assertUpstreamProxyRefusal(t, requestChrome(server, http.MethodPost, "/upstream-proxy", other), http.StatusConflict, "upstream_proxy_already_exists")
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should update the url and the CA and remove the CA", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		requestChrome(server, http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "burp", "url": "http://127.0.0.1:8080"}))
		<-subscriber.events

		want := `{"items":[{"name":"burp","scheme":"socks5","host":"jump.example","port":1080,"has_credentials":true,"has_ca":false}]}`
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/upstream-proxy/burp", `{"url":"socks5://u:p@jump.example:1080"}`), http.StatusOK, want+"\n")
		assertChromeEvent(t, subscriber, "upstream_proxy.updated", want)

		want = `{"items":[{"name":"burp","scheme":"socks5","host":"jump.example","port":1080,"has_credentials":true,"has_ca":true}]}`
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/upstream-proxy/burp", upstreamProxyBody(t, map[string]any{"ca": testUpstreamCAPEM(t)})), http.StatusOK, want+"\n")
		assertChromeEvent(t, subscriber, "upstream_proxy.updated", want)

		want = `{"items":[{"name":"burp","scheme":"socks5","host":"jump.example","port":1080,"has_credentials":true,"has_ca":false}]}`
		assertChromeResponse(t, requestChrome(server, http.MethodPost, "/upstream-proxy/burp", `{"ca":null}`), http.StatusOK, want+"\n")
		assertChromeEvent(t, subscriber, "upstream_proxy.updated", want)

		for _, body := range []string{``, `{}`, `{"url":null}`, `{"url":"ftp://x:1"}`, `{"ca":"junk"}`, `{"ca":""}`, `{"other":1}`} {
			assertUpstreamProxyRefusal(t, requestChrome(server, http.MethodPost, "/upstream-proxy/burp", body), http.StatusBadRequest, "invalid_upstream_proxy")
		}
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should remove an entry and report missing names as not found", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		requestChrome(server, http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "burp", "url": "http://127.0.0.1:8080"}))
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		assertChromeResponse(t, requestChrome(server, http.MethodDelete, "/upstream-proxy/burp", ""), http.StatusOK, `{"items":[]}`+"\n")
		assertChromeEvent(t, subscriber, "upstream_proxy.removed", `{"items":[]}`)
		assertUpstreamProxyRefusal(t, requestChrome(server, http.MethodDelete, "/upstream-proxy/burp", ""), http.StatusNotFound, "not_found")
		assertUpstreamProxyRefusal(t, requestChrome(server, http.MethodPost, "/upstream-proxy/burp", `{"url":"http://127.0.0.1:8080"}`), http.StatusNotFound, "not_found")
		assertNoChromeEvent(t, subscriber)
	})

	t.Run("should never return a credential or a PEM", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		ca := testUpstreamCAPEM(t)
		var seen strings.Builder
		for _, request := range []struct{ method, path, body string }{
			{http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "burp", "url": "http://alice:hunter2@127.0.0.1:8080", "ca": ca})},
			{http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "burp", "url": "http://alice:hunter2@127.0.0.1:8080"})},
			{http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "bad", "url": "ftp://alice:hunter2@127.0.0.1:8080"})},
			{http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "bad", "url": "http://alice:hunter2@127.0.0.1"})},
			{http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "bad", "url": "http://alice:hunter2@127.0.0.1:8080/%zz"})},
			{http.MethodPost, "/upstream-proxy/burp", `{"url":"socks5://alice:hunter2@127.0.0.1:1080"}`},
			{http.MethodGet, "/upstream-proxy", ""},
			{http.MethodDelete, "/upstream-proxy/burp", ""},
		} {
			seen.WriteString(requestChrome(server, request.method, request.path, request.body).Body.String())
		}
		for {
			select {
			case event := <-subscriber.events:
				seen.Write(event.data)
				continue
			default:
			}
			break
		}
		for _, secret := range []string{"alice", "hunter2", "BEGIN CERTIFICATE", strings.Split(ca, "\n")[1]} {
			if strings.Contains(seen.String(), secret) {
				t.Fatalf("\nwanted:\nno %q in responses or events\ngot:\n%s", secret, seen.String())
			}
		}
	})

	t.Run("should write the yaml with owner-only permissions and keep the PEM in it", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		path := filepath.Join(server.proxy.ConfigDir, "marasi_config.yaml")
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatalf("loosening config permissions: %v", err)
		}
		ca := testUpstreamCAPEM(t)
		requestChrome(server, http.MethodPost, "/upstream-proxy", upstreamProxyBody(t, map[string]any{"name": "burp", "url": "http://alice:hunter2@127.0.0.1:8080", "ca": ca}))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("reading config permissions: %v", err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("\nwanted:\n-rw-------\ngot:\n%v", info.Mode().Perm())
		}
		cfg, err := marasi.ReadConfig(server.proxy.ConfigDir)
		if err != nil {
			t.Fatalf("reading config: %v", err)
		}
		want := []marasi.UpstreamProxyConfig{{Name: "burp", URL: "http://alice:hunter2@127.0.0.1:8080", CA: ca}}
		if len(cfg.UpstreamProxies) != 1 || cfg.UpstreamProxies[0] != want[0] {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, cfg.UpstreamProxies)
		}
	})

	t.Run("should pick up hand edits to the yaml", func(t *testing.T) {
		server := newChromeHTTPServer(t, ListenerStatus{Status: ListenerInactive})
		path := filepath.Join(server.proxy.ConfigDir, "marasi_config.yaml")
		yaml := "upstream_proxies:\n  - name: corp\n    url: http://proxy.corp:3128\n"
		if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
			t.Fatalf("writing config: %v", err)
		}
		want := `{"items":[{"name":"corp","scheme":"http","host":"proxy.corp","port":3128,"has_credentials":false,"has_ca":false}]}` + "\n"
		assertChromeResponse(t, requestChrome(server, http.MethodGet, "/upstream-proxy", ""), http.StatusOK, listing(strings.TrimSuffix(want, "\n")))
	})
}

func TestUpstreamProxyConfigSharing(t *testing.T) {
	t.Run("should reread the config so two modules keep each other's changes", func(t *testing.T) {
		configDir := t.TempDir()
		first := NewUpstreamProxies(configDir)
		second := NewUpstreamProxies(configDir)
		newChromeTestProxy(t, configDir)

		if _, err := first.Add(context.Background(), marasi.UpstreamProxyConfig{Name: "one", URL: "http://127.0.0.1:8080"}); err != nil {
			t.Fatalf("adding first: %v", err)
		}
		if _, err := second.Add(context.Background(), marasi.UpstreamProxyConfig{Name: "two", URL: "socks5://127.0.0.1:1080"}); err != nil {
			t.Fatalf("adding second: %v", err)
		}
		items, err := second.Remove(context.Background(), "one")
		if err != nil {
			t.Fatalf("removing first: %v", err)
		}
		if len(items) != 1 || items[0].Name != "two" {
			t.Fatalf("\nwanted:\n[two]\ngot:\n%+v", items)
		}
		items, err = first.List(context.Background())
		if err != nil || len(items) != 1 || items[0].Name != "two" {
			t.Fatalf("\nwanted:\n[two]\ngot:\n%+v, %v", items, err)
		}
	})

	t.Run("should serialize overlapping adds from two modules without losing one", func(t *testing.T) {
		configDir := t.TempDir()
		newChromeTestProxy(t, configDir)
		modules := []*UpstreamProxies{NewUpstreamProxies(configDir), NewUpstreamProxies(configDir)}
		start := make(chan struct{})
		errs := make(chan error, 8)
		for i := range 8 {
			go func() {
				<-start
				_, err := modules[i%2].Add(context.Background(), marasi.UpstreamProxyConfig{Name: "p" + string(rune('0'+i)), URL: "http://127.0.0.1:8080"})
				errs <- err
			}()
		}
		close(start)
		for range 8 {
			if err := <-errs; err != nil {
				t.Fatalf("adding: %v", err)
			}
		}
		items, err := modules[0].List(context.Background())
		if err != nil || len(items) != 8 {
			t.Fatalf("\nwanted:\n8 entries\ngot:\n%+v, %v", items, err)
		}
	})

	t.Run("should keep Chrome paths and upstream proxies when both are edited", func(t *testing.T) {
		configDir := t.TempDir()
		chrome := NewChrome(newChromeTestProxy(t, configDir), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, nil)
		upstream := NewUpstreamProxies(configDir)
		if _, err := upstream.Add(context.Background(), marasi.UpstreamProxyConfig{Name: "burp", URL: "http://127.0.0.1:8080"}); err != nil {
			t.Fatalf("adding upstream proxy: %v", err)
		}
		if _, err := chrome.AddProfile(context.Background(), "pentest"); err != nil {
			t.Fatalf("adding chrome profile: %v", err)
		}
		items, err := upstream.List(context.Background())
		if err != nil || len(items) != 1 {
			t.Fatalf("\nwanted:\n[burp]\ngot:\n%+v, %v", items, err)
		}
		profiles, err := chrome.Profiles(context.Background())
		if err != nil || len(profiles) != 1 {
			t.Fatalf("\nwanted:\n[pentest]\ngot:\n%v, %v", profiles, err)
		}
	})
}

func upstreamProxyBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encoding body: %v", err)
	}
	return string(body)
}

func assertUpstreamProxyRefusal(t *testing.T, response interface {
	Result() *http.Response
}, status int, code string) {
	t.Helper()
	result := response.Result()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(result.Body).Decode(&body); err != nil || result.StatusCode != status || body.Error != code {
		t.Fatalf("\nwanted:\n%d %s\ngot:\n%d %+v %v", status, code, result.StatusCode, body, err)
	}
}

// testUpstreamCAPEM returns a self-signed CA certificate standing in for
// Burp's CA.
func testUpstreamCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Upstream CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// listing is GET /upstream-proxy's body for list, a {"items":[…]} body, with
// nothing selected.
func listing(list string) string {
	return strings.TrimSuffix(list, "}") + `,"selected":"","configured":true}` + "\n"
}
