package marasi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"

	"github.com/tfkr-ae/marasi/core"

	tls "github.com/refraction-networking/utls"
	utls "github.com/refraction-networking/utls"
)

// marasiRoundTripper sends requests through the base RoundTripper without
// adding a User-Agent or Connection header. Marasi's own pages never reach it
// (see MarasiPagesModifier).
type marasiRoundTripper struct {
	base http.RoundTripper
}

// RoundTrip satisfies http.RoundTripper. It sends req through the base transport.
func (m *marasiRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "")
	}
	// The transport reads the request it sends until the response is done, while the
	// response pipeline replaces *res.Request in place. Send a copy so they never share
	// it, and hand back the original, whose address martian keys its context on.
	sent := *req
	// With Close set, the transport writes its own Connection: close unless the
	// first Connection value already says close. The copy keeps the client's
	// Connection lines as they are and adds none. The transport still closes the
	// connection when the first Connection value or the origin's answer says close.
	sent.Close = false
	res, err := m.base.RoundTrip(&sent)
	if res != nil {
		res.Request = req
	}
	return res, err
}

// routedTransport reaches the origin on the current route. It keeps one
// connection pool, an http.Transport, per route: a request takes a
// connection only from the pool of the route current when it asked, so it
// can never be carried on an idle connection a request in flight on an
// older route handed back after a swap. It is the only client that reaches
// the origin. Every other Marasi client sends through the proxy's own
// listener (see Proxy.Client).
type routedTransport struct {
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

	pool atomic.Pointer[routePool]
}

// routePool is the connection pool of one route.
type routePool struct {
	route     *Route
	transport *http.Transport
}

// newRoutedTransport builds the transport to the origin. Each request asks
// resolveRoute how to reach the origin before it takes a connection, and
// each new connection asks again, dials through the route (a tunnel when it
// is an upstream proxy), and for TLS runs a utls handshake that mimics
// Chrome inside it. A dial that a swap overtook runs again on the new route
// (see dialOnCurrentRoute). Waypoints need nothing here: their modifier has
// already rewritten the URL host to the destination, which is the tunnel's
// target.
func newRoutedTransport(resolveRoute, currentRoute func() *Route, dial func(context.Context, string, string) (net.Conn, error)) *routedTransport {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return &routedTransport{resolveRoute: resolveRoute, currentRoute: currentRoute, dial: dial}
}

// newPool builds the connection pool of route.
func (rt *routedTransport) newPool(route *Route) *routePool {
	// Compression stays off so a request without Accept-Encoding gains none
	// and the response reaches the client as the origin encoded it.
	return &routePool{route: route, transport: &http.Transport{
		DisableCompression: true,
		DialContext:        rt.dialPlain,
		DialTLSContext:     rt.dialTLS,
	}}
}

// poolFor returns the pool of the current route, starting from the route a
// request resolved. Building the pool of a new route closes the idle
// connections of the old one.
func (rt *routedTransport) poolFor(route *Route) *routePool {
	for {
		pool := rt.pool.Load()
		if current := rt.currentRoute(); current != route {
			route = current
			continue
		}
		if pool != nil && pool.route == route {
			return pool
		}
		next := rt.newPool(route)
		if rt.pool.CompareAndSwap(pool, next) {
			if pool != nil {
				pool.transport.CloseIdleConnections()
			}
			return next
		}
	}
}

// CloseIdleConnections closes the idle connections of the current route's
// pool.
func (rt *routedTransport) CloseIdleConnections() {
	if pool := rt.pool.Load(); pool != nil {
		pool.transport.CloseIdleConnections()
	}
}

// retire closes the idle connections of pool once it is no longer the
// current one. A connection that carried a request across a swap returns to
// its pool when the request finishes, after the swap closed the pool's idle
// connections, so they are closed again.
func (rt *routedTransport) retire(pool *routePool) {
	if rt.pool.Load() != pool {
		pool.transport.CloseIdleConnections()
	}
}

// RoundTrip satisfies http.RoundTripper. It resolves the route before the
// request takes a connection: when the selection or the selected entry
// changed, even by a hand edit or another instance, the request goes to the
// new route's pool, which holds no connection from the old route.
func (rt *routedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	pool := rt.poolFor(rt.resolveRoute())
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if route, ok := connectionRoute(info.Conn); ok {
			if record, ok := core.RouteRecordFromContext(req.Context()); ok {
				record.Connected(route.Snapshot())
			}
		}
	}}
	res, err := pool.transport.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if res != nil {
		res.Request = req
		// A response without a body is already back in the idle pool. One
		// with a body returns there once the body is read through. A 101
		// keeps its connection, and its body must stay that connection.
		rt.retire(pool)
		if res.Body != nil && res.Body != http.NoBody && res.StatusCode != http.StatusSwitchingProtocols {
			res.Body = &retiringBody{ReadCloser: res.Body, done: func() { rt.retire(pool) }}
		}
	}
	return res, err
}

// dialPlain opens a connection to addr on the current route.
func (rt *routedTransport) dialPlain(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialOnCurrentRoute(ctx, rt.resolveRoute, rt.currentRoute, func(route *Route) (net.Conn, error) {
		return route.dial(ctx, rt.dial, network, addr)
	})
}

// dialTLS opens a TLS connection to addr on the current route.
func (rt *routedTransport) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialOnCurrentRoute(ctx, rt.resolveRoute, rt.currentRoute, func(route *Route) (net.Conn, error) {
		return rt.openTLS(ctx, network, addr, route)
	})
}

// openTLS dials through route and, inside it, runs a utls handshake that
// mimics Chrome.
func (rt *routedTransport) openTLS(ctx context.Context, network, addr string, route *Route) (net.Conn, error) {
	tcpConn, err := route.dial(ctx, rt.dial, network, addr)
	if err != nil {
		return nil, err
	}
	sniHost, _, err := net.SplitHostPort(addr)
	if err != nil {
		sniHost = addr
	}

	roots, err := route.rootCAs()
	if err != nil {
		tcpConn.Close()
		return nil, route.fail(UpstreamProxyInvalid, err.Error(), err)
	}
	// Only connections through this route trust its CA. Direct
	// connections and other routes keep the system roots.
	uTlsConfig := &utls.Config{
		ServerName:         sniHost,
		RootCAs:            roots,
		InsecureSkipVerify: rt.insecureSkipVerify,
	}

	uConn := utls.UClient(tcpConn, uTlsConfig, utls.HelloChrome_Auto)

	// Every failure below closes tcpConn: when the route is a tunnel
	// through an upstream proxy, nothing else would.
	if err := uConn.BuildHandshakeState(); err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("building handshake state: %w", err)
	}

	foundALPN := false
	// HelloChrome_Auto will ignore uTLSConfig.NextProtos and accept H2
	// This will loop over all the TLSExtensions and set the ALPNExtension to accept
	// http/1.1 only. This needs to be done before .HandshakeContext
	for _, ext := range uConn.Extensions {
		if alpnExt, ok := ext.(*tls.ALPNExtension); ok {
			alpnExt.AlpnProtocols = []string{"http/1.1"}
			foundALPN = true
			break
		}
	}

	if !foundALPN {
		tcpConn.Close()
		return nil, errors.New("could not find ALPNExtension")
	}

	if err := uConn.HandshakeContext(ctx); err != nil {
		tcpConn.Close()
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
