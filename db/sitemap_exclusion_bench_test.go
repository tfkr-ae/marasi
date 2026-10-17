//go:build bench1m

package db

// Ticket 25: query reads with marasi-app's default content-type exclusion.
// It opens the project named by MARASI_BENCH_PROJECT in place, so point it
// at a clone (cp -c), never at a shared fixture. Run it by hand:
//
//	MARASI_BENCH_PROJECT=/path/clone.marasi \
//	  go test -tags bench1m -run '^$' -bench SitemapExclusion -benchtime 1x ./db/

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

// appDefaultExclusion is marasi-app's default content-type exclusion (its
// seededContentTypeExclusion), copied so the bench does not import the app.
var appDefaultExclusion = []string{
	"audio/aac", "image/apng", "image/avif", "video/x-msvideo", "image/bmp",
	"audio/midi", "audio/x-midi", "image/gif", "image/vnd.microsoft.icon",
	"image/jpeg", "audio/mpeg", "video/mp4", "video/mpeg", "audio/ogg",
	"video/ogg", "image/png", "image/svg+xml", "image/tiff", "video/mp2t",
	"audio/wav", "audio/webm", "video/webm", "image/webp", "video/3gpp",
	"audio/3gpp", "video/3gpp2", "audio/3gpp2", "application/vnd.ms-fontobject",
	"font/otf", "font/ttf", "font/woff", "font/woff2", "application/x-abiword",
	"application/vnd.amazon.ebook", "application/x-cdf", "application/x-csh",
	"application/epub+zip", "application/vnd.apple.installer+xml",
	"application/vnd.oasis.opendocument.presentation",
	"application/vnd.oasis.opendocument.spreadsheet",
	"application/vnd.oasis.opendocument.text", "application/vnd.visio",
	"application/vnd.mozilla.xul+xml", "application/javascript",
	"application/octet-stream", "binary/octet-stream", "image/x-icon",
	"image/vnd.djvu", "image/x-portable-pixmap", "image/x-portable-bitmap",
	"audio/basic", "audio/aiff", "audio/x-aiff", "audio/x-wav",
	"audio/x-mpegurl", "audio/x-ms-wma", "video/quicktime", "video/x-flv",
	"video/x-ms-wmv", "application/pdf", "application/msword",
	"application/vnd.ms-excel", "application/vnd.ms-powerpoint",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/rtf", "application/x-rar-compressed",
	"application/x-7z-compressed", "application/zip", "application/x-tar",
	"application/gzip", "application/x-font-ttf", "application/x-font-woff",
	"text/css", "text/calendar", "text/vcard", "application/x-shockwave-flash",
	"application/x-bzip", "application/x-bzip2",
}

func BenchmarkSitemapExclusion(b *testing.B) {
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
	exclusion, err := domain.NormalizeExclusion(appDefaultExclusion)
	if err != nil {
		b.Fatal(err)
	}
	queries := []string{`status_code >= 500`, `host = "app.example.com"`, `method = "POST"`, `son`}
	if only := os.Getenv("MARASI_BENCH_QUERY"); only != "" {
		queries = strings.Split(only, "|")
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
		fmt.Fprintf(os.Stderr, "%-70s p50 %8.2fms p95 %8.2fms\n", name, ms(times[len(times)/2]), ms(times[len(times)*95/100]))
	}
	var middle uuid.UUID
	if err := repo.dbConn.Get(&middle, `SELECT id FROM request ORDER BY id LIMIT 1 OFFSET (SELECT count(*) / 2 FROM request)`); err != nil {
		b.Fatal(err)
	}
	wildcards := append(slices.Clone(exclusion), "image/*", "font/*")
	for _, text := range queries {
		for _, excl := range [][]string{exclusion, wildcards} {
			for _, bound := range []uuid.UUID{middle, asOf} {
				sameAsPlainCondition(b, repo, text, excl, bound)
			}
		}
	}
	report("levels all, exclusion only, batch of 4", func() error {
		_, err := repo.ListSitemapLevels(ctx, domain.SitemapLevelsQuery{Nodes: sitemapBenchLevels, Scope: domain.SitemapScopeAll, Exclusion: exclusion, Limit: domain.MaxSitemapPage})
		return err
	})
	for _, text := range queries {
		for _, excl := range [][]string{nil, exclusion} {
			label := "no exclusion"
			if excl != nil {
				label = fmt.Sprintf("%d-type exclusion", len(excl))
			}
			selection := &domain.SelectionQuery{Text: text, Exclusion: excl, AsOf: asOf}
			for _, scope := range []domain.SitemapScope{domain.SitemapScopeMatching, domain.SitemapScopeAll} {
				report(fmt.Sprintf("levels %s %q %s batch of 4", scope, text, label), func() error {
					_, err := repo.ListSitemapLevels(ctx, domain.SitemapLevelsQuery{Nodes: sitemapBenchLevels, Scope: scope, Exclusion: excl, Selection: selection, Limit: domain.MaxSitemapPage})
					return err
				})
			}
			report(fmt.Sprintf("children query %q %s %q", text, label, "api"), func() error {
				_, err := repo.ListSitemapChildren(ctx, domain.SitemapQuery{Node: sitemapBenchLevels[2], Query: selection, Limit: domain.MaxSitemapPage})
				return err
			})
			report(fmt.Sprintf("CountTraffic %q %s", text, label), func() error {
				_, err := repo.CountTraffic(domain.TrafficCountQuery{Text: text, Exclusion: excl, AsOf: &asOf})
				return err
			})
			report(fmt.Sprintf("ListTraffic first page %q %s", text, label), func() error {
				_, err := repo.ListTraffic(domain.TrafficQuery{Text: text, Exclusion: excl, AsOf: &asOf, Limit: 100})
				return err
			})
		}
	}
}

// sameAsPlainCondition checks that the sitemap leaves and the count of text
// with exclusion, up to asOf, are those of the exclusion as a plain
// condition on content_type, as it was before ticket 25.
func sameAsPlainCondition(b *testing.B, repo *Repository, text string, exclusion []string, asOf uuid.UUID) {
	b.Helper()
	match, err := newTrafficMatch(text, exclusion)
	if err != nil {
		b.Fatal(err)
	}
	match.add(`id <= ?`, asOf)
	translated, err := translateTrafficQuery(text, nil)
	if err != nil {
		b.Fatal(err)
	}
	plain := trafficMatch{}
	if translated.where != "" {
		plain.add(translated.where, translated.args...)
	}
	where, args := exclusionCondition(exclusion)
	plain.add(where, args...)
	plain.add(`id <= ?`, asOf)

	tx, err := repo.dbConn.Beginx()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	var leaves [2][]matchedLeaf
	var counts [2]int
	for i, m := range []trafficMatch{match, plain} {
		if leaves[i], err = matchedLeaves(context.Background(), tx, m); err != nil {
			b.Fatal(err)
		}
		slices.SortFunc(leaves[i], func(x, y matchedLeaf) int { return strings.Compare(x.Name, y.Name) })
		if err := tx.Get(&counts[i], trafficCountSQL(m), append(slices.Clone(m.args), 1_000_000)...); err != nil {
			b.Fatal(err)
		}
	}
	if !slices.Equal(leaves[0], leaves[1]) || counts[0] != counts[1] {
		b.Fatalf("%q excluding %d types up to %s: %d pairs in %d leaves, want %d in %d", text, len(exclusion), asOf, counts[0], len(leaves[0]), counts[1], len(leaves[1]))
	}
	fmt.Fprintf(os.Stderr, "same as the plain condition: %q, %d types, as_of %s: %d pairs in %d leaves\n", text, len(exclusion), asOf, counts[0], len(leaves[0]))
}
