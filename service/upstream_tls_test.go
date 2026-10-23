package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/internal/upstreamtest"
)

// addUpstreamProxyWithCA adds an entry that trusts caPEM.
func (s *upstreamService) addUpstreamProxyWithCA(t *testing.T, name, rawURL, caPEM string) {
	t.Helper()
	s.mustCall(t, http.MethodPost, "/upstream-proxy", fmt.Sprintf(`{"name":%q,"url":%q,"ca":%q}`, name, rawURL, caPEM))
}

// newTLSOrigin starts an HTTPS origin whose certificate no client trusts
// unless an intercepting upstream proxy re-signs it.
func newTLSOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls origin "+r.URL.Path)
	}))
	t.Cleanup(origin.Close)
	return origin
}

func TestUpstreamProxyTLSTrust(t *testing.T) {
	t.Run("should verify origin certificates signed by the CA of the intercepting upstream proxy it is attached to", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		origin := newTLSOrigin(t)
		ca := upstreamtest.NewCA(t, "Fake Burp CA")
		fake := upstreamtest.NewIntercepting(t, ca)
		service.addUpstreamProxyWithCA(t, "burp", fake.URL("http", "", ""), ca.PEM())
		service.use(t, "burp")

		response, body := service.get(t, origin.URL+"/trusted")
		if response.StatusCode != http.StatusOK || body != "tls origin /trusted" {
			t.Fatalf("\nwanted:\n200 tls origin /trusted\ngot:\n%d %s", response.StatusCode, body)
		}
		if !carried(fake, "GET /trusted HTTP/1.1\r\n") {
			t.Fatalf("\nwanted:\n/trusted through the intercepting upstream proxy\ngot:\ntargets %v", fake.Targets())
		}
	})

	t.Run("should fail closed naming TLS verification when the intercepting upstream proxy's entry has no CA", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		origin := newTLSOrigin(t)
		ca := upstreamtest.NewCA(t, "Fake Burp CA")
		fake := upstreamtest.NewIntercepting(t, ca)
		service.addUpstreamProxy(t, "burp", fake.URL("http", "", ""))
		service.use(t, "burp")

		response, _ := service.get(t, origin.URL+"/untrusted")
		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
		}
		if carried(fake, "GET /untrusted") {
			t.Fatalf("\nwanted:\nno request sent over the unverified connection\ngot:\n%q", fake.Tunnels()[0].Sent())
		}
		assertTLSVerificationLog(t, service, "/untrusted", "burp", "http://"+fake.Addr)
	})

	t.Run("should not trust an entry's CA on a direct connection", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		ca := upstreamtest.NewCA(t, "Fake Burp CA")
		origin := newCASignedOrigin(t, ca)
		fake := upstreamtest.NewIntercepting(t, ca)
		service.addUpstreamProxyWithCA(t, "burp", fake.URL("http", "", ""), ca.PEM())
		service.use(t, "burp")
		if response, _ := service.get(t, origin.URL+"/through?close"); response.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\n200 through burp\ngot:\n%d", response.StatusCode)
		}
		service.use(t, "")

		if response, _ := service.get(t, origin.URL+"/direct"); response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502 for a direct connection to an origin only the CA signed\ngot:\n%d", response.StatusCode)
		}
		if len(fake.Tunnels()) != 1 {
			t.Fatalf("\nwanted:\nonly the first request through burp\ngot:\n%v", fake.Targets())
		}
	})

	t.Run("should not trust one entry's CA through another upstream proxy", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		ca := upstreamtest.NewCA(t, "Fake Burp CA")
		origin := newCASignedOrigin(t, ca)
		burp := upstreamtest.NewIntercepting(t, ca)
		plain := upstreamtest.NewHTTP(t)
		service.addUpstreamProxyWithCA(t, "burp", burp.URL("http", "", ""), ca.PEM())
		service.addUpstreamProxy(t, "corp", plain.URL("http", "", ""))
		service.use(t, "corp")

		if response, _ := service.get(t, origin.URL+"/other"); response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
		}
		assertTLSVerificationLog(t, service, "/other", "corp", "http://"+plain.Addr)
	})
}

// newCASignedOrigin starts an HTTPS origin whose certificate ca signed.
func newCASignedOrigin(t *testing.T, ca *upstreamtest.CA) *httptest.Server {
	t.Helper()
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("close") {
			w.Header().Set("Connection", "close")
		}
		_, _ = io.WriteString(w, "signed origin "+r.URL.Path)
	}))
	origin.TLS = ca.ServerConfig()
	origin.StartTLS()
	t.Cleanup(origin.Close)
	return origin
}

// assertTLSVerificationLog checks the one proxy log for the pair at path
// names the upstream proxy and TLS verification.
func assertTLSVerificationLog(t *testing.T, service *upstreamService, path, name, address string) {
	t.Helper()
	id := service.pairID(t, path)
	entry := service.upstreamLogs(t, 1)[0]
	if entry.Level != "ERROR" || entry.RequestID == nil || entry.RequestID.String() != id {
		t.Fatalf("\nwanted:\nan ERROR log for pair %s\ngot:\n%+v", id, entry)
	}
	if entry.Context["upstream_proxy"] != name || entry.Context["cause"] != "tls_verification" || entry.Context["address"] != address {
		t.Fatalf("\nwanted:\ncontext %s, tls_verification, %s\ngot:\n%v", name, address, entry.Context)
	}
	if !strings.Contains(entry.Message, "TLS verification") {
		t.Fatalf("\nwanted:\na message naming TLS verification\ngot:\n%s", entry.Message)
	}
}

// hostOf returns the host:port of an httptest server URL.
func hostOf(rawURL string) string {
	_, host, _ := strings.Cut(rawURL, "://")
	return host
}
