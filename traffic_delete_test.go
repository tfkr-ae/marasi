package marasi

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestProxy_WriteToDBResponseForDeletedPair(t *testing.T) {
	t.Run("should drop the response of a deleted in-flight pair with only a debug log", func(t *testing.T) {
		conn, err := db.New(":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		repo := db.NewProxyRepo(conn)
		defer repo.Close()
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		err = repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: "a.example.com", Path: "/held",
			Raw: []byte("GET /held HTTP/1.1\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.DeleteTraffic(domain.Selection{IDs: []uuid.UUID{id}}); err != nil {
			t.Fatalf("DeleteTraffic: %v", err)
		}

		var stdLog strings.Builder
		previous := log.Writer()
		log.SetOutput(&stdLog)
		defer log.SetOutput(previous)
		var records bytes.Buffer
		proxy := &Proxy{
			TrafficRepo:    repo,
			Logger:         slog.New(slog.NewTextHandler(&records, &slog.HandlerOptions{Level: slog.LevelDebug})),
			DBWriteChannel: make(chan any, 1),
		}
		proxy.DBWriteChannel <- &domain.ProxyResponse{
			ID: id, Status: "200 OK", StatusCode: 200, ContentType: "text/html",
			Raw: []byte("HTTP/1.1 200 OK\r\n\r\nlate"), Prettified: "late", Metadata: map[string]any{}, RespondedAt: time.Now(),
		}
		close(proxy.DBWriteChannel)

		proxy.WriteToDB()

		if stdLog.Len() != 0 {
			t.Fatalf("\nwanted:\nnothing on the standard logger\ngot:\n%s", stdLog.String())
		}
		lines := strings.Split(strings.TrimSpace(records.String()), "\n")
		if len(lines) != 1 || !strings.Contains(lines[0], "level=DEBUG") || !strings.Contains(lines[0], id.String()) {
			t.Fatalf("\nwanted:\none debug record naming %s\ngot:\n%s", id, records.String())
		}
		if _, err := repo.GetRequestResponseRow(id); err == nil {
			t.Fatalf("\nwanted:\nno row for the deleted pair\ngot:\na row")
		}
		page, err := repo.ListSitemapChildren(context.Background(), domain.SitemapQuery{Limit: 10})
		if err != nil || len(page.Children) != 0 {
			t.Fatalf("\nwanted:\nan empty sitemap\ngot:\n%+v, %v", page.Children, err)
		}
	})
}
