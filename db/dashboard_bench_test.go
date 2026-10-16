//go:build bench1m

package db

// Ticket 23: the reads marasi-app's Home dashboard makes, cold and warm. It
// reads the project named by MARASI_BENCH_PROJECT and never writes it: each
// round opens an APFS clone (cp -c) of it, whose pages are not in the OS
// cache, so the first call of a round is cold in the way a fresh app start
// is. Run it by hand:
//
//	MARASI_BENCH_PROJECT=/path/project.marasi \
//	  go test -tags bench1m -run '^$' -bench DashboardCounts -benchtime 1x ./db/

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

func BenchmarkDashboardCounts(b *testing.B) {
	source := os.Getenv("MARASI_BENCH_PROJECT")
	if source == "" {
		b.Skip("MARASI_BENCH_PROJECT is not set")
	}
	const rounds, warm = 5, 20
	calls := []struct {
		name string
		call func(repo *Repository) (int, error)
	}{
		{"CountRows", (*Repository).CountRows},
		{"CountNotes", (*Repository).CountNotes},
		{"CountLaunchpads", (*Repository).CountLaunchpads},
		{"CountIntercepted", (*Repository).CountIntercepted},
		{"DashboardCounts", func(repo *Repository) (int, error) {
			counts, err := repo.DashboardCounts()
			return counts.Pairs + counts.Intercepted, err
		}},
		{"GetLogs", func(repo *Repository) (int, error) {
			logs, err := repo.GetLogs()
			return len(logs), err
		}},
	}
	cold := make([][]time.Duration, len(calls))
	hot := make([][]time.Duration, len(calls))
	var opens []time.Duration
	results := make([]int, len(calls))
	for round := 0; round < rounds; round++ {
		dir := b.TempDir()
		clone := filepath.Join(dir, "p.marasi")
		if out, err := exec.Command("cp", "-c", source, clone).CombinedOutput(); err != nil {
			b.Fatalf("cloning: %v %s", err, out)
		}
		started := time.Now()
		conn, err := New(clone, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			b.Fatalf("opening: %v", err)
		}
		repo := NewProxyRepo(conn)
		opens = append(opens, time.Since(started))
		for i, c := range calls {
			started := time.Now()
			n, err := c.call(repo)
			if err != nil {
				b.Fatalf("%s: %v", c.name, err)
			}
			cold[i] = append(cold[i], time.Since(started))
			results[i] = n
		}
		for i, c := range calls {
			for j := 0; j < warm; j++ {
				started := time.Now()
				if _, err := c.call(repo); err != nil {
					b.Fatalf("%s: %v", c.name, err)
				}
				hot[i] = append(hot[i], time.Since(started))
			}
		}
		repo.Close()
		os.RemoveAll(dir)
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
	p := func(ds []time.Duration, q float64) time.Duration {
		s := slices.Clone(ds)
		slices.Sort(s)
		return s[int(q*float64(len(s)-1)+0.5)]
	}
	b.Logf("open: p50 %s ms, max %s ms", ms(p(opens, 0.5)), ms(p(opens, 1)))
	for i, c := range calls {
		b.Logf("%-17s = %-7d cold p50 %7s ms max %7s ms | warm p50 %7s ms p95 %7s ms",
			c.name, results[i], ms(p(cold[i], 0.5)), ms(p(cold[i], 1)), ms(p(hot[i], 0.5)), ms(p(hot[i], 0.95)))
	}
}

// BenchmarkDashboardMigration clones MARASI_BENCH_PROJECT, a project before
// migration 00023, to MARASI_BENCH_OUT and times opening it, which applies
// the migration, and the intercepted_count job that follows. The clone is
// left for BenchmarkDashboardCounts.
func BenchmarkDashboardMigration(b *testing.B) {
	source, out := os.Getenv("MARASI_BENCH_PROJECT"), os.Getenv("MARASI_BENCH_OUT")
	if source == "" || out == "" {
		b.Skip("MARASI_BENCH_PROJECT or MARASI_BENCH_OUT is not set")
	}
	if outBytes, err := exec.Command("cp", "-c", source, out).CombinedOutput(); err != nil {
		b.Fatalf("cloning: %v %s", err, outBytes)
	}
	started := time.Now()
	conn, err := New(out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("opening: %v", err)
	}
	opened := time.Since(started)
	repo := NewProxyRepo(conn)
	defer repo.Close()
	started = time.Now()
	var batches int
	var slowest time.Duration
	for remaining := true; remaining; batches++ {
		batchStarted := time.Now()
		_, remaining, err = repo.RunJobBatch(domain.JobInterceptedCount, 1<<20)
		if err != nil {
			b.Fatalf("RunJobBatch: %v", err)
		}
		slowest = max(slowest, time.Since(batchStarted))
	}
	b.Logf("open with migration: %s; intercepted_count job: %s in %d batches, slowest %s",
		opened.Round(time.Millisecond), time.Since(started).Round(time.Millisecond), batches, slowest.Round(time.Millisecond))
}
