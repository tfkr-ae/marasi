package marasi

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// leafIssuer issues the leaf certificates Marasi shows clients it
// intercepts, signed by the proxy's CA. A leaf is made from the host alone;
// it never copies the origin's certificate. Each host's leaf is kept in
// memory until it expires.
type leafIssuer struct {
	ca    *x509.Certificate
	caKey crypto.Signer

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// newLeafIssuer makes an issuer for the CA certificate and its private key,
// as loaded from the config directory.
func newLeafIssuer(ca *x509.Certificate, caKey any) (*leafIssuer, error) {
	signer, ok := caKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("the CA key is a %T, which cannot sign", caKey)
	}
	return &leafIssuer{ca: ca, caKey: signer, leaves: make(map[string]*tls.Certificate)}, nil
}

// errNoServerName fails a handshake that names no host: no SNI, and no
// CONNECT host to fall back on.
var errNoServerName = errors.New("the client sent no SNI and there is no CONNECT host to name the certificate after")

// config returns the TLS config served to an intercepted client. Its leaf
// names the client's SNI, or connectHost (a CONNECT authority, port allowed)
// when the client sends none. connectHost is "" for TLS sent straight to the
// listener. It offers no protocol by ALPN: the engine answers with the
// origin's choice (h1Conn.intercept).
func (issuer *leafIssuer) config(connectHost string) *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			host := hello.ServerName
			if host == "" {
				host = connectHost
			}
			return issuer.leaf(host)
		},
	}
}

// leaf returns host's leaf, issuing it when the cache has none or the one
// it has expired.
func (issuer *leafIssuer) leaf(host string) (*tls.Certificate, error) {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if host == "" {
		return nil, errNoServerName
	}
	now := time.Now()
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	if leaf, ok := issuer.leaves[host]; ok && now.Before(leaf.Leaf.NotAfter) {
		return leaf, nil
	}
	leaf, err := issuer.issue(host, now)
	if err != nil {
		return nil, fmt.Errorf("issuing a leaf certificate for %s: %w", host, err)
	}
	issuer.leaves[host] = leaf
	return leaf, nil
}

// A leaf's serial is random below 2^leafSerialBits, more than the 64 bits of
// entropy the CA/Browser Forum baseline requirements ask for. A leaf is valid
// for leafValidityYears, from leafBackdate before it was issued, so a client
// whose clock runs behind still accepts it.
const (
	leafSerialBits    = 128
	leafValidityYears = 1
	leafBackdate      = 24 * time.Hour
)

// issue makes host's leaf: organisation Marasi, an ECDSA P-256 key, valid
// from a day before now until a year after, or until the CA expires if that
// is sooner. An IP host is named in an IP SAN, any other in a DNS SAN.
func (issuer *leafIssuer) issue(host string, now time.Time) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), leafSerialBits))
	if err != nil {
		return nil, err
	}
	notAfter := now.AddDate(leafValidityYears, 0, 0)
	if issuer.ca.NotAfter.Before(notAfter) {
		notAfter = issuer.ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"Marasi"}},
		NotBefore:             now.Add(-leafBackdate),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, issuer.ca, key.Public(), issuer.caKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{raw, issuer.ca.Raw}, PrivateKey: key, Leaf: leaf}, nil
}
