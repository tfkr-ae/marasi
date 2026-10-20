package marasi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	tls "github.com/refraction-networking/utls"
	utls "github.com/refraction-networking/utls"
)

// marasiRoundTripper sends requests through the base RoundTripper without
// adding a User-Agent or Connection header. Marasi's own pages never reach it
// (see MarasiPagesModifier).
type marasiRoundTripper struct {
	base http.RoundTripper
}

// newMarasiTransport will create marasi's roundtripper
// It will define the base transport with the upstream TLSConfig using utls to mimic Chrome,
// and waypoint aware DialContext
func newMarasiTransport(dial func(context.Context, string, string) (net.Conn, error)) http.RoundTripper {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	// Compression stays off so a request without Accept-Encoding gains none
	// and the response reaches the client as the origin encoded it.
	transport := &http.Transport{DisableCompression: true}
	transport.DialContext = dial
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		tcpConn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		sniHost, _, err := net.SplitHostPort(addr)
		if err != nil {
			sniHost = addr
		}

		uTlsConfig := &utls.Config{
			ServerName: sniHost,
		}

		if transport.TLSClientConfig != nil {
			uTlsConfig.InsecureSkipVerify = transport.TLSClientConfig.InsecureSkipVerify
		}

		uConn := utls.UClient(tcpConn, uTlsConfig, utls.HelloChrome_Auto)

		if err := uConn.BuildHandshakeState(); err != nil {
			return nil, fmt.Errorf("buildling handshake state : %w", err)
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
			return nil, errors.New("could not find ALPNExtension")
		}

		if err := uConn.HandshakeContext(ctx); err != nil {
			tcpConn.Close()
			return nil, err
		}

		return uConn, nil
	}

	return &marasiRoundTripper{base: transport}
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
