package marasi

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/tfkr-ae/marasi/core"

	utls "github.com/refraction-networking/utls"
)

// originDialer opens the engine's origin connections on the current route.
// It is the only thing that connects to the origin: every other Marasi client
// sends through the proxy's own listener (see Proxy.Client).
type originDialer struct {
	// resolveRoute brings the current route up to date with the selection
	// and the list, swapping it when what they resolve to changed.
	// currentRoute returns the route new connections use without resolving
	// it.
	resolveRoute func() *Route
	currentRoute func() *Route
	dial         func(context.Context, string, string) (net.Conn, error)
	// insecureSkipVerify turns off origin certificate verification. Only
	// tests set it, to reach a self-signed origin.
	insecureSkipVerify bool
}

// newOriginDialer builds the dialer to the origin. Each new connection asks
// resolveRoute how to reach the origin, dials through the route (a tunnel
// when it is an upstream proxy), and for TLS runs a utls handshake that
// mimics Chrome inside it. A dial that a swap overtook runs again on the new
// route (see dialOnCurrentRoute). Waypoints need nothing here: their modifier
// has already rewritten the URL host to the destination, which is the
// tunnel's target.
func newOriginDialer(resolveRoute, currentRoute func() *Route, dial func(context.Context, string, string) (net.Conn, error)) *originDialer {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return &originDialer{resolveRoute: resolveRoute, currentRoute: currentRoute, dial: dial}
}

// dialPlain opens a connection to addr on the current route.
func (rt *originDialer) dialPlain(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialOnCurrentRoute(ctx, rt.resolveRoute, rt.currentRoute, func(route *Route) (net.Conn, error) {
		return route.dial(ctx, rt.dial, network, addr)
	})
}

// dialTLS opens a TLS connection to addr on the current route.
func (rt *originDialer) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialOnCurrentRoute(ctx, rt.resolveRoute, rt.currentRoute, func(route *Route) (net.Conn, error) {
		return rt.openTLS(ctx, network, addr, route)
	})
}

// openTLS dials through route and, inside it, runs a utls handshake that
// mimics Chrome.
func (rt *originDialer) openTLS(ctx context.Context, network, addr string, route *Route) (net.Conn, error) {
	tcpConn, err := route.dial(ctx, rt.dial, network, addr)
	if err != nil {
		return nil, err
	}
	return handshakeOrigin(ctx, tcpConn, addr, route, rt.insecureSkipVerify)
}

// handshakeOrigin runs a utls handshake that mimics Chrome with the origin
// at addr over conn, opened through route, offering http/1.1 only. It
// verifies the origin against the system roots plus the route's CA, unless
// insecure is set (tests only). It closes conn when the handshake fails.
func handshakeOrigin(ctx context.Context, conn net.Conn, addr string, route *Route, insecure bool) (net.Conn, error) {
	sniHost, _, err := net.SplitHostPort(addr)
	if err != nil {
		sniHost = addr
	}

	roots, err := route.rootCAs()
	if err != nil {
		conn.Close()
		return nil, route.fail(UpstreamProxyInvalid, err.Error(), err)
	}
	// Only connections through this route trust its CA. Direct
	// connections and other routes keep the system roots.
	uTlsConfig := &utls.Config{
		ServerName:         sniHost,
		RootCAs:            roots,
		InsecureSkipVerify: insecure,
	}

	uConn := utls.UClient(conn, uTlsConfig, utls.HelloChrome_Auto)

	// Every failure below closes conn: when the route is a tunnel
	// through an upstream proxy, nothing else would.
	if err := uConn.BuildHandshakeState(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("building handshake state: %w", err)
	}

	foundALPN := false
	// HelloChrome_Auto will ignore uTLSConfig.NextProtos and accept H2
	// This will loop over all the TLSExtensions and set the ALPNExtension to accept
	// http/1.1 only. This needs to be done before .HandshakeContext
	for _, ext := range uConn.Extensions {
		if alpnExt, ok := ext.(*utls.ALPNExtension); ok {
			alpnExt.AlpnProtocols = []string{"http/1.1"}
			foundALPN = true
			break
		}
	}

	if !foundALPN {
		conn.Close()
		return nil, errors.New("could not find ALPNExtension")
	}

	if err := uConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, route.originHandshakeFailure(err)
	}

	return uConn, nil
}

// dialOnCurrentRoute opens a connection with open on the resolved route.
// When the route was swapped while open ran, its result, a connection or an
// error, belongs to the old route: it is discarded and open runs again on
// the route now current, so no request is carried on a route already
// swapped out. When the final attempt fails, its route is recorded as the
// one the request tried (see recordTried).
func dialOnCurrentRoute(ctx context.Context, resolveRoute, currentRoute func() *Route, open func(*Route) (net.Conn, error)) (net.Conn, error) {
	route := resolveRoute()
	for {
		conn, err := open(route)
		if current := currentRoute(); current != route {
			if conn != nil {
				conn.Close()
			}
			route = current
			continue
		}
		if err != nil {
			recordTried(ctx, route)
			return nil, err
		}
		return &routedConn{Conn: conn, route: route}, nil
	}
}

// recordTried records route on the route record of the request whose dial
// failed, so its pair's snapshot holds the route that was tried. The dial
// context carries the request's values.
func recordTried(ctx context.Context, route *Route) {
	if record, ok := core.RouteRecordFromContext(ctx); ok {
		record.Tried(route.Snapshot())
	}
}
