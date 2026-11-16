package conformance_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// serveTLS answers one connection with config and writes "ok".
func serveTLS(t *testing.T, config *tls.Config) string {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "ok")
	}()
	return listener.Addr().String()
}

func dialTLS(address string, config *tls.Config) (string, error) {
	var dialer tls.Dialer
	dialer.Config = config
	conn, err := dialer.DialContext(context.Background(), "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	data, err := io.ReadAll(conn)
	return string(data), err
}

func TestCA(t *testing.T) {
	ca, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("should issue a server certificate a client trusting the CA verifies for the name", func(t *testing.T) {
		server, err := ca.ServerTLS("origin.test")
		if err != nil {
			t.Fatal(err)
		}
		address := serveTLS(t, server)

		got, err := dialTLS(address, ca.ClientTLS("origin.test"))
		if err != nil || got != "ok" {
			t.Fatalf("\nwanted ok\ngot: %q, %v", got, err)
		}
	})

	t.Run("should not be trusted by a client trusting another CA", func(t *testing.T) {
		other, err := conformance.NewCA()
		if err != nil {
			t.Fatal(err)
		}
		server, err := ca.ServerTLS("origin.test")
		if err != nil {
			t.Fatal(err)
		}
		address := serveTLS(t, server)

		var unknown x509.UnknownAuthorityError
		if _, err := dialTLS(address, other.ClientTLS("origin.test")); !errors.As(err, &unknown) {
			t.Fatalf("wanted an unknown-authority error\ngot: %v", err)
		}
	})

	t.Run("should not verify for a name it was not issued for", func(t *testing.T) {
		server, err := ca.ServerTLS("origin.test")
		if err != nil {
			t.Fatal(err)
		}
		address := serveTLS(t, server)

		var hostname x509.HostnameError
		if _, err := dialTLS(address, ca.ClientTLS("other.test")); !errors.As(err, &hostname) {
			t.Fatalf("wanted a hostname error\ngot: %v", err)
		}
	})

	t.Run("should give its certificate and an issued key pair as PEM, for tools outside the process", func(t *testing.T) {
		certPEM, keyPEM, err := ca.Issue("origin.test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca.PEM()) {
			t.Fatalf("CA PEM did not parse:\n%s", ca.PEM())
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"origin.test", "127.0.0.1"} {
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
				t.Fatalf("verifying for %s: %v", name, err)
			}
		}
		if block, _ := pem.Decode(keyPEM); block == nil || block.Type != "PRIVATE KEY" {
			t.Fatalf("wanted a PKCS #8 PRIVATE KEY block\ngot:\n%s", keyPEM)
		}
	})
}
