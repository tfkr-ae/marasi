package marasi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// emptyBodyFraming is how the origin saw an empty request body framed.
type emptyBodyFraming struct {
	transferEncoding []string
	contentLength    int64
	header           string
}

func emptyBodyOrigin(t *testing.T) (*httptest.Server, chan emptyBodyFraming) {
	t.Helper()
	seen := make(chan emptyBodyFraming, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- emptyBodyFraming{r.TransferEncoding, r.ContentLength, r.Header.Get("Content-Length")}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(origin.Close)
	return origin, seen
}

func wantContentLengthZero(t *testing.T, seen chan emptyBodyFraming) {
	t.Helper()
	select {
	case got := <-seen:
		if len(got.transferEncoding) != 0 || got.contentLength != 0 || got.header != "0" {
			t.Fatalf("\nwanted:\nContent-Length: 0 and no Transfer-Encoding\ngot:\nTransfer-Encoding %v, ContentLength %d, Content-Length header %q", got.transferEncoding, got.contentLength, got.header)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the origin")
	}
}

func TestProxy_EmptyBodyIsSentWithContentLengthZero(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		t.Run(method+" through the proxy", func(t *testing.T) {
			origin, seen := emptyBodyOrigin(t)
			h := startLaunchHarness(t)

			req, err := http.NewRequest(method, origin.URL+"/empty", http.NoBody)
			if err != nil {
				t.Fatalf("creating request: %v", err)
			}
			res, err := h.outsideClient(t).Do(req)
			if err != nil {
				t.Fatalf("sending request through the proxy: %v", err)
			}
			res.Body.Close()
			wantContentLengthZero(t, seen)
		})

		t.Run(method+" launched", func(t *testing.T) {
			origin, seen := emptyBodyOrigin(t)
			h := startLaunchHarness(t)

			host := strings.TrimPrefix(origin.URL, "http://")
			raw := method + " /empty HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 0\r\n\r\n"
			if _, err := h.proxy.Launch(raw, h.launchpadID.String(), false); err != nil {
				t.Fatalf("Launch() returned unexpected error: %v", err)
			}
			h.waitFinished(t)
			wantContentLengthZero(t, seen)
		})
	}

	t.Run("POST an extension empties", func(t *testing.T) {
		origin, seen := emptyBodyOrigin(t)
		h := startLaunchHarness(t)
		updateExtension(t, h.proxy, "workshop", `
			function processRequest(request)
				request:set_body("")
			end
		`)

		res, err := h.outsideClient(t).Post(origin.URL+"/empty", "text/plain", strings.NewReader("body"))
		if err != nil {
			t.Fatalf("sending request through the proxy: %v", err)
		}
		res.Body.Close()
		wantContentLengthZero(t, seen)
	})
}
