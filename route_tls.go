package marasi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"

	utls "github.com/refraction-networking/utls"
)

// dialHTTPSConnect opens TLS to the upstream proxy, verified against the
// system roots plus the entry's CA, and asks it for a tunnel to address
// with CONNECT as dialHTTPConnect does.
func (route *Route) dialHTTPSConnect(ctx context.Context, dial dialFunc, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, route.timeoutOrDefault())
	defer cancel()
	roots, err := route.rootCAs()
	if err != nil {
		return nil, route.fail(UpstreamProxyInvalid, err.Error(), err)
	}
	conn, err := dial(ctx, network, net.JoinHostPort(route.Host, strconv.Itoa(route.Port)))
	if err != nil {
		return nil, route.classify("connecting", err)
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: route.Host, RootCAs: roots, NextProtos: []string{"http/1.1"}})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, route.handshakeFailure("the upstream proxy", contextCause(ctx, err))
	}
	tunnel, err := route.connect(ctx, tlsConn, address)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}
	return tunnel, nil
}

// originHandshakeFailure turns a failed TLS handshake with the origin into
// the route's error, so a proxied connection fails closed with a proxy log.
// A direct route returns err as it is.
func (route *Route) originHandshakeFailure(err error) error {
	if route.Direct() {
		return err
	}
	return route.handshakeFailure("the origin", err)
}

// handshakeFailure classifies a failed TLS handshake with peer, the
// upstream proxy or the origin.
func (route *Route) handshakeFailure(peer string, err error) *UpstreamProxyError {
	if isCertificateVerificationError(err) {
		return route.fail(UpstreamProxyTLSVerification, "TLS verification of "+peer+"'s certificate failed: "+err.Error(), err)
	}
	return route.classify("TLS handshake with "+peer, err)
}

// isCertificateVerificationError reports whether err says a certificate did
// not verify, from crypto/tls or utls.
func isCertificateVerificationError(err error) bool {
	var stdErr *tls.CertificateVerificationError
	var uErr *utls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &stdErr) || errors.As(err, &uErr) || errors.As(err, &authority) ||
		errors.As(err, &hostname) || errors.As(err, &invalid)
}
