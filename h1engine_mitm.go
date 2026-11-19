package marasi

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"time"
)

// MITM on Marasi's own engine: the client leg's TLS, inside a CONNECT or sent
// straight to the listener. The handshake is origin first
// (docs/adr/2026-11-19-marasi-gives-the-client-the-origins-protocol.md): when
// the client's ClientHello arrives, the engine dials the origin through the
// route and any waypoint, offers it the client's ALPN list in the client's
// order, and completes the client's handshake with the origin's choice. The
// origin connection it dialled is the client connection's origin connection.

// setMITM makes the engine intercept TLS, issuing leaf certificates from the
// CA.
func (engine *h1Engine) setMITM(certificate *x509.Certificate, privateKey any) error {
	issuer, err := newLeafIssuer(certificate, privateKey)
	if err != nil {
		return err
	}
	engine.leaves = issuer
	return nil
}

// tlsHandshakeTimeout bounds the client's handshake, and apart from it the
// origin's dial during it.
const tlsHandshakeTimeout = 10 * time.Second

// errNoOriginName is the cause when TLS sent straight to the listener names
// no host by SNI, so there is no origin to dial.
var errNoOriginName = errors.New("the client sent no SNI, so there is no origin to dial")

// handshakeDial is the origin dial an intercepted handshake made: the
// connection, or why there is none, and the address it dialled.
type handshakeDial struct {
	// target is the origin the handshake named, and addr where it was
	// dialled: its waypoint's destination when it has one.
	target string
	addr   string
	conn   *routedConn
	err    error
	// route is the route the dial ran on.
	route *Route
}

// intercept runs the TLS handshake a client started, as the server, with a
// leaf for the client's SNI or else connectHost: inside the tunnel to
// connectHost, or sent straight to the listener when connectHost is "". The
// connection then carries the client's requests over TLS, which prepare
// marks https, and protocol is what was negotiated. It reports whether the
// handshake succeeded.
func (c *h1Conn) intercept(connectHost string) bool {
	if c.engine.leaves == nil {
		return false
	}
	var dialled *handshakeDial
	config := c.engine.leaves.config(connectHost)
	base := config.Clone()
	config.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		dialled = c.dialHandshakeOrigin(hello, connectHost)
		answer := base.Clone()
		answer.NextProtos = clientProtocol(dialled, hello.SupportedProtos)
		return answer, nil
	}
	tlsConn := tls.Server(&bufferedConn{Conn: c.conn, reader: c.br}, config)
	// The origin's dial runs inside the handshake, under a bound of its own.
	tlsConn.SetDeadline(time.Now().Add(2 * tlsHandshakeTimeout))
	if err := tlsConn.Handshake(); err != nil {
		if dialled != nil && dialled.conn != nil {
			dialled.conn.Close()
		}
		return false
	}
	tlsConn.SetDeadline(time.Time{})
	// The TLS records are read through the old reader, which may already
	// hold some of them; requests are read from the decrypted stream.
	c.conn = tlsConn
	c.br = bufio.NewReader(tlsConn)
	c.bw.Reset(tlsConn)
	c.protocol = tlsConn.ConnectionState().NegotiatedProtocol
	c.handshake = dialled
	if dialled != nil && dialled.conn != nil && c.protocol != "h2" {
		c.origin = &originConn{
			addr: dialled.addr, scheme: "https", route: dialled.conn.route, conn: dialled.conn,
			br: bufio.NewReader(dialled.conn), bw: bufio.NewWriter(dialled.conn),
		}
	}
	return true
}

// dialHandshakeOrigin dials the origin of an intercepted handshake on the
// current route: the CONNECT authority, or the SNI on port 443 for TLS sent
// straight to the listener, either one sent to its waypoint when it has
// one. It offers the origin the client's ALPN list.
func (c *h1Conn) dialHandshakeOrigin(hello *tls.ClientHelloInfo, connectHost string) *handshakeDial {
	proxy := c.engine.proxy
	addr := connectHost
	if addr == "" {
		if hello.ServerName == "" {
			return &handshakeDial{err: errNoOriginName, route: proxy.CurrentRoute()}
		}
		addr = net.JoinHostPort(hello.ServerName, "443")
	}
	target := addr
	if override, ok := proxy.waypointOverride(addr); ok {
		addr = override
	}
	ctx, cancel := context.WithTimeout(withOriginALPN(c.ctx, hello.SupportedProtos), tlsHandshakeTimeout)
	defer cancel()
	conn, err := proxy.dialOrigin(ctx, "https", addr)
	if err != nil {
		return &handshakeDial{target: target, addr: addr, err: err, route: proxy.CurrentRoute()}
	}
	return &handshakeDial{target: target, addr: addr, conn: conn, route: conn.route}
}

// clientProtocol is what the client's handshake answers by ALPN: the origin's
// choice, nothing when the origin chose nothing, and when the origin could
// not be reached http/1.1 if the client offered it, else h2.
func clientProtocol(dialled *handshakeDial, offered []string) []string {
	if dialled.conn != nil {
		if protocol := dialled.conn.protocol(); protocol != "" {
			return []string{protocol}
		}
		return nil
	}
	for _, protocol := range []string{"http/1.1", "h2"} {
		if slices.Contains(offered, protocol) {
			return []string{protocol}
		}
	}
	return nil
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
