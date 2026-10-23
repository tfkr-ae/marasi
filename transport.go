package marasi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"

	tls "github.com/refraction-networking/utls"
	utls "github.com/refraction-networking/utls"
)

// marasiRoundTripper sends requests through the base RoundTripper without
// adding a User-Agent or Connection header. Marasi's own pages never reach it
// (see MarasiPagesModifier).
type marasiRoundTripper struct {
	base http.RoundTripper
	// transport is base when newRoutedTransport built it, and nil otherwise.
	transport *http.Transport
	// currentRoute returns the route new connections use. It is nil when
	// newRoutedTransport did not build the round tripper.
	currentRoute func() *Route
}

// newMarasiTransport builds marasi's round tripper with a route that is
// always direct. See newRoutedTransport.
func newMarasiTransport(dial func(context.Context, string, string) (net.Conn, error)) http.RoundTripper {
	direct := DirectRoute()
	always := func() *Route { return direct }
	return newRoutedTransport(always, always, dial)
}

// newRoutedTransport builds marasi's round tripper. Each new connection asks
// resolveRoute once how to reach the origin, dials through the route (a
// tunnel when it is an upstream proxy), and for TLS runs a utls handshake
// that mimics Chrome inside it. currentRoute reads the route without
// resolving it again. Waypoints need nothing here: their modifier has
// already rewritten the URL host to the destination, which is the tunnel's
// target.
// It is the only client that reaches the origin. Every other Marasi client
// sends through the proxy's own listener (see Proxy.Client).
func newRoutedTransport(resolveRoute, currentRoute func() *Route, dial func(context.Context, string, string) (net.Conn, error)) *marasiRoundTripper {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	// Compression stays off so a request without Accept-Encoding gains none
	// and the response reaches the client as the origin encoded it.
	transport := &http.Transport{DisableCompression: true}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		route := resolveRoute()
		conn, err := route.dial(ctx, dial, network, addr)
		if err != nil {
			return nil, err
		}
		return &routedConn{Conn: conn, route: route}, nil
	}
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		route := resolveRoute()
		tcpConn, err := route.dial(ctx, dial, network, addr)
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
			ServerName: sniHost,
			RootCAs:    roots,
		}

		if transport.TLSClientConfig != nil {
			uTlsConfig.InsecureSkipVerify = transport.TLSClientConfig.InsecureSkipVerify
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

		return &routedConn{Conn: uConn, route: route}, nil
	}

	return &marasiRoundTripper{base: transport, transport: transport, currentRoute: currentRoute}
}

// CloseIdleConnections closes the transport's idle connections to the origin.
func (m *marasiRoundTripper) CloseIdleConnections() {
	if m.transport != nil {
		m.transport.CloseIdleConnections()
	}
}

// retireStale keeps a connection opened on a route that has since been
// swapped out from being reused. The transport puts such a connection back
// in its idle pool when its request finishes, after the swap closed the idle
// connections, so they are closed again.
func (m *marasiRoundTripper) retireStale(used *Route) {
	if used != nil && m.currentRoute != nil && m.currentRoute() != used {
		m.CloseIdleConnections()
	}
}

// RoundTrip satisfies http.RoundTripper. It sends req through the base transport.
func (m *marasiRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "")
	}

	// The transport reads the request it sends until the response is done, while the
	// response pipeline replaces *res.Request in place. Send a copy so they never share
	// it, and hand back the original, whose address martian keys its context on.
	var used atomic.Pointer[Route]
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if route, ok := connectionRoute(info.Conn); ok {
			used.Store(route)
		}
	}}
	sent := *req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	// With Close set, the transport writes its own Connection: close unless the
	// first Connection value already says close. The copy keeps the client's
	// Connection lines as they are and adds none. The transport still closes the
	// connection when the first Connection value or the origin's answer says close.
	sent.Close = false
	res, err := m.base.RoundTrip(&sent)
	if res != nil {
		res.Request = req
		route := used.Load()
		// A response without a body is already back in the idle pool. One
		// with a body returns there once the body is read through. A 101
		// keeps its connection, and its body must stay that connection.
		m.retireStale(route)
		if res.Body != nil && res.Body != http.NoBody && res.StatusCode != http.StatusSwitchingProtocols {
			res.Body = &retiringBody{ReadCloser: res.Body, done: func() { m.retireStale(route) }}
		}
	}
	return res, err
}
