package marasi

import (
	"context"
	"errors"
	"net"
)

// The new engine's route (docs/agents/maps/request-path.md, Route). Each
// client connection's origin connection carries the route that opened it.
// The route is resolved before each request: a request whose origin
// connection is on another route gets a new one. A swap closes the origin
// connections of client connections waiting for their next request, and a
// request/response pair that a swap overtook closes its own once the response is read.
// The client connection stays open either way.

// errNotServing is the cause when the proxy is not serving, so no origin
// dialer is set up.
var errNotServing = errors.New("the proxy is not serving")

// dialOrigin opens a connection to addr on the current route: through the
// route's upstream proxy when there is one, with a utls handshake inside it
// when scheme is https. A dial a swap overtook runs again on the new route,
// and a failed one records the route it tried on the request's route record
// (see dialOnCurrentRoute). ctx carries the request's values.
func (proxy *Proxy) dialOrigin(ctx context.Context, scheme, addr string) (*routedConn, error) {
	dialer := proxy.dialer.Load()
	if dialer == nil {
		return nil, errNotServing
	}
	var conn net.Conn
	var err error
	if scheme == "https" {
		conn, err = dialer.dialTLS(ctx, "tcp", addr)
	} else {
		conn, err = dialer.dialPlain(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	return conn.(*routedConn), nil
}

// recordRoute records the route of the origin connection that carries the
// pair's request as the pair's route.
func recordRoute(pair *Pair, origin *originConn) {
	if pair.Route != nil {
		pair.Route.Connected(origin.route.Snapshot())
	}
}

// retireOrigin closes the origin connection once a swap has made its route
// old. The next request finds the route changed and dials anew.
func (c *h1Conn) retireOrigin() {
	if c.origin != nil && c.origin.route != c.engine.proxy.CurrentRoute() {
		c.origin.retired = true
		c.origin.conn.Close()
	}
}

// routeSwapped closes the origin connections, on a route other than route,
// of the client connections waiting for their next request. A busy one
// retires its own after its request/response pair.
func (engine *h1Engine) routeSwapped(route *Route) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for conn, idle := range engine.conns {
		// While idle, the connection's goroutine is not touching origin.
		if idle && conn.origin != nil && conn.origin.route != route {
			conn.origin.retired = true
			conn.origin.conn.Close()
		}
	}
}
