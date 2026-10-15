package db

// BenchmarkCapturePair measures the writer's cost of capturing one pair: the
// proxy's InsertRequest then InsertResponse on a project that grows by one
// pair per iteration. The pairs have the shape of the proxied throughput
// check (rate.sh in the verify-marasi skill's evidence): a small GET with a
// short text/plain response, so the cost is the per-pair write overhead, not
// tokenising large bodies. ns/op is the mean per-pair capture time, and
// pairs/s its inverse. Run it at the size the throughput check reaches, and
// against main's write path by copying this file to a main checkout:
//
//	GOWORK=off go test -run '^$' -bench CapturePair -benchtime 50000x ./db/

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func BenchmarkCapturePair(b *testing.B) {
	conn, err := New(filepath.Join(b.TempDir(), "capture.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("opening the project: %v", err)
	}
	repo := NewProxyRepo(conn)
	b.Cleanup(func() { repo.Close() })

	const host = "127.0.0.1:63258"
	b.ResetTimer()
	start := time.Now()
	for i := range b.N {
		id, err := uuid.NewV7()
		if err != nil {
			b.Fatal(err)
		}
		path := fmt.Sprintf("/c/%d", i)
		requestedAt := time.Now()
		req := &domain.ProxyRequest{
			ID:          id,
			Scheme:      "http",
			Method:      "GET",
			Host:        host,
			Path:        path,
			Raw:         fmt.Appendf(nil, "GET http://%s%s HTTP/1.1\r\nHost: %s\r\nAccept-Encoding: identity\r\n\r\n", host, path, host),
			Metadata:    map[string]any{},
			RequestedAt: requestedAt,
		}
		if err := repo.InsertRequest(req); err != nil {
			b.Fatal(err)
		}
		body := "default " + path
		resp := &domain.ProxyResponse{
			ID:          id,
			Status:      "200 OK",
			StatusCode:  200,
			ContentType: "text/plain",
			Length:      fmt.Sprint(len(body)),
			Raw: fmt.Appendf(nil, "HTTP/1.1 200 OK\r\nServer: BaseHTTP/0.6 Python/3.14.8\r\nDate: %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
				requestedAt.UTC().Format(time.RFC1123), len(body), body),
			Metadata:    map[string]any{},
			RespondedAt: time.Now(),
		}
		if err := repo.InsertResponse(resp); err != nil {
			b.Fatal(err)
		}
	}
	// Index whatever capture left queued, so its cost counts too.
	if flusher, ok := any(repo).(interface{ flushIndexQueue() error }); ok {
		if err := flusher.flushIndexQueue(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "pairs/s")
}
