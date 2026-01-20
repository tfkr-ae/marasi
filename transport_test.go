package marasi

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
)

func testCert(t *testing.T) *x509.Certificate {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatalf("generating private key: %v", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating serial number: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Marasi Test"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}

	return cert
}

type testBaseRoundTripper struct {
	wasCalled bool
	response  *http.Response
	err       error
	request   *http.Request
}

func (tR *testBaseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tR.wasCalled = true
	tR.request = req
	if tR.response == nil {
		tR.response = &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(bytes.NewBufferString("")),
			Header:     make(http.Header),
		}
	}
	return tR.response, nil
}

func TestMarasiRoundTripper(t *testing.T) {
	cert := testCert(t)

	t.Run("request to http://marasi.cert should return the certificate", func(t *testing.T) {
		baseRoundTripper := &testBaseRoundTripper{}
		roundTripper := &marasiRoundTripper{
			cert: cert,
			base: baseRoundTripper,
		}

		req := httptest.NewRequest("GET", "http://marasi.cert", nil)
		resp, err := roundTripper.RoundTrip(req)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if baseRoundTripper.wasCalled {
			t.Fatal("expected base RoundTrip to not be called")
		}

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("wanted: %d\ngot: %d", http.StatusOK, resp.StatusCode)
		}

		got := resp.Header.Get("Content-Type")
		want := "application/x-x509-ca-cert"
		if want != got {
			t.Errorf("wanted ContentType: %s\ngot: %v", want, got)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading response body: %v", err)
		}
		defer resp.Body.Close()

		if !bytes.Equal(body, cert.Raw) {
			t.Fatalf("wanted %q\ngot: %q", cert.Raw, body)
		}
	})

	t.Run("request to http://marasi.cert/ should return the certificate", func(t *testing.T) {
		baseRoundTripper := &testBaseRoundTripper{}
		roundTripper := &marasiRoundTripper{
			cert: cert,
			base: baseRoundTripper,
		}

		req := httptest.NewRequest("GET", "http://marasi.cert/", nil)
		resp, err := roundTripper.RoundTrip(req)

		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if baseRoundTripper.wasCalled {
			t.Fatal("expected base RoundTrip to not be called")
		}

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("wanted: %d\ngot: %d", http.StatusOK, resp.StatusCode)
		}

		got := resp.Header.Get("Content-Type")
		want := "application/x-x509-ca-cert"
		if want != got {
			t.Errorf("wanted ContentType: %s\ngot: %v", want, got)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading response body: %v", err)
		}
		defer resp.Body.Close()

		if !bytes.Equal(body, cert.Raw) {
			t.Fatalf("wanted %q\ngot: %q", cert.Raw, body)
		}
	})

	t.Run("requests to any other host should call the base RoundTrip", func(t *testing.T) {
		want := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("Marasi Test")),
			Header:     make(http.Header),
		}
		baseRoundTripper := &testBaseRoundTripper{
			response: want,
		}
		roundTripper := &marasiRoundTripper{
			cert: cert,
			base: baseRoundTripper,
		}

		req := httptest.NewRequest("GET", "https://marasi.app", nil)

		resp, err := roundTripper.RoundTrip(req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if !baseRoundTripper.wasCalled {
			t.Fatal("expected base RoundTrip to be called")
		}

		if baseRoundTripper.request != req {
			t.Error("expected base RoundTrip to receive an modified request")
		}

		if resp != want {
			t.Errorf("wanted: %v\n got: %v", resp, want)
		}
	})

	t.Run("requests without User-Agent should not receive the Go default", func(t *testing.T) {
		baseRoundTripper := &testBaseRoundTripper{
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("UA Test")),
			},
		}
		roundTripper := &marasiRoundTripper{
			cert: cert,
			base: baseRoundTripper,
		}

		req := httptest.NewRequest("GET", "https://marasi.app", nil)
		req.Header.Del("User-Agent")

		_, err := roundTripper.RoundTrip(req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		val, ok := baseRoundTripper.request.Header["User-Agent"]
		if !ok {
			t.Error("wanted: User-Agent key to be present\ngot: missing")
		} else if len(val) > 0 && val[0] != "" {
			t.Errorf("wanted: %q\ngot: %q", "", val[0])
		}
	})
}

func TestMarasiTransportDialTLSContext(t *testing.T) {
	marasiCert := testCert(t)
	transport := newMarasiTransport(marasiCert)

	t.Run("request to standard HTTPS server should pass through", func(t *testing.T) {
		testTLSServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("marasi tls"))
		}))
		defer testTLSServer.Close()

		if mrt, ok := transport.(*marasiRoundTripper); ok {
			if ht, ok := mrt.base.(*http.Transport); ok {
				ht.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			}
		}

		testClient := &http.Client{
			Transport: transport,
		}

		resp, err := testClient.Get(testTLSServer.URL)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}
		defer resp.Body.Close()

		if resp.Proto != "HTTP/1.1" {
			t.Errorf("wanted: HTTP/1.1\ngot: %s", resp.Proto)
		}

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("wanted: %d\ngot: %d", http.StatusOK, resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading response body: %v", err)
		}
		if string(body) != "marasi tls" {
			t.Fatalf("wanted %q\ngot: %q", "marasi tls", body)
		}
	})

	t.Run("requests to closed ports should fail", func(t *testing.T) {
		testClient := &http.Client{
			Transport: transport,
		}
		_, err := testClient.Get("https://127.0.0.1:1")
		if err == nil {
			t.Fatal("wanted an error but got nil")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("wanted: %s\ngot: %v", syscall.ECONNREFUSED, err)
		}
	})
}
