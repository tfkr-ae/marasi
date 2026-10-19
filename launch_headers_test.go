package marasi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// headerOrigin records the headers of every request it receives.
func headerOrigin(t *testing.T) (*httptest.Server, chan *http.Request) {
	t.Helper()
	seen := make(chan *http.Request, 4)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(origin.Close)
	return origin, seen
}

func originRequest(t *testing.T, seen chan *http.Request) *http.Request {
	t.Helper()
	select {
	case r := <-seen:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the origin")
		return nil
	}
}

func storedRaw(t *testing.T, h *launchHarness) string {
	t.Helper()
	select {
	case req := <-h.requests:
		return string(req.Raw)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the stored request")
		return ""
	}
}

// headerLines returns the values of every line of the raw head named name.
func headerLines(raw, name string) []string {
	head, _, _ := strings.Cut(raw, "\r\n\r\n")
	var values []string
	for _, line := range strings.Split(head, "\r\n")[1:] {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(key, name) {
			values = append(values, strings.TrimSpace(value))
		}
	}
	return values
}

func TestProxy_LaunchAddsNoHeaders(t *testing.T) {
	tests := []struct {
		name       string
		connection string // the Connection line the researcher wrote, or "" for none
		want       []string
	}{
		{name: "no Connection header", want: nil},
		{name: "Connection keep-Alive", connection: "keep-Alive", want: []string{"keep-Alive"}},
		{name: "Connection close", connection: "close", want: []string{"close"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin, seen := headerOrigin(t)
			h := startLaunchHarness(t)

			raw := "GET /headers HTTP/1.1\r\nHost: " + strings.TrimPrefix(origin.URL, "http://") + "\r\n"
			if tt.connection != "" {
				raw += "Connection: " + tt.connection + "\r\n"
			}
			raw += "\r\n"
			if _, err := h.proxy.Launch(raw, h.launchpadID.String(), false); err != nil {
				t.Fatalf("Launch() returned unexpected error: %v", err)
			}

			stored := storedRaw(t, h)
			if got := headerLines(stored, "Connection"); !slices.Equal(got, tt.want) {
				t.Errorf("stored Connection lines\nwanted:\n%q\ngot:\n%q\nraw:\n%s", tt.want, got, stored)
			}
			if got := headerLines(stored, "Accept-Encoding"); got != nil {
				t.Errorf("stored Accept-Encoding lines\nwanted:\nnone\ngot:\n%q", got)
			}

			sent := originRequest(t, seen)
			if got := sent.Header["Connection"]; !slices.Equal(got, tt.want) {
				t.Errorf("Connection the origin received\nwanted:\n%q\ngot:\n%q", tt.want, got)
			}
			if got := sent.Header["Accept-Encoding"]; got != nil {
				t.Errorf("Accept-Encoding the origin received\nwanted:\nnone\ngot:\n%q", got)
			}
			h.waitFinished(t)
		})
	}
}

func TestProxy_ProxiedRequestGainsNoAcceptEncoding(t *testing.T) {
	origin, seen := headerOrigin(t)
	h := startLaunchHarness(t)

	proxyURL, err := url.Parse("http://" + h.proxy.Addr + ":" + h.proxy.Port)
	if err != nil {
		t.Fatalf("parsing proxy url: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableCompression: true}}
	res, err := client.Get(origin.URL + "/headers")
	if err != nil {
		t.Fatalf("sending request through the proxy: %v", err)
	}
	res.Body.Close()

	if got := originRequest(t, seen).Header["Accept-Encoding"]; got != nil {
		t.Errorf("Accept-Encoding the origin received\nwanted:\nnone\ngot:\n%q", got)
	}
}
