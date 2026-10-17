//go:build bench1m

package db

// Ticket 24: query-mode sitemap levels, the way marasi-app reads them. It
// opens the project named by MARASI_BENCH_PROJECT in place, so point it at
// a clone (cp -c), never at a shared fixture. Run it by hand:
//
//	MARASI_BENCH_PROJECT=/path/clone.marasi \
//	  go test -tags bench1m -run '^$' -bench SitemapQuery -benchtime 1x ./db/

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// sitemapBenchLevels are the open levels the ticket measures.
var sitemapBenchLevels = []string{"", "https://app.example.com", "https://app.example.com/api", "https://app.example.com/api/v2"}

func BenchmarkSitemapQuery(b *testing.B) {
	source := os.Getenv("MARASI_BENCH_PROJECT")
	if source == "" {
		b.Skip("MARASI_BENCH_PROJECT is not set")
	}
	conn, err := New(source, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("opening: %v", err)
	}
	repo := NewProxyRepo(conn)
	defer repo.Close()
	var asOf uuid.UUID
	if err := repo.dbConn.Get(&asOf, `SELECT id FROM request ORDER BY id DESC LIMIT 1`); err != nil {
		b.Fatal(err)
	}
	var sparse string
	if err := repo.dbConn.Get(&sparse, `SELECT host FROM request WHERE host LIKE '%.internal.corp' GROUP BY host ORDER BY count(*) LIMIT 1`); err != nil {
		b.Fatal(err)
	}
	queries := []string{`status_code >= 500`, `host = "` + sparse + `"`, `host = "app.example.com"`, `son`}
	if only := os.Getenv("MARASI_BENCH_QUERY"); only != "" {
		queries = []string{only}
	}
	const rounds = 20
	ctx := context.Background()
	report := func(name string, call func() error) {
		var times []time.Duration
		for range rounds {
			started := time.Now()
			if err := call(); err != nil {
				b.Fatalf("%s: %v", name, err)
			}
			times = append(times, time.Since(started))
		}
		slices.Sort(times)
		fmt.Fprintf(os.Stderr, "%-75s p50 %8.2fms p95 %8.2fms\n", name, ms(times[len(times)/2]), ms(times[len(times)*95/100]))
	}
	for _, text := range queries {
		selection := &domain.SelectionQuery{Text: text, AsOf: asOf}
		for _, node := range sitemapBenchLevels {
			report(fmt.Sprintf("matching %q %q", text, short(node)), func() error {
				_, err := repo.ListSitemapChildren(ctx, domain.SitemapQuery{Node: node, Query: selection, Limit: domain.MaxSitemapPage})
				return err
			})
		}
		for _, node := range sitemapBenchLevels {
			report(fmt.Sprintf("all (app: stored + %d query pages) %q %q", 4, text, short(node)), func() error {
				return appAllScope(ctx, repo, node, selection)
			})
		}
		benchLevels(ctx, report, repo, text, selection)
	}
}

// benchLevels times ListSitemapLevels for each level alone and for the four
// levels in one call, in both scopes.
func benchLevels(ctx context.Context, report func(string, func() error), repo *Repository, text string, selection *domain.SelectionQuery) {
	for _, scope := range []domain.SitemapScope{domain.SitemapScopeMatching, domain.SitemapScopeAll} {
		for _, node := range sitemapBenchLevels {
			report(fmt.Sprintf("levels %s %q %q", scope, text, short(node)), func() error {
				_, err := repo.ListSitemapLevels(ctx, domain.SitemapLevelsQuery{Nodes: []string{node}, Scope: scope, Selection: selection, Limit: domain.MaxSitemapPage})
				return err
			})
		}
		report(fmt.Sprintf("levels %s %q batch of 4", scope, text), func() error {
			_, err := repo.ListSitemapLevels(ctx, domain.SitemapLevelsQuery{Nodes: sitemapBenchLevels, Scope: scope, Selection: selection, Limit: domain.MaxSitemapPage})
			return err
		})
	}
}

// appAllScope is marasi-app's All scope before the batch read: the stored
// page, then up to four query-mode pages for the matching counts.
func appAllScope(ctx context.Context, repo *Repository, node string, selection *domain.SelectionQuery) error {
	page, err := repo.ListSitemapChildren(ctx, domain.SitemapQuery{Node: node, Limit: domain.MaxSitemapPage})
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, child := range page.Children {
		want[child.Name] = true
	}
	cursor := ""
	for range 4 {
		matching, err := repo.ListSitemapChildren(ctx, domain.SitemapQuery{Node: node, Query: selection, Cursor: cursor, Limit: domain.MaxSitemapPage})
		if err != nil {
			return err
		}
		for _, child := range matching.Children {
			delete(want, child.Name)
		}
		if len(want) == 0 || matching.NextCursor == nil {
			return nil
		}
		cursor = *matching.NextCursor
	}
	return nil
}

func short(node string) string {
	if node == "" {
		return "(origins)"
	}
	return strings.TrimPrefix(node, "https://")
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
