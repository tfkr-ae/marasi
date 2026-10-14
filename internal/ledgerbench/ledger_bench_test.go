//go:build bench1m

// The ledger benchmarks measure the targets of the ledger-redesign-support
// spec against a reference project from the fixture package. They are a
// manual release step, behind the bench1m build tag; CI never runs them.
//
// # Running
//
// Build a fixture once, outside the repository:
//
//	go run ./internal/ledgerbench/cmd/ledgerfixture -pairs 1000000 -seed 1 -out ~/fixtures/ledger-1m.marasi
//
// Then run every case, or narrow with -bench:
//
//	MARASI_BENCH_FIXTURE=~/fixtures/ledger-1m.marasi \
//	  go test -tags bench1m -run '^$' -bench . -benchtime 30x -timeout 2h ./internal/ledgerbench/
//
// Each case reports p50-ms and p95-ms; p95 is what passes or fails against
// target-ms, and pass is 1 or 0. Set MARASI_BENCH_GATE=1 to make a miss fail
// the run. The first benchmark prints the run environment.
//
// OS-cold runs (case 28) purge the OS cache before every run with
// `sudo -n purge`, so they need passwordless sudo for purge and are skipped
// unless MARASI_BENCH_OS_COLD=1. They are reported, never gated.
//
// # Policy
//
// Cases measure through the Repository, and where a route exists through
// the service handler too, never raw SQL, so row scanning, UUID and time
// parsing, metadata decoding and the completeness check all count. Pages
// are 200 pairs.
//
//   - Warm (ledgerbench.Measure): one Repository for every run; the first
//     run is discarded.
//   - Connection-cold (ledgerbench.MeasureCold): a new sql.DB and Repository
//     on every run, with the OS cache warm. The mode for opening a project
//     and the first query.
//   - Write cases run on ledgerbench.CloneProject copies, never the fixture.
//
// # Adding a case
//
// A ticket that makes a case runnable adds it here, named with its number in
// the ticket 01 list so -bench can pick it out:
//
//   - A query case is one more queryCase in queryCases: the query (built
//     from the manifest's planted facts), an optional cursor, and its
//     target. It runs warm through the Repository (repo) and GET /traffic
//     (service).
//   - Any other case is a sub-benchmark of the Benchmark function for its
//     area, or a new Benchmark function: get the project with
//     benchProject(b), open what it needs, and call Measure or MeasureCold
//     with the case's ledgerbench.Target. Mark (P) cases Pathological.
//   - When the case needs a fact the fixture does not plant yet, add it to
//     fixture.Manifest and plant it in the generator.
//
// When the ticket changes ListTraffic's signature, update listPage, the one
// place the cases call it.
//
// Then record the case's baseline in the spec's benchmarks.md.
package ledgerbench_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/internal/ledgerbench"
	"github.com/tfkr-ae/marasi/internal/ledgerbench/fixture"
	"github.com/tfkr-ae/marasi/service"
)

const (
	fixtureEnv = "MARASI_BENCH_FIXTURE"
	osColdEnv  = "MARASI_BENCH_OS_COLD"
	pageSize   = 200
)

// project is the fixture a run measures.
type project struct {
	path     string
	manifest *fixture.Manifest
	// exclusion is the seeded content-type exclusion written as a query.
	// Until ticket 04 puts the exclusion in SQL, cases express it this way.
	exclusion string
	// unindexed is a copy with its traffic index emptied, standing in for
	// a project just upgraded whose background jobs have not run.
	unindexed string
}

var (
	projectOnce sync.Once
	theProject  *project
	projectErr  error
)

// benchProject returns the fixture named by MARASI_BENCH_FIXTURE, and prints
// the run environment the first time.
func benchProject(b *testing.B) *project {
	b.Helper()
	path := os.Getenv(fixtureEnv)
	if path == "" {
		b.Skipf("set %s to a fixture built by ledgerfixture", fixtureEnv)
	}
	projectOnce.Do(func() { theProject, projectErr = loadProject(path) })
	if projectErr != nil {
		b.Fatal(projectErr)
	}
	return theProject
}

func loadProject(path string) (*project, error) {
	env, err := ledgerbench.ProbeEnvironment(path)
	if err != nil {
		return nil, err
	}
	manifest, err := fixture.ReadManifest(path)
	if err != nil {
		return nil, err
	}
	fmt.Printf("fixture: %s (%d pairs, seed %d)\n%s", path, manifest.Pairs, manifest.Seed, env)

	var terms []string
	for _, filter := range slices.Compact(slices.Sorted(slices.Values(fixture.SeedExclusion))) {
		terms = append(terms, fmt.Sprintf("content_type != %q", filter))
	}
	return &project{path: path, manifest: manifest, exclusion: strings.Join(terms, " AND ")}, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// unindexedProject returns a copy of the fixture whose traffic index is
// empty, made once per run.
var unindexedOnce sync.Once

func unindexedProject(b *testing.B, p *project) string {
	b.Helper()
	unindexedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ledgerbench-unindexed-")
		if err != nil {
			projectErr = err
			return
		}
		clone, err := ledgerbench.CloneProject(p.path, dir)
		if err != nil {
			projectErr = err
			return
		}
		conn, err := db.New(clone, quiet())
		if err != nil {
			projectErr = err
			return
		}
		defer conn.Close()
		_, err = conn.Exec(`DELETE FROM traffic_index_key; INSERT INTO traffic_index(traffic_index) VALUES('delete-all')`)
		if err != nil {
			projectErr = fmt.Errorf("emptying the traffic index: %w", err)
			return
		}
		p.unindexed = clone
	})
	if projectErr != nil {
		b.Fatal(projectErr)
	}
	return p.unindexed
}

// cloneOf returns a throwaway copy of the project at path, removed when b ends.
func cloneOf(b *testing.B, path string) string {
	b.Helper()
	dir := b.TempDir()
	clone, err := ledgerbench.CloneProject(path, dir)
	if err != nil {
		b.Fatal(err)
	}
	return clone
}

func openRepo(b *testing.B, path string) *db.Repository {
	b.Helper()
	conn, err := db.New(path, quiet())
	if err != nil {
		b.Fatal(err)
	}
	repo := db.NewProxyRepo(conn)
	b.Cleanup(func() { repo.Close() })
	return repo
}

// listPage reads one page, the way every case lists traffic.
func listPage(repo *db.Repository, cursor *uuid.UUID, query string) (complete bool, err error) {
	_, _, complete, err = repo.ListTraffic(cursor, pageSize, query)
	return complete, err
}

// queryCase is one warm query benchmark.
type queryCase struct {
	name   string
	query  func(p *project) string
	cursor func(p *project) *uuid.UUID
	target ledgerbench.Target
}

func fixed(query string) func(*project) string { return func(*project) string { return query } }

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// queryCases are the query cases today's code can run. Numbers are the
// ticket 01 list.
var queryCases = []queryCase{
	{name: "03-unfiltered", query: fixed(""), target: ledgerbench.Target{Max: ms(20)}},
	{name: "03-deep-cursor", query: fixed(""), cursor: func(p *project) *uuid.UUID { return &p.manifest.MiddleID },
		target: ledgerbench.Target{Max: ms(20)}},
	{name: "04-seed-exclusion", query: func(p *project) string { return p.exclusion }, target: ledgerbench.Target{Max: ms(30)}},
	{name: "07-sparse-host-spread", query: func(p *project) string { return fmt.Sprintf("host = %q", p.manifest.SpreadHost) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "08-sparse-host-old", query: func(p *project) string { return fmt.Sprintf("host = %q", p.manifest.OldHost) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "08-sparse-host-old-under-a-page", query: func(p *project) string { return fmt.Sprintf("host = %q", p.manifest.RareOldHost) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "09-path-wildcard-dense", query: func(p *project) string { return fmt.Sprintf("path = %q", p.manifest.GraphQLPattern) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "09-host-wildcard-dense", query: func(p *project) string { return fmt.Sprintf("host = %q", p.manifest.DenseHostPattern) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "09-host-negation-dense", query: func(p *project) string { return fmt.Sprintf("host != %q", p.manifest.DominantHost) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "09-host-wildcard-sparse", query: func(p *project) string { return fmt.Sprintf("host = %q", p.manifest.SparseHostPattern) },
		target: ledgerbench.Target{Max: 3 * time.Second, Pathological: true}},
	{name: "10-server-errors", query: fixed("status_code >= 500"), target: ledgerbench.Target{Max: ms(150)}},
	{name: "12-launchpad-big", query: func(p *project) string { return fmt.Sprintf("launchpad = %q", p.manifest.BigLaunchpad) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "12-launchpad-old", query: func(p *project) string { return fmt.Sprintf("launchpad = %q", p.manifest.OldLaunchpad) },
		target: ledgerbench.Target{Max: ms(150)}},
	{name: "12-launchpad-any", query: fixed("launchpad:*"), target: ledgerbench.Target{Max: ms(150)}},
	{name: "13-metadata-extension", query: func(p *project) string { return fmt.Sprintf("metadata.extension = %q", p.manifest.RareExtension) },
		target: ledgerbench.Target{Max: 3 * time.Second, Pathological: true}},
	{name: "15-rare-text", query: func(p *project) string { return fmt.Sprintf("%q", p.manifest.RareTerm) },
		target: ledgerbench.Target{Max: ms(50)}},
	{name: "16-common-text-term", query: func(p *project) string { return p.manifest.CommonTerm },
		target: ledgerbench.Target{Max: ms(500), Pathological: true}},
	{name: "16-common-text-phrase", query: func(p *project) string { return fmt.Sprintf("response_head:%q", p.manifest.CommonPhrase) },
		target: ledgerbench.Target{Max: ms(500), Pathological: true}},
	{name: "17-text-and-sparse-host", query: func(p *project) string {
		return fmt.Sprintf("%s AND host = %q", p.manifest.CommonTerm, p.manifest.OldHost)
	}, target: ledgerbench.Target{Max: time.Second, Pathological: true}},
	{name: "17-not-text", query: func(p *project) string { return "NOT " + p.manifest.CommonTerm },
		target: ledgerbench.Target{Max: time.Second, Pathological: true}},
	{name: "17-text-and-exclusion", query: func(p *project) string { return p.manifest.CommonTerm + " AND " + p.exclusion },
		target: ledgerbench.Target{Max: time.Second, Pathological: true}},
	// 3 s (P) until ticket 07 makes OR index-driven; then 150 ms.
	{name: "18-host-or-note", query: func(p *project) string {
		return fmt.Sprintf("host = %q OR note:%q", p.manifest.RareOldHost, p.manifest.RareNoteTerm)
	}, target: ledgerbench.Target{Max: 3 * time.Second, Pathological: true}},
}

// BenchmarkQuery runs every query case warm, through the Repository and
// through GET /traffic.
func BenchmarkQuery(b *testing.B) {
	p := benchProject(b)
	repo := openRepo(b, p.path)
	server := service.NewServer(&marasi.Proxy{TrafficRepo: repo}, nil, nil, func() {}, "bench", "bench", p.path)
	for _, c := range queryCases {
		query := c.query(p)
		var cursor *uuid.UUID
		if c.cursor != nil {
			cursor = c.cursor(p)
		}
		b.Run(c.name+"/repo", func(b *testing.B) {
			ledgerbench.Measure(b, c.target, func() error {
				_, err := listPage(repo, cursor, query)
				return err
			})
		})
		b.Run(c.name+"/service", func(b *testing.B) {
			target := "/traffic?limit=" + fmt.Sprint(pageSize) + "&q=" + url.QueryEscape(query)
			if cursor != nil {
				target += "&cursor=" + cursor.String()
			}
			ledgerbench.Measure(b, c.target, func() error {
				response := httptest.NewRecorder()
				server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
				if response.Code != http.StatusOK {
					return fmt.Errorf("GET %s: %d %s", target, response.Code, response.Body)
				}
				return nil
			})
		})
	}
}

// BenchmarkOpen measures opening a project and showing its first page,
// connection-cold.
func BenchmarkOpen(b *testing.B) {
	p := benchProject(b)
	openAndList := func(path, query string) func() (func(), error) {
		return func() (func(), error) {
			conn, err := db.New(path, quiet())
			if err != nil {
				return nil, err
			}
			repo := db.NewProxyRepo(conn)
			_, err = listPage(repo, nil, query)
			return func() { repo.Close() }, err
		}
	}
	// Case 1. Fails today: a fresh Repository scans every pair to check the
	// index is complete. Ticket 02 fixes it.
	b.Run("01-steady-state", func(b *testing.B) {
		ledgerbench.MeasureCold(b, ledgerbench.Target{Max: ms(300)}, openAndList(p.path, p.exclusion))
	})
	// Case 2. Migrations are already applied; the traffic index, today's
	// only background job, is empty.
	b.Run("02-after-upgrade", func(b *testing.B) {
		ledgerbench.MeasureCold(b, ledgerbench.Target{Max: ms(500), Pathological: true},
			openAndList(unindexedProject(b, p), p.exclusion))
	})
	// Case 3 and 4, connection-cold: the first query of a session.
	b.Run("03-unfiltered-cold", func(b *testing.B) {
		ledgerbench.MeasureCold(b, ledgerbench.Target{Max: ms(300)}, openAndList(p.path, ""))
	})
}

// BenchmarkCountContentTypes is case 20. Ticket 16 moves it to the sitemap.
func BenchmarkCountContentTypes(b *testing.B) {
	p := benchProject(b)
	repo := openRepo(b, p.path)
	b.Run("20-count-content-types", func(b *testing.B) {
		ledgerbench.Measure(b, ledgerbench.Target{Max: ms(150)}, func() error {
			_, err := repo.CountContentTypes()
			return err
		})
	})
}

// BenchmarkWrite is case 22: the cost of capturing one pair, InsertRequest
// then InsertResponse, into a copy of the fixture. It has no absolute
// target: tickets 03, 14 and 20 may add at most 25% to the baseline.
func BenchmarkWrite(b *testing.B) {
	p := benchProject(b)
	b.Run("22-capture-pair", func(b *testing.B) {
		repo := openRepo(b, cloneOf(b, p.path))
		pairs := fixture.NewPairs(p.manifest.Seed+22, b.N+1, p.manifest.Newest, 0)
		next := 0
		ledgerbench.Measure(b, ledgerbench.Target{}, func() error {
			pair := pairs[next]
			next++
			if err := repo.InsertRequest(pair.Request); err != nil {
				return err
			}
			if pair.Response == nil {
				return nil
			}
			return repo.InsertResponse(pair.Response)
		})
	})
}

// BenchmarkUnderLoad is case 26: the first page while capture runs at 100
// pairs a second with 50 KB bodies and the index build runs, on a copy whose
// index is empty. It also reports the longest capture stall.
// Ticket 09 adds the variant under a bulk highlight.
func BenchmarkUnderLoad(b *testing.B) {
	p := benchProject(b)
	b.Run("26-capture-and-index-build", func(b *testing.B) {
		repo := openRepo(b, cloneOf(b, unindexedProject(b, p)))
		ctx, stop := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() { buildIndex(ctx, repo) })
		var stall time.Duration
		wg.Go(func() { stall = capture(ctx, repo, p.manifest, 100, 50_000) })

		ledgerbench.Measure(b, ledgerbench.Target{Max: ms(300)}, func() error {
			_, err := listPage(repo, nil, p.exclusion)
			return err
		})
		stop()
		wg.Wait()
		b.ReportMetric(float64(stall)/float64(time.Millisecond), "capture-stall-ms")
	})
}

// BenchmarkIncompleteIndex is case 27: a text query while the index build
// runs. The target is 150 ms plus the wait for the batch in flight.
func BenchmarkIncompleteIndex(b *testing.B) {
	p := benchProject(b)
	b.Run("27-text-while-indexing", func(b *testing.B) {
		repo := openRepo(b, cloneOf(b, unindexedProject(b, p)))
		// Time a few batches first, to know the wait for one.
		var batches ledgerbench.Latencies
		for range 5 {
			started := time.Now()
			if _, _, err := repo.IndexMissingTraffic(1 << 20); err != nil {
				b.Fatal(err)
			}
			batches = append(batches, time.Since(started))
		}
		ctx, stop := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() { buildIndex(ctx, repo) })
		query := fmt.Sprintf("%q", p.manifest.RareTerm)
		sawIncomplete := false
		ledgerbench.Measure(b, ledgerbench.Target{Max: ms(150) + batches.Percentile(95)}, func() error {
			complete, err := listPage(repo, nil, query)
			sawIncomplete = sawIncomplete || !complete
			return err
		})
		stop()
		wg.Wait()
		if !sawIncomplete {
			b.Error("index.complete was never false while the index build ran")
		}
		b.ReportMetric(float64(batches.Percentile(95))/float64(time.Millisecond), "batch-p95-ms")
	})
}

// BenchmarkOSCold is case 28: cases 1, 4, 7 and 15 with the OS cache purged
// before every run. Manual and report-only.
func BenchmarkOSCold(b *testing.B) {
	p := benchProject(b)
	if os.Getenv(osColdEnv) != "1" {
		b.Skipf("set %s=1, with passwordless sudo for purge, to run OS-cold cases", osColdEnv)
	}
	purge := func() error { return exec.Command("sudo", "-n", "purge").Run() }
	queries := map[string]string{
		"01-open-steady-state":  p.exclusion,
		"04-seed-exclusion":     p.exclusion,
		"07-sparse-host-spread": fmt.Sprintf("host = %q", p.manifest.SpreadHost),
		"15-rare-text":          fmt.Sprintf("%q", p.manifest.RareTerm),
	}
	targets := map[string]time.Duration{
		"01-open-steady-state": ms(300), "04-seed-exclusion": ms(30), "07-sparse-host-spread": ms(150), "15-rare-text": ms(50),
	}
	for _, name := range slices.Sorted(maps.Keys(queries)) {
		b.Run("28-"+name, func(b *testing.B) {
			// The OS-cold target is 3 to 5 times looser; report against 5.
			target := ledgerbench.Target{Max: 5 * targets[name], ReportOnly: true}
			ledgerbench.MeasurePrepared(b, target, purge, func() (func(), error) {
				conn, err := db.New(p.path, quiet())
				if err != nil {
					return nil, err
				}
				repo := db.NewProxyRepo(conn)
				_, err = listPage(repo, nil, queries[name])
				return func() { repo.Close() }, err
			})
		})
	}
}

// buildIndex runs the background index build until it finishes or ctx ends,
// as the service does.
func buildIndex(ctx context.Context, repo *db.Repository) {
	for ctx.Err() == nil {
		_, remaining, err := repo.IndexMissingTraffic(1 << 20)
		if err == nil && !remaining {
			return
		}
	}
}

// capture writes perSecond new pairs a second with bodies of bodyBytes until
// ctx ends, and returns the longest single write.
func capture(ctx context.Context, repo *db.Repository, manifest *fixture.Manifest, perSecond, bodyBytes int) time.Duration {
	var longest time.Duration
	after := manifest.Newest
	tick := time.NewTicker(time.Second / time.Duration(perSecond))
	defer tick.Stop()
	for batch := uint64(0); ; batch++ {
		pairs := fixture.NewPairs(manifest.Seed+1000+batch, perSecond, after, bodyBytes)
		after = pairs[len(pairs)-1].Request.RequestedAt
		for _, pair := range pairs {
			select {
			case <-ctx.Done():
				return longest
			case <-tick.C:
			}
			for _, write := range []func() error{
				func() error { return repo.InsertRequest(pair.Request) },
				func() error {
					if pair.Response == nil {
						return nil
					}
					return repo.InsertResponse(pair.Response)
				},
			} {
				started := time.Now()
				if err := write(); err != nil {
					return longest
				}
				longest = max(longest, time.Since(started))
			}
		}
	}
}
