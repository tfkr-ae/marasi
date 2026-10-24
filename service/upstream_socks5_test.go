package service

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/internal/upstreamtest"
)

// internalOrigin is a name only the fake SOCKS5 proxy resolves, so a
// request that reaches the origin proves the proxy resolved it.
const internalOrigin = "origin.internal"

// socksService is an upstream service routed through a fake SOCKS5 proxy
// that resolves internalOrigin to the test origin.
func socksService(t *testing.T, service *upstreamProxyService, rawURL func(*upstreamtest.Proxy) string, options ...upstreamtest.Option) *upstreamtest.Proxy {
	t.Helper()
	fake := upstreamtest.NewSOCKS5(t, append([]upstreamtest.Option{upstreamtest.WithHost(internalOrigin, "127.0.0.1")}, options...)...)
	service.addUpstreamProxy(t, "jump", rawURL(fake))
	service.use(t, "jump")
	return fake
}

// internalHost returns internalOrigin with the test origin's port.
func (s *upstreamProxyService) internalHost() string {
	_, port, _ := net.SplitHostPort(s.originHost())
	return net.JoinHostPort(internalOrigin, port)
}

func TestSOCKS5UpstreamProxy(t *testing.T) {
	t.Run("should send browser traffic through a SOCKS5 upstream proxy that resolves the hostname", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		fake := socksService(t, service, func(fake *upstreamtest.Proxy) string { return fake.URL("socks5", "", "") })

		response, body := service.get(t, "http://"+service.internalHost()+"/through")
		if response.StatusCode != http.StatusOK || body != "origin /through" {
			t.Fatalf("\nwanted:\n200 origin /through\ngot:\n%d %s", response.StatusCode, body)
		}
		tunnels := fake.Tunnels()
		if len(tunnels) != 1 {
			t.Fatalf("\nwanted:\none tunnel\ngot:\n%v", fake.Targets())
		}
		tunnel := tunnels[0]
		if tunnel.Target != service.internalHost() || tunnel.AddressType != upstreamtest.AddressDomain {
			t.Fatalf("\nwanted:\n%s as a domain\ngot:\n%s with address type %d", service.internalHost(), tunnel.Target, tunnel.AddressType)
		}
		if string(tunnel.Methods) != string([]byte{upstreamtest.MethodNoAuthentication}) || tunnel.HasCredentials {
			t.Fatalf("\nwanted:\nonly no authentication offered\ngot:\nmethods %v, credentials %v", tunnel.Methods, tunnel.HasCredentials)
		}
		want := "GET /through HTTP/1.1\r\nHost: " + service.internalHost() + "\r\nUser-Agent: Go-http-client/1.1\r\n\r\n"
		if got := string(tunnel.Sent()); got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should authenticate with the entry's username and password", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		fake := socksService(t, service,
			func(fake *upstreamtest.Proxy) string { return fake.URL("socks5", "alice", "s3cret%3Apw") },
			upstreamtest.WithBehaviour(upstreamtest.RejectCredentials), upstreamtest.WithCredentials("alice", "s3cret:pw"))

		if response, body := service.get(t, "http://"+service.internalHost()+"/auth"); response.StatusCode != http.StatusOK || body != "origin /auth" {
			t.Fatalf("\nwanted:\n200 origin /auth\ngot:\n%d %s", response.StatusCode, body)
		}
		tunnels := fake.Tunnels()
		if len(tunnels) != 1 {
			t.Fatalf("\nwanted:\none tunnel\ngot:\n%v", fake.Targets())
		}
		tunnel := tunnels[0]
		if string(tunnel.Methods) != string([]byte{upstreamtest.MethodUsernamePassword}) || tunnel.Username != "alice" || tunnel.Password != "s3cret:pw" {
			t.Fatalf("\nwanted:\nusername/password as alice\ngot:\nmethods %v, %q %q", tunnel.Methods, tunnel.Username, tunnel.Password)
		}
	})

	t.Run("should send the hostname and run the TLS handshake inside the connection to an HTTPS origin", func(t *testing.T) {
		service := newUpstreamProxyService(t, marasi.WithTLS())
		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(origin.Close)
		fake := socksService(t, service, func(fake *upstreamtest.Proxy) string { return fake.URL("socks5", "", "") })
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "https://"))
		host := net.JoinHostPort(internalOrigin, port)

		// The test origin's certificate does not verify, so the request
		// fails after the ClientHello went through the connection.
		service.get(t, "https://"+host+"/secure")
		tunnels := fake.Tunnels()
		if len(tunnels) != 1 || tunnels[0].Target != host || tunnels[0].AddressType != upstreamtest.AddressDomain {
			t.Fatalf("\nwanted:\none connection to %s as a domain\ngot:\n%v", host, fake.Targets())
		}
		if sent := tunnels[0].Sent(); len(sent) == 0 || sent[0] != tlsHandshakeRecord {
			t.Fatalf("\nwanted:\na TLS handshake record\ngot:\n%q", sent)
		}
	})

	t.Run("should carry a WebSocket connection to the hostname", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		fake := socksService(t, service, func(fake *upstreamtest.Proxy) string { return fake.URL("socks5", "", "") })

		if status := service.upgradeAt(t, service.internalHost(), "/ws"); status != http.StatusSwitchingProtocols {
			t.Fatalf("\nwanted:\n101\ngot:\n%d", status)
		}
		tunnels := fake.Tunnels()
		if len(tunnels) != 1 || tunnels[0].Target != service.internalHost() || tunnels[0].AddressType != upstreamtest.AddressDomain {
			t.Fatalf("\nwanted:\none connection to %s as a domain\ngot:\n%v", service.internalHost(), fake.Targets())
		}
		if !carried(fake, "GET /ws HTTP/1.1\r\n") {
			t.Fatalf("\nwanted:\nthe upgrade through the upstream proxy\ngot:\n%q", tunnels[0].Sent())
		}
	})
}

// tlsHandshakeRecord is the first byte of a TLS handshake record.
const tlsHandshakeRecord = 0x16

// upgradeAt sends a WebSocket upgrade for path on host through the listener
// and returns the response status.
func (s *upstreamProxyService) upgradeAt(t *testing.T, host, path string) int {
	t.Helper()
	conn, err := net.Dial("tcp", s.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "GET http://%s%s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host, path, host)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading the upgrade response: %v", err)
	}
	return response.StatusCode
}

func TestSOCKS5UpstreamProxyFailsClosed(t *testing.T) {
	cases := []struct {
		name      string
		behaviour upstreamtest.Behaviour
		username  string
		cause     string
	}{
		{name: "refused", behaviour: upstreamtest.Refuse, username: "user", cause: "refused"},
		{name: "rejecting the credentials", behaviour: upstreamtest.RejectCredentials, username: "user", cause: "auth_rejected"},
		{name: "wanting credentials the entry lacks", behaviour: upstreamtest.RejectCredentials, cause: "auth_rejected"},
		{name: "hanging", behaviour: upstreamtest.Hang, username: "user", cause: "timeout"},
	}
	for _, tc := range cases {
		t.Run("should fail the request when the SOCKS5 upstream proxy is "+tc.name, func(t *testing.T) {
			service := newUpstreamProxyService(t)
			if err := service.proxy.WithOptions(marasi.WithUpstreamProxyTimeout(300 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			fake := socksService(t, service,
				func(fake *upstreamtest.Proxy) string { return fake.URL("socks5", tc.username, "s3cret-pw") },
				upstreamtest.WithBehaviour(tc.behaviour), upstreamtest.WithCredentials("right", "pass"))

			response, _ := service.get(t, "http://"+service.internalHost()+"/blocked")
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
			}
			if warning := response.Header.Get("Warning"); !strings.Contains(warning, `upstream proxy \"jump\"`) || strings.Contains(warning, "s3cret-pw") {
				t.Fatalf("\nwanted:\na Warning naming jump without credentials\ngot:\n%s", warning)
			}
			if hits := service.originHits.Load(); hits != 0 || len(fake.Targets()) != 0 {
				t.Fatalf("\nwanted:\nthe origin never reached\ngot:\n%d hits, targets %v", hits, fake.Targets())
			}
			id := service.pairID(t, "/blocked")
			entry := service.upstreamProxyLogs(t, 1)[0]
			if entry.Level != "ERROR" || entry.RequestID == nil || entry.RequestID.String() != id {
				t.Fatalf("\nwanted:\nan ERROR log for pair %s\ngot:\n%+v", id, entry)
			}
			if entry.Context["upstream_proxy"] != "jump" || entry.Context["cause"] != tc.cause || entry.Context["address"] != "socks5://"+fake.Addr {
				t.Fatalf("\nwanted:\ncontext jump, %s, socks5://%s\ngot:\n%v", tc.cause, fake.Addr, entry.Context)
			}
			if strings.Contains(entry.Message, "s3cret-pw") || !strings.Contains(entry.Message, `"jump"`) {
				t.Fatalf("\nwanted:\na message naming jump without credentials\ngot:\n%s", entry.Message)
			}
		})
	}

	t.Run("should fail the request when the SOCKS5 upstream proxy cannot reach the target", func(t *testing.T) {
		service := newUpstreamProxyService(t)
		fake := socksService(t, service, func(fake *upstreamtest.Proxy) string { return fake.URL("socks5", "", "") })

		if response, _ := service.get(t, "http://unknown.internal:1/blocked"); response.StatusCode != http.StatusBadGateway {
			t.Fatalf("\nwanted:\n502\ngot:\n%d", response.StatusCode)
		}
		entry := service.upstreamProxyLogs(t, 1)[0]
		if entry.Context["cause"] != "rejected" || len(fake.Targets()) != 1 {
			t.Fatalf("\nwanted:\na rejected log after one request to the proxy\ngot:\n%v, targets %v", entry.Context, fake.Targets())
		}
	})
}
