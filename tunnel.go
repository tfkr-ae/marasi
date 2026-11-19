package marasi

import (
	"net"
	"net/http"
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
// listener. localhost and 127.0.0.1 are the same host, and a missing port is
// the scheme's default.
func (proxy *Proxy) targetsListener(req *http.Request) bool {
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		host = req.Host
		if req.URL.Scheme == "https" || req.TLS != nil {
			port = "443"
		} else {
			port = "80"
		}
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	listenerAddr := proxy.Addr
	if listenerAddr == "localhost" {
		listenerAddr = "127.0.0.1"
	}
	return host == listenerAddr && port == proxy.Port
}
