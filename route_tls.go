package marasi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	utls "github.com/refraction-networking/utls"
)

// connectOverTLS runs TLS with the upstream proxy on conn, verified against
// the system roots plus the entry's CA, and inside it asks for a tunnel to
// address with CONNECT as connect does.
func (route *Route) connectOverTLS(ctx context.Context, conn net.Conn, address string) (net.Conn, error) {
	roots, err := route.rootCAs()
	if err != nil {
		return nil, route.fail(UpstreamProxyInvalid, err.Error(), err)
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: route.Host, RootCAs: roots, NextProtos: []string{"http/1.1"}})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, route.handshakeFailure("the upstream proxy", contextCause(ctx, err))
	}
	return route.connect(ctx, tlsConn, address)
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
	return route.classify("running the TLS handshake with "+peer, err)
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
