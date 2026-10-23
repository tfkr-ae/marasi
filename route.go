package marasi

import (
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Route says how the transport reaches the origin for a new connection. The
// proxy holds the current route behind an atomic pointer, and every
// connection the transport opens carries the route that opened it. A swap
// installs a new *Route, so a connection belongs to the current route only
// when it carries that same pointer. The route resolver (route_resolver.go)
// builds routes, and Route.dial (route_dial.go) applies them.
type Route struct {
	// Name is the upstream proxy's name. It is empty for a direct route.
	Name string
	// Scheme is the upstream proxy's scheme. It is empty for a direct route
	// and for one that fails closed before reaching any upstream proxy.
	Scheme UpstreamProxyScheme
	// Host and Port are the upstream proxy's address.
	Host string
	Port int

	// credentials is the URL's userinfo, or nil. No error or log prints it.
	credentials *url.Userinfo
	// ca is the entry's extra trusted CA as PEM, or empty.
	ca string
	// failure makes every dial fail closed with it: the selection is missing
	// from this machine's list, or its entry cannot be used.
	failure *UpstreamProxyError
	// timeout bounds reaching the upstream proxy and opening the tunnel.
	timeout time.Duration
}

// DirectRoute returns a route that connects to the origin directly.
func DirectRoute() *Route { return &Route{} }

// Direct reports whether the route connects to the origin directly.
func (route *Route) Direct() bool { return route.Name == "" }

// Address returns scheme://host:port of the upstream proxy without
// credentials, or "" when the route reaches no upstream proxy.
func (route *Route) Address() string {
	if route.Scheme == "" {
		return ""
	}
	return string(route.Scheme) + "://" + net.JoinHostPort(route.Host, strconv.Itoa(route.Port))
}

// sameRoute reports whether two routes reach the origin the same way.
func sameRoute(a, b *Route) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name && a.Scheme == b.Scheme && a.Host == b.Host && a.Port == b.Port &&
		a.credentials.String() == b.credentials.String() && a.ca == b.ca && a.timeout == b.timeout &&
		a.failure.sameAs(b.failure)
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
