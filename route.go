package marasi

import (
	"context"
	"io"
	"net"
	"sync"
)

// Route says how the transport reaches the origin for a new connection. The
// proxy holds the current route behind an atomic pointer, and every
// connection the transport opens carries the route that opened it. A swap
// installs a new *Route, so a connection belongs to the current route only
// when it carries that same pointer.
type Route struct {
	// Name is the upstream proxy's name. It is empty for a direct route.
	Name string
}

// DirectRoute returns a route that connects to the origin directly.
func DirectRoute() *Route { return &Route{} }

// dialFunc opens a network connection, as net.Dialer.DialContext does.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// dial opens the connection the transport speaks HTTP, or runs the utls
// handshake, over. address is the origin after any waypoint rewrite.
func (route *Route) dial(ctx context.Context, dial dialFunc, network, address string) (net.Conn, error) {
	return dial(ctx, network, address)
}

// routedConn is a connection to the origin together with the route that
// opened it.
type routedConn struct {
	net.Conn
	route *Route
}

// connectionRoute returns the route that opened a connection the transport
// handed out, such as httptrace.GotConnInfo.Conn.
func connectionRoute(conn net.Conn) (*Route, bool) {
	routed, ok := conn.(*routedConn)
	if !ok {
		return nil, false
	}
	return routed.route, true
}

// CurrentRoute returns the route new connections to the origin use.
func (proxy *Proxy) CurrentRoute() *Route {
	return proxy.route.Load()
}

// SetRoute makes route the one new connections to the origin use. It closes
// the transport's idle connections so none opened on the old route is
// reused. Connections carrying a request keep it until it finishes and are
// not reused afterwards (see marasiRoundTripper.retireStale). The listener
// keeps running.
func (proxy *Proxy) SetRoute(route *Route) {
	proxy.route.Store(route)
	if transport := proxy.transport.Load(); transport != nil {
		transport.CloseIdleConnections()
	}
}

// retiringBody runs done once, after the transport has finished with the
// body's connection: on the first error, including io.EOF, or on Close.
type retiringBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (body *retiringBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if err != nil {
		body.once.Do(body.done)
	}
	return n, err
}

func (body *retiringBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.done)
	return err
}
