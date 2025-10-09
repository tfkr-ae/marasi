package listener

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// generateTestTLSConfig creates a self-signed TLS configuration for testing purposes.
// It returns a server-side tls.Config and a client-side x509.CertPool that trusts the server's cert.
func generateTestTLSConfig(t *testing.T) (serverTLSConfig *tls.Config, clientTLSConfig *tls.Config) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("failed to generate serial number: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Test Co"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	keyDer, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("failed to marshal private key: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDer})

	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("failed to load key pair: %v", err)
	}

	// Create a cert pool for the client, containing our self-signed cert
	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(certPEM) {
		t.Fatalf("failed to add server certificate to client cert pool")
	}

	serverTLSConfig = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}
	clientTLSConfig = &tls.Config{
		RootCAs: clientCertPool,
	}

	return serverTLSConfig, clientTLSConfig
}

// TestConnWrapper_ReadDelegates tests that the connWrapper delegates the read to the underlying conn
func TestConnWrapper_ReadDelegates(t *testing.T) {
	read, write := net.Pipe()
	defer read.Close()
	defer write.Close()

	want := []byte("hello, marasi")
	go func() {
		defer write.Close()
		_, _ = write.Write(want)
	}()

	cW := &connWrapper{
		Conn:   read,
		Reader: bufio.NewReader(read),
	}

	got := make([]byte, len(want))
	numBytes, err := cW.Read(got)
	if err != nil {
		t.Fatalf("read error : %v", err)
	}

	got = got[:numBytes]

	if !bytes.Equal(got, want[:numBytes]) {
		t.Fatalf("mismatch: want %q got %q", want, got)
	}
}

func TestProtocolMuxListener(t *testing.T) {
	testServerTLSConfig, testClientTLSConfig := generateTestTLSConfig(t)
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener : %v", err)
	}
	defer baseListener.Close()

	muxListener := NewProtocolMuxListener(baseListener, testServerTLSConfig)

	// Echo server will reply with whatever is sent
	// errChannel will return any errors for the tests to check
	runServerEcho := func() chan error {
		errChannel := make(chan error, 1)
		go func() {
			conn, err := muxListener.Accept()
			if err != nil {
				errChannel <- fmt.Errorf("accept failed : %w", err)
				return
			}
			defer conn.Close()
			buffer := make([]byte, 1024)
			n, err := conn.Read(buffer)
			if err != nil && err != io.EOF {
				errChannel <- fmt.Errorf("server read failed: %w", err)
				return
			}

			if _, err := conn.Write(buffer[:n]); err != nil {
				errChannel <- fmt.Errorf("server write failed: %w", err)
				return
			}
			close(errChannel)
		}()
		return errChannel
	}

	t.Run("Accept Plain TCP Connections", func(t *testing.T) {
		serverErrChannel := runServerEcho()
		clientConn, err := net.Dial("tcp", baseListener.Addr().String())
		if err != nil {
			t.Fatalf("client fialed to dial: %v", err)
		}
		defer clientConn.Close()

		want := []byte("plain marasi")
		_, err = clientConn.Write(want)
		if err != nil {
			t.Fatalf("client write failed : %v", err)
		}

		got := make([]byte, len(want))
		_, err = io.ReadFull(clientConn, got)
		if err != nil {
			t.Fatalf("client read failed: %v", err)
		}

		if !bytes.Equal(got, want) {
			t.Errorf("expected %q, got %q", want, got)
		}

		err = <-serverErrChannel
		if err != nil {
			t.Fatalf("server side error: %v", err)
		}
	})

	t.Run("Accept TLS Connections", func(t *testing.T) {
		serverErrChannel := runServerEcho()
		clientConn, err := tls.Dial("tcp", baseListener.Addr().String(), testClientTLSConfig)
		if err != nil {
			t.Fatalf("client fialed to dial: %v", err)
		}
		defer clientConn.Close()

		want := []byte("tls marasi")
		_, err = clientConn.Write(want)
		if err != nil {
			t.Fatalf("client write failed : %v", err)
		}

		got := make([]byte, len(want))
		_, err = io.ReadFull(clientConn, got)
		if err != nil {
			t.Fatalf("client read failed: %v", err)
		}

		if !bytes.Equal(got, want) {
			t.Errorf("expected %q, got %q", want, got)
		}

		err = <-serverErrChannel
		if err != nil {
			t.Fatalf("server side error: %v", err)
		}
	})

	t.Run("Timeout on Initial Read", func(t *testing.T) {
		serverErrChannel := runServerEcho()
		clientConn, err := net.Dial("tcp", baseListener.Addr().String())
		if err != nil {
			t.Fatalf("client fialed to dial: %v", err)
		}
		defer clientConn.Close()
		err = <-serverErrChannel
		if err == nil {
			t.Fatal("expected a timeout error but got nil")
		}

		if !strings.Contains(err.Error(), "peaking initial bytes") {
			t.Errorf("expected error to contain 'peaking initial bytes', but got: %v", err)
		}
		if !strings.Contains(err.Error(), "i/o timeout") {
			t.Errorf("expected error to contain 'i/o timeout', but got: %v", err)
		}
	})

	t.Run("TLS Handshake Failure", func(t *testing.T) {
		serverErrChannel := runServerEcho()

		// Empty RootCAs
		badClientConfig := &tls.Config{
			RootCAs:    x509.NewCertPool(),
			ServerName: "localhost",
		}

		_, err := tls.Dial("tcp", baseListener.Addr().String(), badClientConfig)
		if err == nil {
			t.Fatal("expected client dial to fail due to handshake error, but it succeeded")
		}

		serverErr := <-serverErrChannel
		if serverErr == nil {
			t.Fatal("expected the server to return an error from a failed handshake, but got nil")
		}

		if !strings.Contains(serverErr.Error(), "performing tls handshake") {
			t.Errorf("expected server error to be about handshake, but got: %v", serverErr)
		}
	})

	t.Run("Incomplete Initial Read", func(t *testing.T) {
		serverErrChannel := runServerEcho()

		clientConn, err := net.Dial("tcp", baseListener.Addr().String())
		if err != nil {
			t.Fatalf("client failed to dial: %v", err)
		}

		// Write 2 bytes only
		_, err = clientConn.Write([]byte{0x01, 0x02})
		clientConn.Close()

		serverErr := <-serverErrChannel
		if serverErr == nil {
			t.Fatal("expected an error from incomplete read, but got nil")
		}

		if !strings.Contains(serverErr.Error(), "peaking initial bytes") {
			t.Errorf("expected error to contain 'peaking initial bytes', but got: %v", serverErr)
		}

		if !errors.Is(serverErr, io.EOF) && !errors.Is(serverErr, io.ErrUnexpectedEOF) {
			t.Errorf("expected error to wrap EOF or UnexpectedEOF, but got: %v", serverErr)
		}
	})
}

// mockListener allows custom methos to be implemented for test cases
type mockListener struct {
	accept func() (net.Conn, error)
	close  func() error
	addr   func() net.Addr
}

func (m *mockListener) Accept() (net.Conn, error) { return m.accept() }
func (m *mockListener) Close() error              { return m.close() }
func (m *mockListener) Addr() net.Addr            { return m.addr() }

func TestMarasiListener_RecoversFromError(t *testing.T) {
	var acceptCount atomic.Int32

	want := []byte("hello marasi")

	// Failing Listener will fail on the first Accept and then error
	failingListener := &mockListener{
		accept: func() (net.Conn, error) {
			currentCount := acceptCount.Add(1)
			if currentCount == 1 {
				return nil, errors.New("recoverable error")
			}
			server, client := net.Pipe()
			go func() {
				client.Write([]byte("hello marasi"))
				client.Close()
			}()
			return server, nil
		},
	}

	marasiListener := NewMarasiListener(failingListener)
	conn, err := marasiListener.Accept()

	// The first error should be handled gracefully by MarasiListener
	if err != nil {
		t.Fatalf("MarasiListener.Accept() failed: %v", err)
	}

	defer conn.Close()

	got := make([]byte, len(want))
	_, err = conn.Read(got)
	if err != nil && err != io.EOF {
		t.Fatalf("failed to read from the connection: %v", err)
	}

	if !bytes.Equal(want, got) {
		t.Errorf("expected %s got %v", want, got)
	}

	acceptedCount := acceptCount.Load()
	if acceptedCount != 2 {
		t.Errorf("expected 2 got %d", acceptedCount)
	}

}

func TestMarasiListener_FatalError(t *testing.T) {
	var acceptCount atomic.Int32

	// fatalListener will immediately return a fatal error (net.ErrClosed)
	fatalListener := &mockListener{
		accept: func() (net.Conn, error) {
			acceptCount.Add(1)
			return nil, net.ErrClosed
		},
	}

	marasiListener := NewMarasiListener(fatalListener)
	_, err := marasiListener.Accept()

	if err == nil {
		t.Fatal("expected a fatal error but got nil")
	}

	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("expected error to be net.ErrClosed, but got: %v", err)
	}

	acceptedCount := acceptCount.Load()
	if acceptedCount != 1 {
		t.Errorf("expected 1 but got %d", acceptedCount)
	}
}
