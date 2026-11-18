package marasi

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// MITM on Marasi's own engine: the client leg's TLS inside a CONNECT. The
// origin leg's TLS for an https request is proxy.dialOrigin's
// (h1engine_route.go), through the routed transport's utls handshake. TLS sent straight to the
// listener is completed by the protocol mux (listener package) with the
// config setMITM returns, before the engine sees the connection.

// setMITM makes the engine intercept TLS, issuing leaf certificates from the
// CA. The config it returns is the one TLS sent straight to the listener
// gets; a CONNECT gets one that falls back on its host (intercept).
func (engine *h1Engine) setMITM(certificate *x509.Certificate, privateKey any) (*tls.Config, error) {
	issuer, err := newLeafIssuer(certificate, privateKey)
	if err != nil {
		return nil, err
	}
	engine.leaves = issuer
	return issuer.config(""), nil
}

// setRoundTripper takes the routed transport under base, if any, as the one
// the engine dials origins through (proxy.dialOrigin): the engine carries
// requests itself. Serve stores the same transport; tests that serve the
// engine directly set one that trusts their self-signed origins.
func (engine *h1Engine) setRoundTripper(base http.RoundTripper) {
	for {
		switch rt := base.(type) {
		case *launchFailureRoundTripper:
			base = rt.base
		case *marasiRoundTripper:
			base = rt.base
		case *routedTransport:
			engine.proxy.transport.Store(rt)
			return
		default:
			return
		}
	}
}

// tlsHandshakeTimeout bounds the client's handshake inside a tunnel, as the
// protocol mux bounds one sent straight to the listener.
const tlsHandshakeTimeout = 10 * time.Second

// intercept runs the TLS handshake a client started inside the tunnel to
// connectHost, as the server, with a leaf for the client's SNI or else
// connectHost. The connection then carries the client's requests over TLS,
// which prepare marks https. It reports whether the handshake succeeded.
func (c *h1Conn) intercept(connectHost string) bool {
	if c.engine.leaves == nil {
		return false
	}
	tlsConn := tls.Server(&bufferedConn{Conn: c.conn, reader: c.br}, c.engine.leaves.config(connectHost))
	tlsConn.SetDeadline(time.Now().Add(tlsHandshakeTimeout))
	if err := tlsConn.Handshake(); err != nil {
		return false
	}
	tlsConn.SetDeadline(time.Time{})
	// The TLS records are read through the old reader, which may already
	// hold some of them; requests are read from the decrypted stream.
	c.conn = tlsConn
	c.br = bufio.NewReader(tlsConn)
	c.bw.Reset(tlsConn)
	return true
}

// carried reports whether the engine carries a request with scheme, and the
// port it defaults to.
func carried(scheme string) (port string, ok bool) {
	switch scheme {
	case "http":
		return "80", true
	case "https":
		return "443", true
	}
	return "", false
}
