package rawhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"testing"
)

var forcedErr = errors.New("forced error")

// erroringReader will return an error on Reads
type erroringReader struct{}

func (er *erroringReader) Read(p []byte) (n int, err error) {
	return 0, forcedErr
}

func (er *erroringReader) Close() error {
	return nil
}

func TestPrettify(t *testing.T) {
	t.Run("Prettify Valid JSON", func(t *testing.T) {
		want := []byte("{\n  \"a\": 1,\n  \"b\": 2\n}")
		got, err := Prettify([]byte(`{"b":2,"a":1}`))
		if err != nil {
			t.Fatalf("prettifying json: %v", err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("wanted:\n%q\ngot:    %q", want, got)
		}
	})

	t.Run("Invalid JSON", func(t *testing.T) {
		got, err := Prettify([]byte(`{"b":2,"a":2,}`))
		if err != nil {
			t.Fatalf("prettifying invalid json: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected an empty string got %q", got)
		}
	})

	t.Run("Prettify Valid XML", func(t *testing.T) {
		want := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<root>\n <item>1</item>\n</root>\n")
		got, err := Prettify([]byte(`<?xml version="1.0" encoding="UTF-8"?><root><item>1</item></root>`))
		if err != nil {
			t.Fatalf("prettying xml: %v", err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("wanted:\n%q\ngot:    %q", want, got)
		}
	})

	t.Run("Invalid XML", func(t *testing.T) {
		got, err := Prettify([]byte(`<?xml version="1.0" encoding="UTF-8"?<root><item>1</item></root>`))
		if err != nil {
			t.Fatalf("prettifying invalid xml: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected an empty string got %q", got)
		}
	})

	t.Run("Prettify Valid HTML", func(t *testing.T) {
		want := []byte("<html>\n <body>\n  <p>Hello</p>\n </body>\n</html>\n")
		got, err := Prettify([]byte(`<html><body><p>Hello</p></body></html>`))
		if err != nil {
			t.Fatalf("prettifying HTML: %v", err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("wanted:\n%q\ngot:    %q", want, got)
		}
	})

	t.Run("Prettify Valid HTML with DOCTYPE", func(t *testing.T) {
		want := []byte("<!DOCTYPE html>\n<html>\n <body>\n  <p>Hello</p>\n </body>\n</html>\n")
		got, err := Prettify([]byte(`<!DOCTYPE html><html><body><p>Hello</p></body></html>`))
		if err != nil {
			t.Fatalf("prettifying HTML with doctype: %v", err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("wanted:\n%q\ngot:    %q", want, got)
		}
	})
	t.Run("Prettify Valid HTML Fragment", func(t *testing.T) {
		want := []byte("<div>\n <p>Hello</p>\n</div>\n")
		got, err := Prettify([]byte(`<div><p>Hello</p></div>`))
		if err != nil {
			t.Fatalf("prettifying html fragment: %v", err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("wanted:\n%q\ngot:    %q", want, got)
		}
	})

	t.Run("Plaintext should not be prettified", func(t *testing.T) {
		got, err := Prettify([]byte(`hello, marasi`))
		if err != nil {
			t.Fatalf("prettifying plaintext: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected an empty string got %q", got)
		}
	})

	t.Run("Empty body", func(t *testing.T) {
		got, err := Prettify([]byte(``))
		if err != nil {
			t.Fatalf("prettifying empty body: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected an empty string got %q", got)
		}
	})

	t.Run("Prettify with whitespace", func(t *testing.T) {
		want := []byte("{\n  \"a\": 1,\n  \"b\": 2\n}")
		got, err := Prettify([]byte(`   {"b":2,"a":1}`))
		if err != nil {
			t.Fatalf("prettifying with whitespace : %v", err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("wanted:\n%q\ngot:    %q", want, got)
		}
	})
}

func TestRecalculateContentLength(t *testing.T) {
	t.Run("Missing content-length", func(t *testing.T) {
		want := []byte("GET /get?id=1 HTTP/1.1\r\n" +
			"Host: httpbin.org\r\n" +
			"Accept: */*\r\n" +
			"Accept-Encoding: gzip\r\n" +
			"User-Agent: curl/8.7.1\r\n" +
			"Content-Length: 6\r\n" +
			"\r\n" +
			"Marasi")

		got, err := RecalculateContentLength([]byte("GET /get?id=1 HTTP/1.1\n" +
			"Host: httpbin.org\n" +
			"Accept: */*\n" +
			"Accept-Encoding: gzip\n" +
			"User-Agent: curl/8.7.1\n\n" +
			"Marasi"))
		if err != nil {
			t.Fatalf("recalculating content length: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("wanted:\n%q\n\ngot:\n%q", want, got)
		}
	})

	t.Run("Incorrect Content Length", func(t *testing.T) {
		want := []byte("GET /get?id=1 HTTP/1.1\r\n" +
			"Host: httpbin.org\r\n" +
			"Accept: */*\r\n" +
			"Accept-Encoding: gzip\r\n" +
			"User-Agent: curl/8.7.1\r\n" +
			"Content-Length: 6\r\n" +
			"\r\n" +
			"Marasi")

		got, err := RecalculateContentLength([]byte("GET /get?id=1 HTTP/1.1\n" +
			"Host: httpbin.org\n" +
			"Accept: */*\n" +
			"Accept-Encoding: gzip\n" +
			"User-Agent: curl/8.7.1\n" +
			"Content-Length: 10\n\n" +
			"Marasi"))
		if err != nil {
			t.Fatalf("recalculating content length: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("wanted:\n%q\n\ngot:\n%q", want, got)
		}
	})

	t.Run("Lowercase Headers", func(t *testing.T) {
		want := []byte("GET /get?id=1 HTTP/1.1\r\n" +
			"Host: httpbin.org\r\n" +
			"Accept: */*\r\n" +
			"Accept-Encoding: gzip\r\n" +
			"User-Agent: curl/8.7.1\r\n" +
			"Content-Length: 6\r\n" +
			"\r\n" +
			"Marasi")

		got, err := RecalculateContentLength([]byte("GET /get?id=1 HTTP/1.1\n" +
			"Host: httpbin.org\n" +
			"Accept: */*\n" +
			"Accept-Encoding: gzip\n" +
			"User-Agent: curl/8.7.1\n" +
			"content-Length: 10\n\n" +
			"Marasi"))
		if err != nil {
			t.Fatalf("recalculating content length: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("wanted:\n%q\n\ngot:\n%q", want, got)
		}
	})

	t.Run("No body in request", func(t *testing.T) {
		want := []byte("GET /get?id=1 HTTP/1.1\r\n" +
			"Host: httpbin.org\r\n" +
			"Accept: */*\r\n" +
			"Accept-Encoding: gzip\r\n" +
			"User-Agent: curl/8.7.1\r\n" +
			"\r\n")

		got, err := RecalculateContentLength([]byte("GET /get?id=1 HTTP/1.1\n" +
			"Host: httpbin.org\n" +
			"Accept: */*\n" +
			"Accept-Encoding: gzip\n" +
			"User-Agent: curl/8.7.1\n" +
			"content-Length: 10\n\n"))
		if err != nil {
			t.Fatalf("recalculating content length: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("wanted:\n%q\n\ngot:\n%q", want, got)
		}
	})

	t.Run("Malformed request", func(t *testing.T) {
		_, err := RecalculateContentLength([]byte("GET /get?id=1 HTTP/1.1\n" +
			"Host: httpbin.org\n" +
			"Accept: */*\n" +
			"Accept-Encoding: gzip\n" +
			"User-Agent: curl/8.7.1\n" +
			"Marasi"))
		if err == nil {
			t.Fatal("expected an error when recalculating a malformed request / response")
		}
	})

	t.Run("Normalize \r\n", func(t *testing.T) {
		want := []byte("GET /get?id=1 HTTP/1.1\r\n" +
			"Host: httpbin.org\r\n" +
			"Accept: */*\r\n" +
			"Accept-Encoding: gzip\r\n" +
			"User-Agent: curl/8.7.1\r\n" +
			"Content-Length: 6\r\n" +
			"\r\n" +
			"Marasi")

		got, err := RecalculateContentLength([]byte("GET /get?id=1 HTTP/1.1\r\n" +
			"Host: httpbin.org\r\n" +
			"Accept: */*\r\n" +
			"Accept-Encoding: gzip\r\n" +
			"User-Agent: curl/8.7.1\r\n" +
			"Content-Length: 10\r\n\r\n" +
			"Marasi"))
		if err != nil {
			t.Fatalf("recalculating content length: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("wanted:\n%q\n\ngot:\n%q", want, got)
		}
	})
}

func TestDumpRequest(t *testing.T) {
	t.Run("DumpRequest with Prettifiable Body (JSON)", func(t *testing.T) {
		inputBody := []byte(`{"b":2,"a":1}`)
		wantPrettyBody := "{\n  \"a\": 1,\n  \"b\": 2\n}"

		req, err := http.NewRequest(http.MethodGet, "/", io.NopCloser(bytes.NewReader(inputBody)))
		if err != nil {
			t.Fatalf("creating new request: %v", err)
		}

		rawDump, prettyDump, err := DumpRequest(req)
		if err != nil {
			t.Fatalf("dumping request: %v", err)
		}

		// Check that rawDump ends with the compact, original body
		if !bytes.HasSuffix(rawDump, inputBody) {
			t.Errorf("expected raw dump to end with\n%s\nbut got\n%q", inputBody, rawDump)
		}

		// Check that prettyDump ends with the formatted body
		if !strings.HasSuffix(prettyDump, wantPrettyBody) {
			t.Errorf("expected pretty dump to end with\n%s\nbut got\n%q", wantPrettyBody, prettyDump)
		}

		// Check that the body is still readable
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading body after dump: %v", err)
		}
		if !bytes.Equal(body, inputBody) {
			t.Errorf("expected body to not be empty after dumping: %q\n%q", body, inputBody)
		}
	})
	t.Run("DumpRequest with plaintext body", func(t *testing.T) {
		inputBody := []byte(`hello, marasi`)
		wantRaw := []byte(`hello, marasi`)
		req, err := http.NewRequest(http.MethodGet, "/", io.NopCloser(bytes.NewReader(inputBody)))
		if err != nil {
			t.Fatalf("creating new request: %v", err)
		}

		rawDump, prettyDump, err := DumpRequest(req)
		if err != nil {
			t.Fatalf("dumping request: %v", err)
		}

		// Check that wantRaw is there
		if !bytes.HasSuffix(rawDump, wantRaw) {
			t.Errorf("expected raw dump to end with\n%s but got\n%q", wantRaw, rawDump)
		}

		// Check that prettyDump is empty
		if prettyDump != "" {
			t.Errorf("expected prettyDump to be empty but got : %q", prettyDump)
		}

		// Check that the body is still readable
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading body : %v", err)
		}

		if !bytes.Equal(body, inputBody) {
			t.Errorf("expected body to not be empty after dumping: %q\n%q", body, inputBody)
		}
	})

	t.Run("DumpRequest with empty body", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "/", io.NopCloser(bytes.NewReader([]byte{})))
		if err != nil {
			t.Fatalf("creating new request: %v", err)
		}

		headers, err := httputil.DumpRequest(req, false)
		if err != nil {
			t.Fatalf("dumping request (httputil): %v", err)
		}

		rawDump, prettyDump, err := DumpRequest(req)
		if err != nil {
			t.Fatalf("dumping request (rawhttp): %v", err)
		}

		// Check that prettyDump is empty
		if prettyDump != "" {
			t.Errorf("expected prettyDump to be empty but got : %q", prettyDump)
		}

		// Check that the rawDump is equivalent to the headers
		if !bytes.Equal(rawDump, headers) {
			t.Errorf("expected\n%q\nbut got\n%q", headers, rawDump)
		}

		// Check that the body is still readable
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading body : %v", err)
		}

		if len(body) != 0 {
			t.Errorf("expected body to not be empty after dumping: %q\n%q", body, []byte{})
		}
	})

	t.Run("DumpRequest read body fails", func(t *testing.T) {
		wantedContext := "reading request body"
		req, err := http.NewRequest(http.MethodGet, "/", &erroringReader{})
		if err != nil {
			t.Fatalf("creating request: %v", err)
		}

		_, _, err = DumpRequest(req)

		// Check if err is nil
		if err == nil {
			t.Fatal("expected an error when reading the body, but got nil")
		}

		// Check for the forced error
		if !errors.Is(err, forcedErr) {
			t.Errorf("expected error to wrap %q, but got: %v", forcedErr, err)
		}

		// Check that DumpRequest added its context
		if !strings.Contains(err.Error(), wantedContext) {
			t.Errorf("expected error message to contain %s but got: %v", wantedContext, err)
		}
	})
}

func TestDumpResponse(t *testing.T) {
	t.Run("DumpResponse with Prettifiable Body (JSON)", func(t *testing.T) {
		responseBody := []byte(`{"b":2,"a":1}`)
		wantPrettyBody := "{\n  \"a\": 1,\n  \"b\": 2\n}"

		res := &http.Response{
			Body: io.NopCloser(bytes.NewReader(responseBody)),
		}

		rawDump, prettyDump, err := DumpResponse(res)
		if err != nil {
			t.Fatalf("dumping response: %v", err)
		}

		// Check that rawDump ends with the original body
		if !bytes.HasSuffix(rawDump, responseBody) {
			t.Errorf("expected raw dump to end with\n%s\nbut got\n%q", responseBody, rawDump)
		}

		// Check that prettyDump ends with the formatted body
		if !strings.HasSuffix(prettyDump, wantPrettyBody) {
			t.Errorf("expected pretty dump to end with\n%q\nbut got\n%q", wantPrettyBody, prettyDump)
		}

		// Check that the body is still readable
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Errorf("reading body after dump: %v", err)
		}
		if !bytes.Equal(body, responseBody) {
			t.Errorf("expected body to not be empty after dumping: %q\n%q", body, responseBody)
		}
	})

	t.Run("DumpResponse with plaintext body", func(t *testing.T) {
		responseBody := []byte(`hello, marasi`)

		res := &http.Response{
			Body: io.NopCloser(bytes.NewReader(responseBody)),
		}

		rawDump, prettyDump, err := DumpResponse(res)
		if err != nil {
			t.Fatalf("dumping response: %v", err)
		}

		// Check that responseBody is there
		if !bytes.HasSuffix(rawDump, responseBody) {
			t.Errorf("expected raw dump to end with\n%s but got\n%q", responseBody, rawDump)
		}

		// Check that prettyDump is empty
		if prettyDump != "" {
			t.Errorf("expected prettyDump to be empty but got : %q", prettyDump)
		}

		// Check that the body is still readable
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Errorf("reading body : %v", err)
		}

		if !bytes.Equal(body, responseBody) {
			t.Errorf("expected body to not be empty after dumping: %q\n%q", body, responseBody)
		}
	})

	t.Run("DumpResponse with empty body", func(t *testing.T) {
		responseBody := []byte{}
		res := &http.Response{
			Body: io.NopCloser(bytes.NewReader(responseBody)),
		}

		headers, err := httputil.DumpResponse(res, false)
		if err != nil {
			t.Fatalf("dumping response (httputil): %v", err)
		}

		rawDump, prettyDump, err := DumpResponse(res)
		if err != nil {
			t.Fatalf("dumping response (rawhttp): %v", err)
		}

		// Check that prettyDump is empty
		if prettyDump != "" {
			t.Errorf("expected prettyDump to be empty but got : %q", prettyDump)
		}

		// Check that the rawDump is equivalent to the headers
		if !bytes.Equal(rawDump, headers) {
			t.Errorf("expected\n%q\nbut got\n%q", headers, rawDump)
		}

		// Check that the body is still readable
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Errorf("reading body : %v", err)
		}

		if !bytes.Equal(body, responseBody) {
			t.Errorf("expected body to be empty after dumping: %q\n%q", body, responseBody)
		}
	})

	t.Run("DumpResponse read body fails", func(t *testing.T) {
		wantedContext := "reading response body"
		res := &http.Response{
			Body: &erroringReader{},
		}

		_, _, err := DumpResponse(res)

		// Check if err is nil
		if err == nil {
			t.Fatal("expected an error when reading the body, but got nil")
		}

		// Check for the forced error
		if !errors.Is(err, forcedErr) {
			t.Errorf("expected error to wrap %q, but got: %v", forcedErr, err)
		}

		// Check that DumpResponse added its context
		if !strings.Contains(err.Error(), wantedContext) {
			t.Errorf("expected error message to contain %s but got: %v", wantedContext, err)
		}
	})

	t.Run("DumpResponse with nil body", func(t *testing.T) {
		res := &http.Response{
			StatusCode: http.StatusNoContent,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(http.Header),
			Body:       nil,
		}

		headers, err := httputil.DumpResponse(res, false)
		if err != nil {
			t.Fatalf("dumping response (httputil): %v", err)
		}

		rawDump, prettyDump, err := DumpResponse(res)
		if err != nil {
			t.Fatalf("dumping response (rawhttp): %v", err)
		}

		if prettyDump != "" {
			t.Errorf("expected prettyDump to be empty but got : %q", prettyDump)
		}

		if !bytes.Equal(rawDump, headers) {
			t.Errorf("expected\n%q\nbut got\n%q", headers, rawDump)
		}
	})

}
func TestRebuildRequest(t *testing.T) {
	t.Run("RebuildRequest (Success with POST Body)", func(t *testing.T) {
		rawBody := `{"a":1}`
		rawRequest := "POST /test HTTP/1.1\r\n" +
			"Host: example.com\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: 100\r\n" + // Deliberately wrong length
			"\r\n" +
			rawBody

		originalRequest, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
		if err != nil {
			t.Fatalf("creating original request: %v", err)
		}

		newReq, err := RebuildRequest([]byte(rawRequest), originalRequest)
		if err != nil {
			t.Fatalf("rebuilding request: %v", err)
		}

		// Check method
		if newReq.Method != http.MethodPost {
			t.Errorf("expected method POST, got %s", newReq.Method)
		}

		// Check body
		body, err := io.ReadAll(newReq.Body)
		if err != nil {
			t.Fatalf("reading rebuilt body: %v", err)
		}
		if string(body) != rawBody {
			t.Errorf("body mismatch. want:\n%q\ngot:\n%q", rawBody, body)
		}

		// Check Content-Length was fixed
		if newReq.Header.Get("Content-Length") != "7" {
			t.Errorf("expected Content-Length to be recalculated to 7, got %s", newReq.Header.Get("Content-Length"))
		}
	})

	t.Run("RebuildRequest (Context and Scheme Preserved)", func(t *testing.T) {
		type contextKey string
		var testKey = contextKey("my-key")
		wantValue := "my-value"

		// Create an original request with a specific context and scheme
		ctx := context.WithValue(context.Background(), testKey, wantValue)
		originalRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
		if err != nil {
			t.Fatalf("creating original request: %v", err)
		}

		rawRequest := "GET /test HTTP/1.1\r\nHost: example.com\r\n\r\n"

		newReq, err := RebuildRequest([]byte(rawRequest), originalRequest)
		if err != nil {
			t.Fatalf("rebuilding request: %v", err)
		}

		// Check Scheme
		if newReq.URL.Scheme != "https" {
			t.Errorf("expected scheme 'https', got %q", newReq.URL.Scheme)
		}

		// Check Context
		gotValue := newReq.Context().Value(testKey)
		if gotValue != wantValue {
			t.Errorf("context value mismatch. want %q, got %q", wantValue, gotValue)
		}
	})

	t.Run("RebuildRequest (RecalculateContentLength Fails)", func(t *testing.T) {
		// Malformed: no \r\n\r\n
		rawRequest := "GET /test HTTP/1.1\r\nHost: example.com"
		originalRequest, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)

		_, err := RebuildRequest([]byte(rawRequest), originalRequest)
		if err == nil {
			t.Fatal("expected an error, but got nil")
		}
		if !strings.Contains(err.Error(), "recalculating content length") {
			t.Errorf("expected error to contain 'recalculating content length', got %v", err)
		}
	})

	t.Run("RebuildRequest (ReadRequest Fails)", func(t *testing.T) {
		// Malformed: Not valid HTTP
		rawRequest := "this is not a request\r\n\r\n"
		originalRequest, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)

		_, err := RebuildRequest([]byte(rawRequest), originalRequest)
		if err == nil {
			t.Fatal("expected an error, but got nil")
		}
		if !strings.Contains(err.Error(), "reading raw request") {
			t.Errorf("expected error to contain 'reading raw request', got %v", err)
		}
	})
}

func TestRebuildResponse(t *testing.T) {
	t.Run("RebuildResponse (Success 200 OK with Body)", func(t *testing.T) {
		rawBody := `{"ok":true}`
		rawResponse := "HTTP/1.1 200 OK\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: 100\r\n" + // Deliberately wrong length
			"\r\n" +
			rawBody

		// ReadResponse needs a dummy request
		dummyReq, _ := http.NewRequest(http.MethodGet, "/", nil)

		newRes, err := RebuildResponse([]byte(rawResponse), dummyReq)
		if err != nil {
			t.Fatalf("rebuilding response: %v", err)
		}

		// Check status code
		if newRes.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", newRes.StatusCode)
		}

		// Check body
		body, err := io.ReadAll(newRes.Body)
		if err != nil {
			t.Fatalf("reading rebuilt body: %v", err)
		}
		if string(body) != rawBody {
			t.Errorf("body mismatch. want:\n%q\ngot:\n%q", rawBody, body)
		}

		// Check Content-Length was fixed
		if newRes.Header.Get("Content-Length") != "11" {
			t.Errorf("expected Content-Length to be recalculated to 11, got %s", newRes.Header.Get("Content-Length"))
		}
	})

	t.Run("RebuildResponse (Success 204 No Content)", func(t *testing.T) {
		rawResponse := "HTTP/1.1 204 No Content\r\n" +
			"Connection: keep-alive\r\n" +
			"\r\n"

		dummyReq, _ := http.NewRequest(http.MethodGet, "/", nil)

		newRes, err := RebuildResponse([]byte(rawResponse), dummyReq)
		if err != nil {
			t.Fatalf("rebuilding response: %v", err)
		}

		// Check status code
		if newRes.StatusCode != http.StatusNoContent {
			t.Errorf("expected status 204, got %d", newRes.StatusCode)
		}

		// Check body is empty
		body, err := io.ReadAll(newRes.Body)
		if err != nil {
			t.Fatalf("reading rebuilt body: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("expected empty body, got %q", body)
		}
	})

	t.Run("RebuildResponse (RecalculateContentLength Fails)", func(t *testing.T) {
		// Malformed: no \r\n\r\n
		rawResponse := "HTTP/1.1 200 OK\r\nConnection: keep-alive"
		dummyReq, _ := http.NewRequest(http.MethodGet, "/", nil)

		_, err := RebuildResponse([]byte(rawResponse), dummyReq)
		if err == nil {
			t.Fatal("expected an error, but got nil")
		}
		if !strings.Contains(err.Error(), "recalculating content length") {
			t.Errorf("expected error to contain 'recalculating content length', got %v", err)
		}
	})

	t.Run("RebuildResponse (ReadResponse Fails)", func(t *testing.T) {
		// Malformed: Not valid HTTP
		rawResponse := "this is not a response\r\n\r\n"
		dummyReq, _ := http.NewRequest(http.MethodGet, "/", nil)

		_, err := RebuildResponse([]byte(rawResponse), dummyReq)
		if err == nil {
			t.Fatal("expected an error, but got nil")
		}
		if !strings.Contains(err.Error(), "reading raw response") {
			t.Errorf("expected error to contain 'reading raw response', got %v", err)
		}
	})
}
