package marasi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tfkr-ae/marasi/listener"
)

// clientConnection is a client's connection to the proxy listener and the
// state that lasts as long as it does.
type clientConnection struct {
	*listener.WatchedConn
	// selfClient is set when the proxy's own client opened a tunnel on this
	// connection. Every request inside the tunnel is then the proxy's own.
	selfClient atomic.Bool
	// serving is set under proxy.connectionMu once the engine accepted this
	// connection and took over removing it from clientConnections and closing
	// served.
	serving bool
	// served is closed once the engine's handler for this connection returns
	// (or when the connection closes before the engine accepted it), after
	// every stream and streamed pair on it has finished and queued its capture.
	served     chan struct{}
	servedOnce sync.Once
}

func (c *clientConnection) markServed() {
	if c != nil {
		c.servedOnce.Do(func() { close(c.served) })
	}
}

// canonicalClientAddr normalizes an IP:port address so an IPv4-mapped IPv6
// peer address on a dual-stack listener matches the IPv4 local address the
// client sees.
func canonicalClientAddr(addr string) string {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return addr
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String()
}

// admitTunnel is the admission hook for CONNECT, which never enters the pair
// chains. It decides whether the tunnel opens and returns the refusal when it
// does not. internal reports whether the CONNECT carried the proxy's own
// credentials, which marks the whole connection as the proxy's own client.
func (proxy *Proxy) admitTunnel(req *http.Request, internal bool) error {
	if internal {
		if client := proxy.clientConnection(req.RemoteAddr); client != nil {
			client.selfClient.Store(true)
		}
	}
	release, _, err := proxy.admitWork(req.Context(), internal)
	if err != nil {
		return err
	}
	release()
	// Compass scope and the loop check judge each request inside the tunnel,
	// so neither refuses a tunnel, as before.
	inScope := proxy.Scope == nil || proxy.Scope.Matches(req)
	if loop := proxy.targetsListener(req); (loop || !inScope) && proxy.Logger != nil {
		proxy.Logger.Debug("admitting tunnel; its requests are judged one by one", "host", req.Host, "in_scope", inScope, "loop", loop)
	}
	return nil
}

// isSelfClientConnection reports whether the proxy's own client opened a
// tunnel on the client connection at remoteAddr.
func (proxy *Proxy) isSelfClientConnection(remoteAddr string) bool {
	client := proxy.clientConnection(remoteAddr)
	return client != nil && client.selfClient.Load()
}

// targetsListener reports whether req is addressed to the proxy's own
// listener. It judges the host the request is sent to, its URL's, which
// inside a tunnel is the tunnel's origin whatever the Host field says
// (docs/adr/2026-11-19-requests-inside-a-tunnel-go-to-the-tunnels-origin.md),
// and falls back to the Host field only when the URL names no host.
// localhost, in any case, and 127.0.0.1 are the same host, and a missing port
// is the scheme's default.
func (proxy *Proxy) targetsListener(req *http.Request) bool {
	authority := req.Host
	if req.URL != nil && req.URL.Host != "" {
		authority = req.URL.Host
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		host = authority
		if (req.URL != nil && req.URL.Scheme == "https") || req.TLS != nil {
			port = "443"
		} else {
			port = "80"
		}
	}
	return loopbackAlias(host) == loopbackAlias(proxy.Addr) && port == proxy.Port
}

// loopbackAlias is host with localhost, in any case, written as 127.0.0.1.
func loopbackAlias(host string) string {
	if strings.EqualFold(host, "localhost") {
		return "127.0.0.1"
	}
	return host
}
