//go:build bench1m

package db

// Ledger benchmark cases 4, 5 and 6 (ticket 04): traffic list pages under a
// content-type exclusion. They read the reference project named by
// MARASI_BENCH_PROJECT, built by the ledger fixture generator, and are
// skipped without it. Run them by hand:
//
//	MARASI_BENCH_PROJECT=/path/fixture-1m-seed1.marasi \
//	  go test -tags bench1m -run '^$' -bench LedgerExclusion -benchtime 20x ./db/
//
// Each case is warm: one Repository, one discarded warm-up run, then p50 and
// p95 over b.N runs. A case reports pass 1 when p95 meets its target, and
// fails only when MARASI_BENCH_GATE=1. When ticket 01's harness lands on the
// integration branch, these cases move into it unchanged in shape.

import (
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// benchPageSize is the ledger's page size.
const benchPageSize = 200

func BenchmarkLedgerExclusion(b *testing.B) {
	path := os.Getenv("MARASI_BENCH_PROJECT")
	if path == "" {
		b.Skip("MARASI_BENCH_PROJECT is not set")
	}
	conn, err := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("opening %s: %v", path, err)
	}
	repo := NewProxyRepo(conn)
	b.Cleanup(func() { repo.Close() })

	seed := seedExclusion(b)
	var total int
	if err := conn.Get(&total, `SELECT count(*) FROM request`); err != nil {
		b.Fatal(err)
	}
	// The fixture's excluded stretches: the 1% stretch starts at 85% of the
	// pairs and the 10% stretch at 40%, oldest first. As of the newest pair
	// in a stretch, the newest pairs a page can see are all excluded.
	stretchTop := func(start, length float64) *uuid.UUID {
		var id uuid.UUID
		offset := int(float64(total)*start) + int(float64(total)*length) - 1
		if err := conn.Get(&id, `SELECT id FROM request ORDER BY id LIMIT 1 OFFSET ?`, offset); err != nil {
			b.Fatal(err)
		}
		return &id
	}
	stretchTypes := []string{
		"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml",
		"font/woff2", "font/woff", "text/css", "application/javascript", "application/octet-stream",
	}
	majorForms := []string{"image/*", "font/*", "text/css", "application/javascript", "application/octet-stream"}

	var everyType []string
	counts, err := repo.CountContentTypes()
	if err != nil {
		b.Fatal(err)
	}
	for _, count := range counts {
		if count.ContentType != "" {
			everyType = append(everyType, count.ContentType)
		}
	}

	cases := []struct {
		name   string
		query  domain.TrafficQuery
		target time.Duration
	}{
		// 4. First page with the seed exclusion.
		{"04_seed_exclusion", domain.TrafficQuery{Exclusion: seed}, 30 * time.Millisecond},
		// 5. The newest 10k pairs all excluded, then the newest 100k (P).
		{"05_newest_10k_excluded_exact", domain.TrafficQuery{Exclusion: stretchTypes, AsOf: stretchTop(0.85, 0.01)}, 150 * time.Millisecond},
		{"05_newest_10k_excluded_major", domain.TrafficQuery{Exclusion: majorForms, AsOf: stretchTop(0.85, 0.01)}, 150 * time.Millisecond},
		{"05_newest_100k_excluded_exact_P", domain.TrafficQuery{Exclusion: stretchTypes, AsOf: stretchTop(0.40, 0.10)}, 500 * time.Millisecond},
		{"05_newest_100k_excluded_major_P", domain.TrafficQuery{Exclusion: majorForms, AsOf: stretchTop(0.40, 0.10)}, 500 * time.Millisecond},
		// 6. Every pair excluded (P). Pairs with no content type are never
		// excluded, so status_code >= 0 leaves out the in-flight ones and
		// the result is empty over the whole project.
		{"06_every_pair_excluded_P", domain.TrafficQuery{Text: "status_code >= 0", Exclusion: everyType}, 3 * time.Second},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			q := c.query
			q.Limit = benchPageSize
			run := func() time.Duration {
				started := time.Now()
				if _, err := repo.ListTraffic(q); err != nil {
					b.Fatal(err)
				}
				return time.Since(started)
			}
			run() // warm-up, discarded
			b.ResetTimer()
			latencies := make([]time.Duration, 0, b.N)
			for range b.N {
				latencies = append(latencies, run())
			}
			b.StopTimer()
			reportLatencies(b, latencies, c.target)
		})
	}
}

// seedExclusion returns the content-type exclusion every project was seeded
// with, read from the first migration so it does not depend on where a
// project now keeps its settings.
func seedExclusion(b *testing.B) []string {
	b.Helper()
	raw, err := embedMigrations.ReadFile("migrations/00001_db_setup.sql")
	if err != nil {
		b.Fatal(err)
	}
	match := regexp.MustCompile(`(?s)filters JSON DEFAULT '(\[.*?\])'`).FindSubmatch(raw)
	if match == nil {
		b.Fatal("no seeded filters in the first migration")
	}
	var seed []string
	if err := json.Unmarshal(match[1], &seed); err != nil {
		b.Fatal(err)
	}
	return seed
}

// reportLatencies reports p50, p95 and the target in milliseconds, and pass
// 1 or 0. With MARASI_BENCH_GATE=1 a p95 over the target fails the case.
func reportLatencies(b *testing.B, latencies []time.Duration, target time.Duration) {
	b.Helper()
	slices.Sort(latencies)
	percentile := func(p float64) time.Duration {
		rank := int(math.Ceil(p / 100 * float64(len(latencies))))
		return latencies[max(rank, 1)-1]
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	p95 := percentile(95)
	b.ReportMetric(ms(percentile(50)), "p50-ms")
	b.ReportMetric(ms(p95), "p95-ms")
	b.ReportMetric(ms(target), "target-ms")
	pass := 0.0
	if p95 <= target {
		pass = 1
	}
	b.ReportMetric(pass, "pass")
	if pass == 0 && os.Getenv("MARASI_BENCH_GATE") == "1" {
		b.Errorf("p95 %v is over the target %v", p95, target)
	}
}
