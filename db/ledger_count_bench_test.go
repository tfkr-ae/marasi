//go:build bench1m

package db

// Ledger benchmark cases 19 and 33 (ticket 17): the capped traffic count and
// the count of new matches since a snapshot. Like cases 4 to 6, they read the
// reference project named by MARASI_BENCH_PROJECT and are skipped without it.
// They also read the fixture's manifest (path + ".manifest.json"). Run them
// by hand:
//
//	MARASI_BENCH_PROJECT=/path/fixture-1m-seed1.marasi \
//	  go test -tags bench1m -run '^$' -bench LedgerCount -benchtime 20x ./db/
//
// When ticket 01's harness lands on the integration branch, these cases move
// into it unchanged in shape.

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// countManifest is the part of the fixture manifest the count cases read.
type countManifest struct {
	SparseHostPattern string `json:"sparse_host_pattern"`
	CommonTerm        string `json:"common_term"`
}

func BenchmarkLedgerCount(b *testing.B) {
	path := os.Getenv("MARASI_BENCH_PROJECT")
	if path == "" {
		b.Skip("MARASI_BENCH_PROJECT is not set")
	}
	raw, err := os.ReadFile(path + ".manifest.json")
	if err != nil {
		b.Fatalf("reading the fixture manifest: %v", err)
	}
	var manifest countManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		b.Fatalf("reading the fixture manifest: %v", err)
	}
	conn, err := New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("opening %s: %v", path, err)
	}
	repo := NewProxyRepo(conn)
	b.Cleanup(func() { repo.Close() })

	// A dense 10%: the host whose share of pairs is closest to a tenth.
	var denseHost string
	if err := conn.Get(&denseHost, `SELECT host FROM request GROUP BY host
		ORDER BY abs(count(*) - (SELECT count(*) FROM request) / 10) LIMIT 1`); err != nil {
		b.Fatal(err)
	}
	// A snapshot with 1,000 pairs newer than it: the newest pairs a client
	// has not listed yet while traffic is in flight.
	var snapshot uuid.UUID
	if err := conn.Get(&snapshot, `SELECT id FROM request ORDER BY id DESC LIMIT 1 OFFSET 1000`); err != nil {
		b.Fatal(err)
	}
	seed := seedExclusion(b)

	const indexDriven, pathological = 300 * time.Millisecond, time.Second
	cases := []struct {
		name   string
		query  domain.TrafficCountQuery
		target time.Duration
	}{
		// 19. The capped count. The unfiltered count walks the primary key,
		// and text uses the traffic index. Field conditions scan the table
		// until their indexes land (tickets 03 and 07), so they are (P).
		{"19_unfiltered", domain.TrafficCountQuery{}, indexDriven},
		{"19_unfiltered_seed_exclusion_P", domain.TrafficCountQuery{Exclusion: seed}, pathological},
		{"19_dense_10pct_host_P", domain.TrafficCountQuery{Text: fmt.Sprintf("host = %q", denseHost)}, pathological},
		{"19_sparse_host_P", domain.TrafficCountQuery{Text: fmt.Sprintf("host = %q", manifest.SparseHostPattern)}, pathological},
		{"19_common_text", domain.TrafficCountQuery{Text: manifest.CommonTerm}, indexDriven},
		// 33. New matches since a snapshot: a short range read on id.
		{"33_new_matches", domain.TrafficCountQuery{After: &snapshot}, 50 * time.Millisecond},
		{"33_new_matches_seed_exclusion", domain.TrafficCountQuery{Exclusion: seed, After: &snapshot}, 50 * time.Millisecond},
		{"33_new_matches_dense_host", domain.TrafficCountQuery{Text: fmt.Sprintf("host = %q", denseHost), Exclusion: seed, After: &snapshot}, 50 * time.Millisecond},
		{"33_new_matches_common_text", domain.TrafficCountQuery{Text: manifest.CommonTerm, Exclusion: seed, After: &snapshot}, 50 * time.Millisecond},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			run := func() time.Duration {
				started := time.Now()
				if _, err := repo.CountTraffic(c.query); err != nil {
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
