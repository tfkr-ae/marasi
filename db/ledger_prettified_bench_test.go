//go:build bench1m

package db

// Ledger benchmark cases 1, 3 and 13, re-measured for ticket 12: prettified
// copies leave metadata. They read the reference project named by
// MARASI_BENCH_PROJECT, built by the ledger fixture generator, and are
// skipped without it. The project is copied first, because opening it
// migrates it and the prettified move rewrites its metadata. Run them by hand:
//
//	MARASI_BENCH_PROJECT=/path/fixture-1m-seed1.marasi \
//	  go test -tags bench1m -run '^$' -bench LedgerPrettified -benchtime 20x ./db/
//
// Every background job runs to completion before the cases, so they measure
// the steady state. Case 1 is connection-cold: a new connection and
// Repository per run. The others are warm: one Repository. Each case
// discards one warm-up run and reports p50 and p95 over b.N runs. When
// ticket 01's harness lands on the integration branch, these cases move into
// it unchanged in shape.

import (
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

const prettifiedBenchPageSize = 200

func BenchmarkLedgerPrettified(b *testing.B) {
	source := os.Getenv("MARASI_BENCH_PROJECT")
	if source == "" {
		b.Skip("MARASI_BENCH_PROJECT is not set")
	}
	path := copyBenchProject(b, source)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	conn, err := New(path, logger)
	if err != nil {
		b.Fatalf("opening %s: %v", path, err)
	}
	repo := NewProxyRepo(conn)
	b.Cleanup(func() { repo.Close() })
	finishJobs(b, repo)

	var total int
	if err := conn.Get(&total, `SELECT count(*) FROM request`); err != nil {
		b.Fatal(err)
	}
	var deep uuid.UUID
	if err := conn.Get(&deep, `SELECT id FROM request ORDER BY id DESC LIMIT 1 OFFSET ?`, total/2); err != nil {
		b.Fatal(err)
	}

	b.Run("01_open_first_page", func(b *testing.B) {
		measurePrettifiedCase(b, 300*time.Millisecond, func() {
			conn, err := New(path, logger)
			if err != nil {
				b.Fatal(err)
			}
			cold := NewProxyRepo(conn)
			defer cold.Close()
			if _, err := cold.ListTraffic(domain.TrafficQuery{Limit: prettifiedBenchPageSize}); err != nil {
				b.Fatal(err)
			}
		})
	})
	b.Run("03_unfiltered_first_page", func(b *testing.B) {
		measurePrettifiedCase(b, 20*time.Millisecond, func() {
			if _, err := repo.ListTraffic(domain.TrafficQuery{Limit: prettifiedBenchPageSize}); err != nil {
				b.Fatal(err)
			}
		})
	})
	defaultSort, err := parseTrafficOrderBy("")
	if err != nil {
		b.Fatal(err)
	}
	deepCursor := cursorAt(defaultSort, sortedRow{dbRequestResponseSummary: dbRequestResponseSummary{ID: deep}}, nil)
	b.Run("03_unfiltered_deep_cursor", func(b *testing.B) {
		measurePrettifiedCase(b, 20*time.Millisecond, func() {
			if _, err := repo.ListTraffic(domain.TrafficQuery{Cursor: *deepCursor, Limit: prettifiedBenchPageSize}); err != nil {
				b.Fatal(err)
			}
		})
	})
	b.Run("13_metadata_extension_P", func(b *testing.B) {
		measurePrettifiedCase(b, 3*time.Second, func() {
			if _, err := repo.ListTraffic(domain.TrafficQuery{Text: `metadata.extension = "x"`, Limit: prettifiedBenchPageSize}); err != nil {
				b.Fatal(err)
			}
		})
	})
}

// copyBenchProject copies the project at source, with its WAL, into a
// temporary directory and returns the copy's path.
func copyBenchProject(b *testing.B, source string) string {
	b.Helper()
	target := filepath.Join(b.TempDir(), filepath.Base(source))
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(source + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			b.Fatalf("copying the project: %v", err)
		}
		if err := os.WriteFile(target+suffix, data, 0o600); err != nil {
			b.Fatalf("copying the project: %v", err)
		}
	}
	return target
}

// finishJobs runs every incomplete background job to completion and reports
// how long that took.
func finishJobs(b *testing.B, repo *Repository) {
	b.Helper()
	jobs, err := repo.ListJobs()
	if err != nil {
		b.Fatal(err)
	}
	for _, job := range jobs {
		started := time.Now()
		for remaining := !job.Complete; remaining; {
			if _, remaining, err = repo.RunJobBatch(job.Name, 4<<20); err != nil {
				b.Fatalf("running %s: %v", job.Name, err)
			}
		}
		if !job.Complete {
			b.Logf("%s completed in %s", job.Name, time.Since(started).Round(time.Millisecond))
		}
	}
}

// measurePrettifiedCase runs op once as a discarded warm-up, then b.N times,
// and reports p50 and p95 in milliseconds, and pass 1 when p95 meets target.
func measurePrettifiedCase(b *testing.B, target time.Duration, op func()) {
	b.Helper()
	op()
	b.ResetTimer()
	latencies := make([]time.Duration, 0, b.N)
	for range b.N {
		started := time.Now()
		op()
		latencies = append(latencies, time.Since(started))
	}
	b.StopTimer()
	slices.Sort(latencies)
	percentile := func(p float64) time.Duration {
		rank := int(math.Ceil(p / 100 * float64(len(latencies))))
		return latencies[max(rank, 1)-1]
	}
	p95 := percentile(95)
	b.ReportMetric(float64(percentile(50).Microseconds())/1000, "p50_ms")
	b.ReportMetric(float64(p95.Microseconds())/1000, "p95_ms")
	pass := 0.0
	if p95 <= target {
		pass = 1
	}
	b.ReportMetric(pass, "pass")
}
