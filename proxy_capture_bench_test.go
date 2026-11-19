package marasi

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// BenchmarkProxyCapture measures one proxied, captured pair end to end: a
// keep-alive client sends through the proxy with the default modifier
// pipeline and the seeded extensions to a local origin, and the pair is
// stored in a project on disk, as the app runs it. ns/op is the time per
// pair, client to origin and back, capture included.
//
//	go test -run '^$' -bench ProxyCapture -count 10 .
func BenchmarkProxyCapture(b *testing.B) {
	payload := strings.Repeat("marasi ", 64)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/plain")
		if strings.HasPrefix(r.URL.Path, "/chunked") {
			w.(http.Flusher).Flush() // no Content-Length: the response goes out chunked
		}
		io.WriteString(w, payload)
	}))
	b.Cleanup(origin.Close)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"get", http.MethodGet, "/get", ""},
		{"get-chunked", http.MethodGet, "/chunked", ""},
		{"post", http.MethodPost, "/post", strings.Repeat("a=1&", 256)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			client := serveCaptureBenchProxy(b)
			target := origin.URL + c.path
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				req, err := http.NewRequest(c.method, fmt.Sprintf("%s/%d", target, i), strings.NewReader(c.body))
				if err != nil {
					b.Fatal(err)
				}
				res, err := client.Do(req)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, res.Body); err != nil {
					b.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != http.StatusOK {
					b.Fatalf("status %d", res.StatusCode)
				}
			}
		})
	}
}

// serveCaptureBenchProxy serves a proxy with the default pipeline and the
// seeded extensions, capturing into a project file, and returns a keep-alive
// client that sends through it.
func serveCaptureBenchProxy(b *testing.B) *http.Client {
	b.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.New(filepath.Join(b.TempDir(), "bench.marasi"), logger)
	if err != nil {
		b.Fatalf("opening the project: %v", err)
	}
	repository := db.NewProxyRepo(database)
	seeded, err := repository.GetExtensions()
	if err != nil {
		repository.Close()
		b.Fatalf("getting the seeded extensions: %v", err)
	}
	proxy, err := New(
		WithLogger(logger),
		WithConfigDir(b.TempDir()),
		WithDefaultRepositories(repository),
		WithExtensions(seeded),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithRequestHandler(func(domain.ProxyRequest) error { return nil }),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithBasePipeline(),
		WithDefaultModifierPipeline(),
	)
	if err != nil {
		repository.Close()
		b.Fatalf("creating proxy: %v", err)
	}
	listener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		proxy.Close()
		b.Fatalf("creating proxy listener: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- proxy.Serve(listener) }()
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(proxy.Addr, proxy.Port)}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), MaxIdleConnsPerHost: 1}
	b.Cleanup(func() {
		transport.CloseIdleConnections()
		proxy.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			b.Error("proxy serve did not stop")
		}
		repository.Close()
	})
	return &http.Client{Transport: transport}
}
