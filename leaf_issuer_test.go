package marasi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// newTestLeafIssuer makes a CA valid for validity either side of now and a
// leaf issuer for it.
func newTestLeafIssuer(t *testing.T, validity time.Duration) (*leafIssuer, *x509.Certificate) {
	t.Helper()
	ca, key, err := newCertificateAuthority("Marasi", "Marasi Authority", validity)
	if err != nil {
		t.Fatalf("creating the CA: %v", err)
	}
	issuer, err := newLeafIssuer(ca, key)
	if err != nil {
		t.Fatalf("creating the leaf issuer: %v", err)
	}
	return issuer, ca
}

// issue asks config for the leaf a client saying serverName in its hello
// gets.
func issue(t *testing.T, config *tls.Config, serverName string) *x509.Certificate {
	t.Helper()
	certificate, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: serverName})
	if err != nil {
		t.Fatalf("issuing a leaf for %q: %v", serverName, err)
	}
	return certificate.Leaf
}

// verifyLeaf checks that leaf chains to ca for name.
func verifyLeaf(t *testing.T, leaf, ca *x509.Certificate, name string) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots}); err != nil {
		t.Fatalf("wanted the leaf to verify for %s against the CA\ngot: %v", name, err)
	}
}

func TestLeafIssuer(t *testing.T) {
	t.Run("should issue a leaf naming Marasi for the SNI host with an ECDSA P-256 key", func(t *testing.T) {
		issuer, ca := newTestLeafIssuer(t, 3*365*24*time.Hour)
		before := time.Now().Truncate(time.Second)
		leaf := issue(t, issuer.config(""), "Example.COM")
		after := time.Now().Add(time.Second)

		if !slices.Equal(leaf.Subject.Organization, []string{"Marasi"}) {
			t.Errorf("organisation\nwanted:\n[Marasi]\ngot:\n%v", leaf.Subject.Organization)
		}
		if leaf.Subject.CommonName != "example.com" || !slices.Equal(leaf.DNSNames, []string{"example.com"}) || len(leaf.IPAddresses) != 0 {
			t.Errorf("names\nwanted:\nCN example.com, SAN [example.com], no IP SAN\ngot:\nCN %s, SAN %v, IP SAN %v", leaf.Subject.CommonName, leaf.DNSNames, leaf.IPAddresses)
		}
		key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P256() {
			t.Errorf("key\nwanted:\nECDSA P-256\ngot:\n%T", leaf.PublicKey)
		}
		if leaf.NotBefore.Before(before.Add(-24*time.Hour)) || leaf.NotBefore.After(after.Add(-24*time.Hour)) {
			t.Errorf("not before\nwanted:\nabout %v\ngot:\n%v", before.Add(-24*time.Hour), leaf.NotBefore)
		}
		if year := before.AddDate(1, 0, 0); leaf.NotAfter.Before(year) || leaf.NotAfter.After(after.AddDate(1, 0, 0)) {
			t.Errorf("not after\nwanted:\nabout %v\ngot:\n%v", year, leaf.NotAfter)
		}
		if leaf.IsCA || !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
			t.Errorf("usage\nwanted:\nnot a CA, server auth\ngot:\nCA %v, %v", leaf.IsCA, leaf.ExtKeyUsage)
		}
		verifyLeaf(t, leaf, ca, "example.com")
	})

	t.Run("should end the leaf when the CA ends if that is sooner than a year", func(t *testing.T) {
		issuer, ca := newTestLeafIssuer(t, 48*time.Hour)
		leaf := issue(t, issuer.config(""), "example.com")
		if !leaf.NotAfter.Equal(ca.NotAfter) {
			t.Fatalf("not after\nwanted:\n%v\ngot:\n%v", ca.NotAfter, leaf.NotAfter)
		}
		verifyLeaf(t, leaf, ca, "example.com")
	})

	t.Run("should name the CONNECT host when the client sends no SNI", func(t *testing.T) {
		issuer, ca := newTestLeafIssuer(t, 3*365*24*time.Hour)
		leaf := issue(t, issuer.config("tunnel.example:8443"), "")
		if leaf.Subject.CommonName != "tunnel.example" || !slices.Equal(leaf.DNSNames, []string{"tunnel.example"}) {
			t.Fatalf("names\nwanted:\nCN and SAN tunnel.example\ngot:\nCN %s, SAN %v", leaf.Subject.CommonName, leaf.DNSNames)
		}
		verifyLeaf(t, leaf, ca, "tunnel.example")
	})

	t.Run("should prefer SNI over the CONNECT host", func(t *testing.T) {
		issuer, _ := newTestLeafIssuer(t, 3*365*24*time.Hour)
		leaf := issue(t, issuer.config("tunnel.example:443"), "sni.example")
		if leaf.Subject.CommonName != "sni.example" {
			t.Fatalf("common name\nwanted:\nsni.example\ngot:\n%s", leaf.Subject.CommonName)
		}
	})

	t.Run("should give an IP host an IP SAN", func(t *testing.T) {
		issuer, ca := newTestLeafIssuer(t, 3*365*24*time.Hour)
		for _, host := range []string{"127.0.0.1:443", "[::1]:443"} {
			leaf := issue(t, issuer.config(host), "")
			ip, _, _ := net.SplitHostPort(host)
			if leaf.Subject.CommonName != ip || len(leaf.DNSNames) != 0 || len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP(ip)) {
				t.Fatalf("names for %s\nwanted:\nCN %s, IP SAN %s, no DNS SAN\ngot:\nCN %s, IP SAN %v, DNS SAN %v", host, ip, ip, leaf.Subject.CommonName, leaf.IPAddresses, leaf.DNSNames)
			}
			verifyLeaf(t, leaf, ca, ip)
		}
	})

	t.Run("should fail the handshake when there is neither SNI nor a CONNECT host", func(t *testing.T) {
		issuer, _ := newTestLeafIssuer(t, 3*365*24*time.Hour)
		if _, err := issuer.config("").GetCertificate(&tls.ClientHelloInfo{}); err == nil {
			t.Fatal("wanted an error, got a leaf")
		}
	})

	t.Run("should keep one leaf per host in memory", func(t *testing.T) {
		issuer, _ := newTestLeafIssuer(t, 3*365*24*time.Hour)
		first, err := issuer.config("").GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example"})
		if err != nil {
			t.Fatal(err)
		}
		again, err := issuer.config("A.example:443").GetCertificate(&tls.ClientHelloInfo{})
		if err != nil {
			t.Fatal(err)
		}
		other, err := issuer.config("").GetCertificate(&tls.ClientHelloInfo{ServerName: "b.example"})
		if err != nil {
			t.Fatal(err)
		}
		if first != again {
			t.Error("wanted the same leaf for a.example twice")
		}
		if first == other {
			t.Error("wanted another leaf for b.example")
		}
	})

	t.Run("should offer http/1.1 only by ALPN", func(t *testing.T) {
		issuer, _ := newTestLeafIssuer(t, 3*365*24*time.Hour)
		for _, config := range []*tls.Config{issuer.config(""), issuer.config("example.com:443")} {
			if !slices.Equal(config.NextProtos, []string{"http/1.1"}) {
				t.Fatalf("ALPN\nwanted:\n[http/1.1]\ngot:\n%v", config.NextProtos)
			}
		}
	})

	t.Run("should send the CA after the leaf", func(t *testing.T) {
		issuer, ca := newTestLeafIssuer(t, 3*365*24*time.Hour)
		certificate, err := issuer.config("").GetCertificate(&tls.ClientHelloInfo{ServerName: "example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if len(certificate.Certificate) != 2 || !bytes.Equal(certificate.Certificate[1], ca.Raw) {
			t.Fatalf("wanted the chain [leaf, CA], got %d certificates", len(certificate.Certificate))
		}
	})
}

func TestLeafIssuer_LoadsTodaysCA(t *testing.T) {
	t.Run("should sign leaves with a CA from the config directory without making a new one", func(t *testing.T) {
		dir := t.TempDir()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		made, _, err := initializeCertificateAuthority(context.Background(), dir, logger)
		if err != nil {
			t.Fatalf("creating the CA: %v", err)
		}
		files := func() []byte {
			cert, err := os.ReadFile(filepath.Join(dir, certFile))
			if err != nil {
				t.Fatal(err)
			}
			key, err := os.ReadFile(filepath.Join(dir, keyFile))
			if err != nil {
				t.Fatal(err)
			}
			return append(cert, key...)
		}
		saved := files()

		loaded, key, err := initializeCertificateAuthority(context.Background(), dir, logger)
		if err != nil {
			t.Fatalf("loading the CA: %v", err)
		}
		if !bytes.Equal(files(), saved) || !bytes.Equal(loaded.Raw, made.Raw) {
			t.Fatal("wanted the saved CA loaded, not a new one")
		}
		issuer, err := newLeafIssuer(loaded, key)
		if err != nil {
			t.Fatalf("creating the leaf issuer: %v", err)
		}
		verifyLeaf(t, issue(t, issuer.config(""), "example.com"), made, "example.com")
	})
}
