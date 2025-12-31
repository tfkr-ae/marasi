package marasi

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"

	tls "github.com/refraction-networking/utls"
	utls "github.com/refraction-networking/utls"
)

// marasiRoundTripper will intercept requests to marasi.cert and serve the CA certificate
// Other requests will use the base RoundTripper
type marasiRoundTripper struct {
	cert *x509.Certificate
	base http.RoundTripper
}

// newMarasiTransport will create marasi's roundtripper
// It will define the base transport with the upstream TLSConfig using utls to mimic Chrome,
// waypoint aware DialContext and marasiRoundTripper to serve the certificate
func newMarasiTransport(cert *x509.Certificate) http.RoundTripper {
	transport := &http.Transport{}
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		tcpConn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
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

	return &marasiRoundTripper{
		cert: cert,
		base: transport,
	}
}

// RoundTrip satisfies http.RoundTrip, it will take the request and check if the URL matches marasi.cert
// if it does, it will return the certificate in .der format
func (m *marasiRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	urls := []string{"http://marasi.cert/", "http://marasi.cert"}
	if slices.Contains(urls, req.URL.String()) {
		body := m.cert.Raw
		resp := &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Request:       req,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}
		resp.Header.Set("Content-Type", "application/x-x509-ca-cert")
		resp.Header.Set("Content-Disposition", "attachment; filename=\"marasi-cert.der\"")
		return resp, nil
	}

	return m.base.RoundTrip(req)
}
