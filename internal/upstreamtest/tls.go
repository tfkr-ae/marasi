package upstreamtest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// CA is a test certificate authority, standing in for the CA of an
// intercepting proxy such as Burp.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewCA makes a test CA.
func NewCA(t testing.TB, name string) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the CA certificate: %v", err)
	}
	return &CA{
		cert:   cert,
		key:    key,
		pem:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		leaves: map[string]*tls.Certificate{},
	}
}

// PEM returns the CA certificate as PEM.
func (ca *CA) PEM() string { return ca.pem }

// Leaf returns a certificate for host signed by the CA. host is an IP
// address or a DNS name.
func (ca *CA) Leaf(host string) (*tls.Certificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if leaf, ok := ca.leaves[host]; ok {
		return leaf, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ca.leaves[host] = leaf
	return leaf, nil
}

// ServerConfig presents a leaf signed by ca for whatever name the client asks
// for, or for 127.0.0.1 when it sends none.
func (ca *CA) ServerConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			host := hello.ServerName
			if host == "" {
				host = "127.0.0.1"
			}
			return ca.Leaf(host)
		},
	}
}

// NewHTTPS starts a fake HTTPS upstream proxy: the client speaks TLS to it,
// with a certificate signed by ca, and then CONNECT as to NewHTTP.
func NewHTTPS(t testing.TB, ca *CA, options ...Option) *Proxy {
	t.Helper()
	config := ca.ServerConfig()
	return start(t, func(proxy *Proxy, conn net.Conn) {
		proxy.serveHTTP(tls.Server(conn, config))
	}, options)
}

// NewIntercepting starts a fake intercepting HTTP upstream proxy, like Burp.
// It answers CONNECT, then terminates the client's TLS with a certificate
// for the target signed by ca, and carries the decrypted bytes to the
// target over a TLS connection it does not verify. Tunnel.Sent holds the
// decrypted bytes.
func NewIntercepting(t testing.TB, ca *CA, options ...Option) *Proxy {
	t.Helper()
	return start(t, (*Proxy).serveHTTP, append(options, func(proxy *Proxy) { proxy.intercept = ca }))
}

// interceptTunnel serves an established tunnel from conn to target as an
// intercepting proxy does.
func (proxy *Proxy) interceptTunnel(conn net.Conn, reader *bufio.Reader, target string, tunnel *Tunnel) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return
	}
	config := proxy.intercept.ServerConfig()
	config.NextProtos = []string{"http/1.1"}
	client := tls.Server(&prefixedConn{Conn: conn, reader: reader}, config)
	if err := client.Handshake(); err != nil {
		return
	}
	// The intercepting proxy, not the test, decides to trust the origin.
	origin, err := tls.Dial("tcp", target, &tls.Config{ServerName: host, InsecureSkipVerify: true})
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		client.Close()
		return
	}
	if !proxy.track(origin) {
		origin.Close()
		return
	}
	defer proxy.untrack(origin)
	Splice(client, client, origin, tunnel)
}

// prefixedConn reads bytes already buffered by reader before reading conn.
type prefixedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (conn *prefixedConn) Read(p []byte) (int, error) { return conn.reader.Read(p) }
