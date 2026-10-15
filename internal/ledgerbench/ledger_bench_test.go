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
//	  go test -tags bench1m -run '^$' -bench . -benchtime 30x -timeout 3h ./internal/ledgerbench/
//
// The fixture is opened with db.New, so a fixture built by an older binary
// is migrated in place on the first run. Rebuild it after a schema change
// for a clean steady-state baseline.
//
// Each case reports p50-ms and p95-ms; p95 is what passes or fails against
// target-ms, and pass is 1 or 0. Set MARASI_BENCH_GATE=1 to make a miss fail
// the run. A case stops sampling after MARASI_BENCH_CASE_BUDGET (default 2m)
// with at least 5 samples, so a slow case reports fewer samples than 30;
// samples says how many. The first benchmark prints the run environment.
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
//   - A query case is one more queryCase in queryCases: a domain.TrafficQuery
//     built from the manifest's planted facts, and its target. It runs warm
//     through the Repository (repo) and GET /traffic (service).
//   - Any other case is a sub-benchmark of the Benchmark function for its
//     area, or a new Benchmark function: get the project with
//     benchProject(b), open what it needs, and call Measure or MeasureCold
//     with the case's ledgerbench.Target. Mark (P) cases Pathological.
//   - When the case needs a fact the fixture does not plant yet, add it to
//     fixture.Manifest and plant it in the generator.
//
// Every case lists traffic through listPage and trafficURL; a change to
// ListTraffic or GET /traffic changes those two.
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

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/ledgerbench"
	"github.com/tfkr-ae/marasi/internal/ledgerbench/fixture"
	"github.com/tfkr-ae/marasi/service"
)

const (
	fixtureEnv = "MARASI_BENCH_FIXTURE"
	osColdEnv  = "MARASI_BENCH_OS_COLD"
	pageSize   = 200
)

// project is the fixture a run measures, with facts read from it once.
type project struct {
	path     string
	manifest *fixture.Manifest
	// deepCursor continues a list from the pair halfway back.
	deepCursor string
	// contentTypes is every content type the project has, for excluding
	// every pair.
	contentTypes []string
	// unindexed is a copy whose traffic index job was rebuilt and has not
	// run, standing in for a project just upgraded.
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

	conn, err := db.New(path, quiet())
	if err != nil {
		return nil, err
	}
	repo := db.NewProxyRepo(conn)
	defer repo.Close()
	p := &project{path: path, manifest: manifest}
	middle := manifest.MiddleID
	page, err := repo.ListTraffic(domain.TrafficQuery{AsOf: &middle, Limit: 1})
	if err != nil {
		return nil, err
	}
	if page.NextCursor == nil {
		return nil, fmt.Errorf("no pairs older than the middle pair %s", middle)
	}
	p.deepCursor = *page.NextCursor
	counts, err := repo.CountContentTypes()
	if err != nil {
		return nil, err
	}
	for _, count := range counts.Counts {
		if count.ContentType != "" {
			p.contentTypes = append(p.contentTypes, count.ContentType)
		}
	}
	return p, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var unindexedOnce sync.Once

// unindexedProject returns a copy of the fixture whose traffic index job has
// been rebuilt and not run again, made once per run.
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
		repo := db.NewProxyRepo(conn)
		defer repo.Close()
		if err := repo.RebuildJob(domain.JobTrafficIndex); err != nil {
			projectErr = fmt.Errorf("rebuilding the traffic index: %w", err)
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
	clone, err := ledgerbench.CloneProject(path, b.TempDir())
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

// listPage reads one page of q, the way every case lists traffic, and
// reports whether the traffic index was complete.
func listPage(repo *db.Repository, q domain.TrafficQuery) (complete bool, err error) {
	q.Limit = pageSize
	page, err := repo.ListTraffic(q)
	return page.Complete[domain.JobTrafficIndex], err
}

// trafficURL is the GET /traffic request for q.
func trafficURL(q domain.TrafficQuery) string {
	values := url.Values{"limit": {fmt.Sprint(pageSize)}}
	if q.Text != "" {
		values.Set("q", q.Text)
	}
	for _, excluded := range q.Exclusion {
		values.Add("exclude", excluded)
	}
	if q.Cursor != "" {
		values.Set("cursor", q.Cursor)
	}
	if q.AsOf != nil {
		values.Set("as_of", q.AsOf.String())
	}
	if q.OrderBy != "" {
		values.Set("order_by", q.OrderBy)
	}
	if q.Direction == domain.Backward {
		values.Set("direction", "backward")
	}
	return "/traffic?" + values.Encode()
}

// queryCase is one warm list benchmark.
type queryCase struct {
	name   string
	query  func(p *project) domain.TrafficQuery
	target ledgerbench.Target
}

func text(format string, facts ...func(m *fixture.Manifest) any) func(*project) domain.TrafficQuery {
	return func(p *project) domain.TrafficQuery {
		args := make([]any, len(facts))
		for i, fact := range facts {
			args[i] = fact(p.manifest)
		}
		return domain.TrafficQuery{Text: fmt.Sprintf(format, args...)}
	}
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// stretchTypes are the content types of the fixture's excluded stretches,
// exact and in major/* form.
var (
	stretchTypes = []string{
		"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml",
		"font/woff2", "font/woff", "text/css", "application/javascript", "application/octet-stream",
	}
	stretchMajorForms = []string{"image/*", "font/*", "text/css", "application/javascript", "application/octet-stream"}
)

// excludedStretch reads as of the newest pair of the excluded stretch with
// the given number of pairs, so the newest pairs a page can see are all
// excluded.
func excludedStretch(fraction int, exclusion []string) func(*project) domain.TrafficQuery {
	return func(p *project) domain.TrafficQuery {
		stretch := p.manifest.ExcludedStretches[0]
		for _, s := range p.manifest.ExcludedStretches {
			if s.Pairs == p.manifest.Pairs/fraction {
				stretch = s
			}
		}
		newest := stretch.Newest
		return domain.TrafficQuery{Exclusion: exclusion, AsOf: &newest}
	}
}

// queryCases are the list cases the code can run. Numbers are the ticket 01
// list.
var queryCases = []queryCase{
	{"03-unfiltered", text(""), ledgerbench.Target{Max: ms(20)}},
	{"03-deep-cursor", func(p *project) domain.TrafficQuery { return domain.TrafficQuery{Cursor: p.deepCursor} },
		ledgerbench.Target{Max: ms(20)}},
	{"04-seed-exclusion", func(*project) domain.TrafficQuery { return domain.TrafficQuery{Exclusion: fixture.SeedExclusion} },
		ledgerbench.Target{Max: ms(30)}},
	// Case 5: the newest 1% (10k at 1M) and 10% (100k at 1M) all excluded.
	{"05-newest-1pct-excluded-exact", excludedStretch(100, stretchTypes), ledgerbench.Target{Max: ms(150)}},
	{"05-newest-1pct-excluded-major", excludedStretch(100, stretchMajorForms), ledgerbench.Target{Max: ms(150)}},
	{"05-newest-10pct-excluded-exact", excludedStretch(10, stretchTypes), ledgerbench.Target{Max: ms(500), Pathological: true}},
	{"05-newest-10pct-excluded-major", excludedStretch(10, stretchMajorForms), ledgerbench.Target{Max: ms(500), Pathological: true}},
	// Case 6: pairs with no content type are never excluded, so
	// status_code >= 0 leaves out the in-flight ones and the result is empty.
	{"06-every-pair-excluded", func(p *project) domain.TrafficQuery {
		return domain.TrafficQuery{Text: "status_code >= 0", Exclusion: p.contentTypes}
	}, ledgerbench.Target{Max: 3 * time.Second, Pathological: true}},
	{"07-sparse-host-spread", text("host = %q", func(m *fixture.Manifest) any { return m.SpreadHost }), ledgerbench.Target{Max: ms(150)}},
	{"08-sparse-host-old", text("host = %q", func(m *fixture.Manifest) any { return m.OldHost }), ledgerbench.Target{Max: ms(150)}},
	{"08-sparse-host-old-under-a-page", text("host = %q", func(m *fixture.Manifest) any { return m.RareOldHost }),
		ledgerbench.Target{Max: ms(150)}},
	{"09-path-wildcard-dense", text("path = %q", func(m *fixture.Manifest) any { return m.GraphQLPattern }), ledgerbench.Target{Max: ms(150)}},
	{"09-host-wildcard-dense", text("host = %q", func(m *fixture.Manifest) any { return m.DenseHostPattern }), ledgerbench.Target{Max: ms(150)}},
	{"09-host-negation-dense", text("host != %q", func(m *fixture.Manifest) any { return m.DominantHost }), ledgerbench.Target{Max: ms(150)}},
	{"09-host-wildcard-sparse", text("host = %q", func(m *fixture.Manifest) any { return m.SparseHostPattern }),
		ledgerbench.Target{Max: 3 * time.Second, Pathological: true}},
	{"10-server-errors", text("status_code >= 500"), ledgerbench.Target{Max: ms(150)}},
	{"12-launchpad-big", text("launchpad = %q", func(m *fixture.Manifest) any { return m.BigLaunchpad }), ledgerbench.Target{Max: ms(150)}},
	{"12-launchpad-old", text("launchpad = %q", func(m *fixture.Manifest) any { return m.OldLaunchpad }), ledgerbench.Target{Max: ms(150)}},
	{"12-launchpad-any", text("launchpad:*"), ledgerbench.Target{Max: ms(150)}},
	{"12-finding-small", text("finding = %q", func(m *fixture.Manifest) any { return m.SmallFinding }), ledgerbench.Target{Max: ms(150)}},
	{"12-test-case-large", text("test_case = %q", func(m *fixture.Manifest) any { return m.LargeTestCase }), ledgerbench.Target{Max: ms(150)}},
	// Case 14: the day before the newest pair. requested_at adds only a lower
	// id bound (ticket 07), so this is (P) at 500 ms.
	{"14-requested-at-yesterday", text("requested_at >= %q AND requested_at < %q",
		func(m *fixture.Manifest) any { return m.Newest.Add(-48 * time.Hour).Format(time.RFC3339) },
		func(m *fixture.Manifest) any { return m.Newest.Add(-24 * time.Hour).Format(time.RFC3339) }),
		ledgerbench.Target{Max: ms(500), Pathological: true}},
	{"13-metadata-extension", text("metadata.extension = %q", func(m *fixture.Manifest) any { return m.RareExtension }),
		ledgerbench.Target{Max: 3 * time.Second, Pathological: true}},
	{"15-rare-text", text("%q", func(m *fixture.Manifest) any { return m.RareTerm }), ledgerbench.Target{Max: ms(50)}},
	{"16-common-text-term", text("%s", func(m *fixture.Manifest) any { return m.CommonTerm }),
		ledgerbench.Target{Max: ms(500), Pathological: true}},
	{"16-common-text-phrase", text("response_head:%q", func(m *fixture.Manifest) any { return m.CommonPhrase }),
		ledgerbench.Target{Max: ms(500), Pathological: true}},
	{"17-text-and-sparse-host", text("%s AND host = %q",
		func(m *fixture.Manifest) any { return m.CommonTerm }, func(m *fixture.Manifest) any { return m.OldHost }),
		ledgerbench.Target{Max: time.Second, Pathological: true}},
	{"17-not-text", text("NOT %s", func(m *fixture.Manifest) any { return m.CommonTerm }),
		ledgerbench.Target{Max: time.Second, Pathological: true}},
	{"17-text-and-exclusion", func(p *project) domain.TrafficQuery {
		return domain.TrafficQuery{Text: p.manifest.CommonTerm, Exclusion: fixture.SeedExclusion}
	}, ledgerbench.Target{Max: time.Second, Pathological: true}},
	// Ticket 07 makes a top-level OR of index-driven branches a UNION.
	{"18-host-or-note", text("host = %q OR note:%q",
		func(m *fixture.Manifest) any { return m.RareOldHost }, func(m *fixture.Manifest) any { return m.RareNoteTerm }),
		ledgerbench.Target{Max: ms(150)}},
}

// BenchmarkQuery runs every list case warm, through the Repository and
// through GET /traffic.
func BenchmarkQuery(b *testing.B) {
	p := benchProject(b)
	repo := openRepo(b, p.path)
	server := service.NewServer(&marasi.Proxy{TrafficRepo: repo}, nil, nil, func() {}, "bench", "bench", p.path)
	for _, c := range queryCases {
		query := c.query(p)
		b.Run(c.name+"/repo", func(b *testing.B) {
			ledgerbench.Measure(b, c.target, func() error {
				_, err := listPage(repo, query)
				return err
			})
		})
		b.Run(c.name+"/service", func(b *testing.B) {
			target := trafficURL(query)
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

// BenchmarkSort is cases 29 to 32: sorted traffic lists (ticket 20). Each
// case runs warm through the Repository and through GET /traffic.
func BenchmarkSort(b *testing.B) {
	p := benchProject(b)
	repo := openRepo(b, p.path)
	server := service.NewServer(&marasi.Proxy{TrafficRepo: repo, LaunchpadRepo: repo}, nil, nil, func() {}, "bench", "bench", p.path)
	// cursorAfter is the cursor after the first n pairs of q.
	cursorAfter := func(q domain.TrafficQuery, n int) string {
		q.Limit = n
		page, err := repo.ListTraffic(q)
		if err != nil {
			b.Fatal(err)
		}
		if page.NextCursor == nil {
			b.Fatalf("%+v has no more than %d pairs", q, n)
		}
		return *page.NextCursor
	}
	type sortCase struct {
		name   string
		query  domain.TrafficQuery
		target ledgerbench.Target
	}
	within := ledgerbench.Target{Max: ms(150)}
	var cases []sortCase

	// Case 29: the first page and a page from halfway, for every sort key
	// in both directions, with and without the seed exclusion.
	middle := p.manifest.MiddleID
	for _, key := range []string{"method", "host", "path", "status_code", "content_type", "length"} {
		for _, orderBy := range []string{key, key + " desc"} {
			for _, exclusion := range []struct {
				name   string
				values []string
			}{{"", nil}, {"-seed-exclusion", fixture.SeedExclusion}} {
				q := domain.TrafficQuery{OrderBy: orderBy, Exclusion: exclusion.values}
				name := "29-" + strings.ReplaceAll(orderBy, " ", "-") + exclusion.name
				cases = append(cases, sortCase{name + "-first", q, within})
				deep := q
				deep.AsOf = &middle
				deep.Cursor = cursorAfter(deep, p.manifest.Pairs/4)
				cases = append(cases, sortCase{name + "-deep", deep, within})
			}
		}
	}

	// Case 30: a sort with a filter. highlight:* lands with ticket 09;
	// until then launchpad:* stands in as the sparse link-table filter.
	denseType := ""
	if counts, err := repo.CountContentTypes(); err == nil {
		top := 0
		for _, count := range counts.Counts {
			if count.ContentType != "" && count.Count > top {
				denseType, top = count.ContentType, count.Count
			}
		}
	}
	cases = append(cases,
		sortCase{"30-status-asc-sparse-host", domain.TrafficQuery{OrderBy: "status_code", Text: fmt.Sprintf("host = %q", p.manifest.SpreadHost)}, within},
		sortCase{"30-length-desc-dense-type", domain.TrafficQuery{OrderBy: "length desc", Text: fmt.Sprintf("content_type = %q", denseType)}, within},
		sortCase{"30-host-asc-launchpad-any", domain.TrafficQuery{OrderBy: "host", Text: "launchpad:*"}, within},
		sortCase{"30-length-desc-common-text", domain.TrafficQuery{OrderBy: "length desc", Text: p.manifest.CommonTerm},
			ledgerbench.Target{Max: ms(500), Pathological: true}},
	)

	// Case 31: a backward page from an anchor halfway through a sorted
	// result made under an older as_of, read under a new one.
	for _, orderBy := range []string{"host", "length desc"} {
		anchor := cursorAfter(domain.TrafficQuery{OrderBy: orderBy, AsOf: &middle}, p.manifest.Pairs/4)
		cases = append(cases, sortCase{"31-backward-from-anchor-" + strings.ReplaceAll(orderBy, " ", "-"),
			domain.TrafficQuery{OrderBy: orderBy, Cursor: anchor, Direction: domain.Backward}, within})
	}

	// Case 32: status_code asc reaching the pairs with no response, the
	// second phase of the walk: a page that crosses into it and the last
	// page.
	var unvalued int
	if count, err := repo.CountTraffic(domain.TrafficCountQuery{Text: "NOT status_code >= 0"}); err != nil {
		b.Fatal(err)
	} else {
		unvalued = count.Count
	}
	statusAsc := domain.TrafficQuery{OrderBy: "status_code"}
	crossing := statusAsc
	crossing.Cursor = cursorAfter(statusAsc, p.manifest.Pairs-unvalued-pageSize/2)
	last := statusAsc
	last.Cursor = cursorAfter(statusAsc, p.manifest.Pairs-pageSize/2)
	cases = append(cases,
		sortCase{"32-status-asc-into-no-response", crossing, within},
		sortCase{"32-status-asc-last-page", last, within})

	for _, c := range cases {
		b.Run(c.name+"/repo", func(b *testing.B) {
			ledgerbench.Measure(b, c.target, func() error {
				_, err := listPage(repo, c.query)
				return err
			})
		})
		b.Run(c.name+"/service", func(b *testing.B) {
			target := trafficURL(c.query)
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

// openAndList opens the project at path with a new sql.DB and Repository and
// reads the first page of q: a connection-cold run.
func openAndList(path string, q domain.TrafficQuery) func() (func(), error) {
	return func() (func(), error) {
		conn, err := db.New(path, quiet())
		if err != nil {
			return nil, err
		}
		repo := db.NewProxyRepo(conn)
		_, err = listPage(repo, q)
		return func() { repo.Close() }, err
	}
}

// BenchmarkOpen measures opening a project and showing its first page,
// connection-cold.
func BenchmarkOpen(b *testing.B) {
	p := benchProject(b)
	seed := domain.TrafficQuery{Exclusion: fixture.SeedExclusion}
	b.Run("01-steady-state", func(b *testing.B) {
		ledgerbench.MeasureCold(b, ledgerbench.Target{Max: ms(300)}, openAndList(p.path, seed))
	})
	// Case 2: the traffic index job was reset, as an upgrade that changes a
	// job's version resets it. Migrations are already applied.
	b.Run("02-after-upgrade", func(b *testing.B) {
		ledgerbench.MeasureCold(b, ledgerbench.Target{Max: ms(500), Pathological: true}, openAndList(unindexedProject(b, p), seed))
	})
	// Case 3, connection-cold: the first query of a session.
	b.Run("03-unfiltered-cold", func(b *testing.B) {
		ledgerbench.MeasureCold(b, ledgerbench.Target{Max: ms(300)}, openAndList(p.path, domain.TrafficQuery{}))
	})
}

// BenchmarkCountContentTypes is case 20: the project's content-type counts,
// read from the sitemap's buckets, through the Repository and through
// GET /traffic/content-types. It first checks that the counts equal a
// GROUP BY over the pairs, and that the sitemap was complete.
func BenchmarkCountContentTypes(b *testing.B) {
	p := benchProject(b)
	conn, err := db.New(p.path, quiet())
	if err != nil {
		b.Fatal(err)
	}
	repo := db.NewProxyRepo(conn)
	b.Cleanup(func() { repo.Close() })
	counts, err := repo.CountContentTypes()
	if err != nil {
		b.Fatal(err)
	}
	if !counts.Complete[domain.JobSitemap] {
		b.Fatal("the fixture's sitemap job is incomplete, so case 20 would not read the sitemap")
	}
	var grouped []domain.ContentTypeCount
	err = conn.Select(&grouped, `SELECT coalesce(content_type, '') AS contenttype, count(*) AS count
		FROM request GROUP BY coalesce(content_type, '') ORDER BY count DESC, contenttype ASC`)
	if err != nil {
		b.Fatal(err)
	}
	got := make([]domain.ContentTypeCount, len(counts.Counts))
	for i, count := range counts.Counts {
		got[i] = *count
	}
	if !slices.Equal(got, grouped) {
		b.Fatalf("\nwanted the GROUP BY counts:\n%v\ngot:\n%v", grouped, got)
	}

	server := service.NewServer(&marasi.Proxy{TrafficRepo: repo}, nil, nil, func() {}, "bench", "bench", p.path)
	b.Run("20-count-content-types/repo", func(b *testing.B) {
		ledgerbench.Measure(b, ledgerbench.Target{Max: ms(150)}, func() error {
			_, err := repo.CountContentTypes()
			return err
		})
	})
	b.Run("20-count-content-types/service", func(b *testing.B) {
		ledgerbench.Measure(b, ledgerbench.Target{Max: ms(150)}, func() error {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/traffic/content-types", nil))
			if response.Code != http.StatusOK {
				return fmt.Errorf("GET /traffic/content-types: %d %s", response.Code, response.Body)
			}
			return nil
		})
	})
}

// Case 21 adds a node with wideChildren children, one pair each, to a copy
// of the fixture, under wideHost.
const (
	wideHost     = "wide.example.com"
	wideNode     = "https://" + wideHost + "/items"
	wideChildren = 50_000
)

// BenchmarkSitemap is case 21: expanding a node of the stored sitemap over
// all traffic, a page of domain.MaxSitemapPage children at a time, with and
// without the seed exclusion. It expands the origins of the fixture and a
// node of 50,000 children added to a copy of it.
func BenchmarkSitemap(b *testing.B) {
	p := benchProject(b)
	repo := openRepo(b, cloneOf(b, p.path))
	for {
		_, remaining, err := repo.RunJobBatch(domain.JobSitemap, 1<<20)
		if err != nil {
			b.Fatal(err)
		}
		if !remaining {
			break
		}
	}
	for i, pair := range fixture.NewPairs(p.manifest.Seed+21, wideChildren, p.manifest.Newest, 0) {
		pair.Request.Host = wideHost
		pair.Request.Path = fmt.Sprintf("/items/item-%d-x", i)
		if err := repo.InsertRequest(pair.Request); err != nil {
			b.Fatal(err)
		}
		if pair.Response != nil {
			if err := repo.InsertResponse(pair.Response); err != nil {
				b.Fatal(err)
			}
		}
	}
	// cursorAfter is the cursor of node's page after pages full pages.
	cursorAfter := func(node string, pages int) string {
		cursor := ""
		for range pages {
			page, err := repo.ListSitemapChildren(node, nil, cursor, domain.MaxSitemapPage)
			if err != nil {
				b.Fatal(err)
			}
			if page.NextCursor == nil {
				b.Fatalf("%s has fewer than %d pages", node, pages+1)
			}
			cursor = *page.NextCursor
		}
		return cursor
	}
	cases := []struct {
		name      string
		node      string
		exclusion []string
		cursor    string
	}{
		{"21-root", "", nil, ""},
		{"21-root-seed-exclusion", "", fixture.SeedExclusion, ""},
		{"21-50k-children", wideNode, nil, ""},
		{"21-50k-children-seed-exclusion", wideNode, fixture.SeedExclusion, ""},
		{"21-50k-children-page-50", wideNode, nil, cursorAfter(wideNode, 49)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			ledgerbench.Measure(b, ledgerbench.Target{Max: ms(50)}, func() error {
				page, err := repo.ListSitemapChildren(c.node, c.exclusion, c.cursor, domain.MaxSitemapPage)
				if err == nil && len(page.Children) == 0 {
					err = fmt.Errorf("%q has no children", c.node)
				}
				return err
			})
		})
	}
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
// index job was reset. It also reports the longest capture stall.
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
			_, err := listPage(repo, domain.TrafficQuery{Exclusion: fixture.SeedExclusion})
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
			if _, _, err := repo.RunJobBatch(domain.JobTrafficIndex, 1<<20); err != nil {
				b.Fatal(err)
			}
			batches = append(batches, time.Since(started))
		}
		ctx, stop := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() { buildIndex(ctx, repo) })
		query := domain.TrafficQuery{Text: fmt.Sprintf("%q", p.manifest.RareTerm)}
		sawIncomplete := false
		ledgerbench.Measure(b, ledgerbench.Target{Max: ms(150) + batches.Percentile(95)}, func() error {
			complete, err := listPage(repo, query)
			sawIncomplete = sawIncomplete || !complete
			return err
		})
		stop()
		wg.Wait()
		if !sawIncomplete {
			b.Error("the traffic index was never reported incomplete while its job ran")
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
	cases := map[string]struct {
		query  domain.TrafficQuery
		target time.Duration
	}{
		"01-open-steady-state":  {domain.TrafficQuery{Exclusion: fixture.SeedExclusion}, ms(300)},
		"04-seed-exclusion":     {domain.TrafficQuery{Exclusion: fixture.SeedExclusion}, ms(30)},
		"07-sparse-host-spread": {domain.TrafficQuery{Text: fmt.Sprintf("host = %q", p.manifest.SpreadHost)}, ms(150)},
		"15-rare-text":          {domain.TrafficQuery{Text: fmt.Sprintf("%q", p.manifest.RareTerm)}, ms(50)},
	}
	for _, name := range slices.Sorted(maps.Keys(cases)) {
		c := cases[name]
		b.Run("28-"+name, func(b *testing.B) {
			// The OS-cold target is 3 to 5 times looser; report against 5.
			target := ledgerbench.Target{Max: 5 * c.target, ReportOnly: true}
			ledgerbench.MeasurePrepared(b, target, purge, openAndList(p.path, c.query))
		})
	}
}

// buildIndex runs the traffic index job until it finishes or ctx ends, as
// the service's job runner does.
func buildIndex(ctx context.Context, repo *db.Repository) {
	for ctx.Err() == nil {
		_, remaining, err := repo.RunJobBatch(domain.JobTrafficIndex, 1<<20)
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
			writes := []func() error{func() error { return repo.InsertRequest(pair.Request) }}
			if pair.Response != nil {
				writes = append(writes, func() error { return repo.InsertResponse(pair.Response) })
			}
			for _, write := range writes {
				started := time.Now()
				if err := write(); err != nil {
					return longest
				}
				longest = max(longest, time.Since(started))
			}
		}
	}
}
