package marasi

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
)

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
	t.Run("requests to any other host should call the base RoundTrip", func(t *testing.T) {
		want := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("Marasi Test")),
			Header:     make(http.Header),
		}
		baseRoundTripper := &testBaseRoundTripper{
			response: want,
		}
		roundTripper := &marasiRoundTripper{base: baseRoundTripper}

		req := httptest.NewRequest("GET", "https://marasi.app", nil)

		resp, err := roundTripper.RoundTrip(req)
		if err != nil {
			t.Fatalf("wanted: nil\ngot: %v", err)
		}

		if !baseRoundTripper.wasCalled {
			t.Fatal("expected base RoundTrip to be called")
		}

		sent := baseRoundTripper.request
		if sent == req {
			t.Error("expected base RoundTrip to receive a copy of the request, not the request itself")
		}
		if sent.Method != req.Method || sent.URL != req.URL || sent.Body != req.Body {
			t.Errorf("wanted: copy of %s %s\ngot: %s %s", req.Method, req.URL, sent.Method, sent.URL)
		}

		if resp != want {
			t.Errorf("wanted: %v\n got: %v", resp, want)
		}
		if resp.Request != req {
			t.Error("expected the response to point at the original request")
		}
	})

	t.Run("requests without User-Agent should not receive the Go default", func(t *testing.T) {
		baseRoundTripper := &testBaseRoundTripper{
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("UA Test")),
			},
		}
		roundTripper := &marasiRoundTripper{base: baseRoundTripper}

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
	transport := newMarasiTransport(nil)

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
