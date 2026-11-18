package marasi

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"slices"
	"testing"
	"time"
)

// testdata/martian_ca_cert.pem is a CA certificate that Martian's
// mitm.NewAuthority made, which is how Marasi made its CA before the CA moved
// to the standard library. Only the certificate is kept: its key is not needed
// to compare the two.
func readMartianCA(t *testing.T) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile("testdata/martian_ca_cert.pem")
	if err != nil {
		t.Fatalf("reading Martian CA: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("decoding Martian CA: no PEM block")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing Martian CA: %v", err)
	}
	return certificate
}

func TestNewCertificateAuthority(t *testing.T) {
	t.Run("should make the CA Martian made", func(t *testing.T) {
		want := readMartianCA(t)
		got, key, err := newCertificateAuthority("Marasi", "Marasi Authority", time.Hour)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.Subject.String() != want.Subject.String() {
			t.Errorf("subject\nwanted:\n%s\ngot:\n%s", want.Subject, got.Subject)
		}
		if got.Issuer.String() != got.Subject.String() {
			t.Errorf("issuer\nwanted:\n%s\ngot:\n%s", got.Subject, got.Issuer)
		}
		if got.KeyUsage != want.KeyUsage {
			t.Errorf("key usage\nwanted:\n%v\ngot:\n%v", want.KeyUsage, got.KeyUsage)
		}
		if !slices.Equal(got.ExtKeyUsage, want.ExtKeyUsage) {
			t.Errorf("extended key usage\nwanted:\n%v\ngot:\n%v", want.ExtKeyUsage, got.ExtKeyUsage)
		}
		if got.IsCA != want.IsCA || got.BasicConstraintsValid != want.BasicConstraintsValid ||
			got.MaxPathLen != want.MaxPathLen || got.MaxPathLenZero != want.MaxPathLenZero {
			t.Errorf("basic constraints\nwanted:\n%v %v %d %v\ngot:\n%v %v %d %v",
				want.IsCA, want.BasicConstraintsValid, want.MaxPathLen, want.MaxPathLenZero,
				got.IsCA, got.BasicConstraintsValid, got.MaxPathLen, got.MaxPathLenZero)
		}
		if !slices.Equal(got.DNSNames, want.DNSNames) {
			t.Errorf("DNS names\nwanted:\n%v\ngot:\n%v", want.DNSNames, got.DNSNames)
		}
		if got.SignatureAlgorithm != want.SignatureAlgorithm || got.PublicKeyAlgorithm != want.PublicKeyAlgorithm {
			t.Errorf("algorithms\nwanted:\n%v %v\ngot:\n%v %v",
				want.SignatureAlgorithm, want.PublicKeyAlgorithm, got.SignatureAlgorithm, got.PublicKeyAlgorithm)
		}
		if size, wantSize := got.PublicKey.(*rsa.PublicKey).N.BitLen(), want.PublicKey.(*rsa.PublicKey).N.BitLen(); size != wantSize {
			t.Errorf("key size\nwanted:\n%d\ngot:\n%d", wantSize, size)
		}
		if !got.PublicKey.(*rsa.PublicKey).Equal(key.Public()) {
			t.Error("the certificate is not for the returned key")
		}
		publicKey, err := x509.MarshalPKIXPublicKey(key.Public())
		if err != nil {
			t.Fatalf("marshaling public key: %v", err)
		}
		if keyID := sha1.Sum(publicKey); !bytes.Equal(got.SubjectKeyId, keyID[:]) {
			t.Errorf("subject key ID\nwanted:\n%x\ngot:\n%x", keyID, got.SubjectKeyId)
		}
		if err := got.CheckSignatureFrom(got); err != nil {
			t.Errorf("self signature: %v", err)
		}
	})

	t.Run("should be valid from validity ago until validity from now", func(t *testing.T) {
		before := time.Now().Truncate(time.Second)
		got, _, err := newCertificateAuthority("Marasi", "Marasi Authority", time.Hour)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		after := time.Now().Add(time.Second)
		if got.NotBefore.Before(before.Add(-time.Hour)) || got.NotBefore.After(after.Add(-time.Hour)) {
			t.Errorf("not before\nwanted:\nabout %v\ngot:\n%v", before.Add(-time.Hour), got.NotBefore)
		}
		if got.NotAfter.Before(before.Add(time.Hour)) || got.NotAfter.After(after.Add(time.Hour)) {
			t.Errorf("not after\nwanted:\nabout %v\ngot:\n%v", before.Add(time.Hour), got.NotAfter)
		}
	})

	t.Run("should issue leaf certificates through the MITM engine", func(t *testing.T) {
		ca, key, err := newCertificateAuthority("Marasi", "Marasi Authority", time.Hour)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		serverConfig, err := newH1Engine(nil).setMITM(ca, key)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		roots := x509.NewCertPool()
		roots.AddCert(ca)

		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()
		defer clientConn.Close()
		serverErr := make(chan error, 1)
		go func() { serverErr <- tls.Server(serverConn, serverConfig).Handshake() }()
		client := tls.Client(clientConn, &tls.Config{ServerName: "example.test", RootCAs: roots})
		if err := client.Handshake(); err != nil {
			t.Fatalf("client handshake\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := <-serverErr; err != nil {
			t.Fatalf("server handshake\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}
