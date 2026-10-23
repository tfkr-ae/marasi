package service

import (
	"crypto/tls"
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

func TestHTTPSUpstreamProxy(t *testing.T) {
	t.Run("should speak TLS to an HTTPS upstream proxy its entry's CA signed, then CONNECT", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		ca := upstreamtest.NewCA(t, "Corp proxy CA")
		fake := upstreamtest.NewHTTPS(t, ca, upstreamtest.WithBehaviour(upstreamtest.RejectCredentials), upstreamtest.WithCredentials("alice", "pw"))
		service.addUpstreamProxyWithCA(t, "corp", fake.URL("https", "alice", "pw"), ca.PEM())
		service.use(t, "corp")

		response, body := service.get(t, service.origin.URL+"/over-tls")
		if response.StatusCode != http.StatusOK || body != "origin /over-tls" {
			t.Fatalf("\nwanted:\n200 origin /over-tls\ngot:\n%d %s", response.StatusCode, body)
		}
		tunnels := fake.Tunnels()
		if len(tunnels) != 1 || tunnels[0].Target != service.originHost() || tunnels[0].Username != "alice" {
			t.Fatalf("\nwanted:\none CONNECT to %s as alice\ngot:\n%v", service.originHost(), fake.Targets())
		}
		if !carried(fake, "GET /over-tls HTTP/1.1\r\n") {
			t.Fatalf("\nwanted:\n/over-tls through the tunnel\ngot:\n%q", tunnels[0].Sent())
		}
	})

	t.Run("should verify an HTTPS origin inside the TLS connection to the upstream proxy", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		ca := upstreamtest.NewCA(t, "Corp proxy CA")
		origin := newCASignedOrigin(t, ca)
		fake := upstreamtest.NewHTTPS(t, ca)
		service.addUpstreamProxyWithCA(t, "corp", fake.URL("https", "", ""), ca.PEM())
		service.use(t, "corp")

		response, body := service.get(t, origin.URL+"/nested")
		if response.StatusCode != http.StatusOK || body != "signed origin /nested" {
			t.Fatalf("\nwanted:\n200 signed origin /nested\ngot:\n%d %s", response.StatusCode, body)
		}
		if got := fake.Targets(); len(got) != 1 || got[0] != hostOf(origin.URL) {
			t.Fatalf("\nwanted:\n[%s]\ngot:\n%v", hostOf(origin.URL), got)
		}
	})

	t.Run("should fail closed naming TLS verification when the upstream proxy's certificate is not trusted", func(t *testing.T) {
		service := newUpstreamService(t, marasi.WithTLS())
		ca := upstreamtest.NewCA(t, "Corp proxy CA")
		fake := upstreamtest.NewHTTPS(t, ca)
		service.addUpstreamProxy(t, "corp", fake.URL("https", "", ""))
		service.use(t, "corp")

		response, _ := service.get(t, service.origin.URL+"/untrusted-proxy")
		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
		}
		if hits := service.originHits.Load(); hits != 0 || len(fake.Tunnels()) != 0 {
			t.Fatalf("\nwanted:\nno CONNECT and no origin hit\ngot:\n%v, %d hits", fake.Targets(), hits)
		}
		assertTLSVerificationLog(t, service, "/untrusted-proxy", "corp", "https://"+fake.Addr)
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
	// httptest presents its own certificate unless Certificates is set.
	leaf, err := ca.Leaf("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	origin.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}}
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
