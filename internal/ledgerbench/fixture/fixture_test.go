package fixture_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/ledgerbench/fixture"
)

// testMaxBody caps the bodies of the projects these tests build. Capturing
// and indexing full-size bodies takes minutes under -race; what the tests
// check does not depend on body size. The bench1m tag builds full-size bodies.
var testMaxBody = 1 << 10

func testConfig(seed uint64, pairs int) fixture.Config {
	return fixture.Config{Seed: seed, Pairs: pairs, MaxBody: testMaxBody}
}

// openProject opens a built project through the real repository.
func openProject(t *testing.T, path string) *db.Repository {
	t.Helper()
	conn, err := db.New(path, nil)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}
	repo := db.NewProxyRepo(conn)
	t.Cleanup(func() { repo.Close() })
	return repo
}

func TestBuild(t *testing.T) {
	t.Run("should write a fully indexed project with the requested number of pairs", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "small.marasi")

		if _, err := fixture.Build(path, testConfig(1, 150)); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		repo := openProject(t, path)
		count, err := repo.CountRows()
		if err != nil {
			t.Fatalf("counting pairs: %v", err)
		}
		if count != 150 {
			t.Fatalf("\nwanted:\n150\ngot:\n%d", count)
		}
		page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 200})
		if err != nil {
			t.Fatalf("listing traffic: %v", err)
		}
		if !page.Complete[domain.JobTrafficIndex] {
			t.Fatalf("\nwanted:\nindex complete\ngot:\nincomplete")
		}
		jobs, err := repo.ListJobs()
		if err != nil {
			t.Fatalf("listing jobs: %v", err)
		}
		for _, job := range jobs {
			if !job.Complete {
				t.Fatalf("\nwanted:\nevery background job complete\ngot:\n%+v", job)
			}
		}
	})

	t.Run("should cap every body at MaxBody", func(t *testing.T) {
		t.Parallel()
		const maxBody = 1 << 10
		path := filepath.Join(t.TempDir(), "capped.marasi")
		if _, err := fixture.Build(path, fixture.Config{Seed: 1, Pairs: 100, MaxBody: maxBody}); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		// A body is written in whole records until it reaches its size, so
		// it may run over by one record.
		const slack = 1 << 10
		for _, row := range pairRows(t, openProject(t, path)) {
			for _, raw := range [][]byte{row.Request.Raw, row.Response.Raw} {
				_, body, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
				if len(body) > maxBody+slack {
					t.Fatalf("\nwanted:\nbodies of at most about %d bytes\ngot:\n%d bytes", maxBody, len(body))
				}
			}
		}
	})

	t.Run("should write the same pairs for the same seed and different pairs for another", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		build := func(name string, seed uint64) []string {
			path := filepath.Join(dir, name)
			if _, err := fixture.Build(path, testConfig(seed, 100)); err != nil {
				t.Fatalf("building %s: %v", name, err)
			}
			return pairDigests(t, openProject(t, path))
		}

		first := build("a.marasi", 7)
		again := build("b.marasi", 7)
		other := build("c.marasi", 8)

		if !slices.Equal(first, again) {
			t.Fatalf("\nwanted:\nthe same pairs for seed 7\ngot:\n%d of %d pairs differ", countDiffering(first, again), len(first))
		}
		if slices.Equal(first, other) {
			t.Fatalf("\nwanted:\ndifferent pairs for seed 8\ngot:\nthe same pairs")
		}
	})
}

func TestManifest(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "facts.marasi")
	const pairs = 2500
	built, err := fixture.Build(path, testConfig(3, pairs))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	repo := openProject(t, path)
	manifest, err := fixture.ReadManifest(path)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if manifest.RareTerm != built.RareTerm || manifest.Pairs != pairs {
		t.Fatalf("\nwanted:\nthe manifest Build returned\ngot:\n%+v", manifest)
	}

	// all lists every pair matching query, newest first.
	all := func(query string) []*domain.RequestResponseSummary {
		t.Helper()
		var out []*domain.RequestResponseSummary
		q := domain.TrafficQuery{Text: query, Limit: 500}
		for {
			page, err := repo.ListTraffic(q)
			if err != nil {
				t.Fatalf("listing %q: %v", query, err)
			}
			out = append(out, page.Items...)
			if page.NextCursor == nil {
				return out
			}
			q.Cursor, q.AsOf = *page.NextCursor, page.AsOf
		}
	}
	everything := all("")
	middle := everything[pairs/2].RequestedAt

	cases := []struct {
		name  string
		query string
		want  int
		old   bool // every match is older than the middle pair
	}{
		{"spread host", fmt.Sprintf("host = %q", manifest.SpreadHost), manifest.SpreadHostPairs, false},
		{"old host", fmt.Sprintf("host = %q", manifest.OldHost), manifest.OldHostPairs, true},
		{"rare old host", fmt.Sprintf("host = %q", manifest.RareOldHost), manifest.RareOldHostPairs, true},
		{"rare term", fmt.Sprintf("%q", manifest.RareTerm), manifest.RareTermPairs, false},
		{"rare note term", fmt.Sprintf("note:%q", manifest.RareNoteTerm), 1, true},
		{"big launchpad", fmt.Sprintf("launchpad = %q", manifest.BigLaunchpad), pairs / 100, false},
		{"old launchpad", fmt.Sprintf("launchpad = %q", manifest.OldLaunchpad), 100, true},
		{"old highlight", fmt.Sprintf("highlight = %d", manifest.OldHighlight), manifest.OldHighlightPairs, true},
		{"highlights", "highlight:*", pairs / 200, false},
	}
	for _, c := range cases {
		t.Run("should plant the "+c.name, func(t *testing.T) {
			matches := all(c.query)
			if len(matches) != c.want {
				t.Fatalf("\nwanted:\n%d matches\ngot:\n%d", c.want, len(matches))
			}
			for _, m := range matches {
				if c.old && !m.RequestedAt.Before(middle) {
					t.Fatalf("\nwanted:\nevery match older than %v\ngot:\n%v", middle, m.RequestedAt)
				}
			}
		})
	}

	t.Run("should put the spread host's 201st newest match about halfway back", func(t *testing.T) {
		matches := all(fmt.Sprintf("host = %q", manifest.SpreadHost))
		back := slices.IndexFunc(everything, func(s *domain.RequestResponseSummary) bool { return s.ID == matches[200].ID })
		if back < pairs*40/100 || back > pairs*60/100 {
			t.Fatalf("\nwanted:\nabout halfway back\ngot:\n%d of %d pairs back", back, pairs)
		}
	})

	t.Run("should list only excluded content types as of each excluded stretch's newest pair", func(t *testing.T) {
		filters := fixture.SeedExclusion
		if len(manifest.ExcludedStretches) != 2 {
			t.Fatalf("\nwanted:\n2 stretches\ngot:\n%d", len(manifest.ExcludedStretches))
		}
		for _, stretch := range manifest.ExcludedStretches {
			newest := stretch.Newest
			page, err := repo.ListTraffic(domain.TrafficQuery{AsOf: &newest, Limit: stretch.Pairs})
			if err != nil {
				t.Fatalf("listing the stretch: %v", err)
			}
			if len(page.Items) != stretch.Pairs {
				t.Fatalf("\nwanted:\n%d pairs\ngot:\n%d", stretch.Pairs, len(page.Items))
			}
			for _, item := range page.Items {
				if !slices.Contains(filters, item.ContentType) {
					t.Fatalf("\nwanted:\nonly excluded content types\ngot:\n%q", item.ContentType)
				}
			}
		}
	})

	t.Run("should link the small finding and the large test case", func(t *testing.T) {
		finding, err := repo.GetFindingRequests(manifest.SmallFinding)
		if err != nil {
			t.Fatalf("reading finding: %v", err)
		}
		if len(finding) != 50 {
			t.Fatalf("\nwanted:\n50\ngot:\n%d", len(finding))
		}
		testCase, err := repo.GetTestCaseRequests(manifest.LargeTestCase)
		if err != nil {
			t.Fatalf("reading test case: %v", err)
		}
		if len(testCase) != manifest.LargeTestCasePairs {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", manifest.LargeTestCasePairs, len(testCase))
		}
	})
}

func TestNewPairs(t *testing.T) {
	t.Parallel()
	t.Run("should give new pairs after a time that the capture path accepts", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "grow.marasi")
		manifest, err := fixture.Build(path, testConfig(1, 100))
		if err != nil {
			t.Fatalf("building: %v", err)
		}
		repo := openProject(t, path)

		pairs := fixture.NewPairs(9, 120, manifest.Newest, 5_000)

		if len(pairs) != 120 {
			t.Fatalf("\nwanted:\n120 pairs\ngot:\n%d", len(pairs))
		}
		for i, pair := range pairs {
			if !pair.Request.RequestedAt.After(manifest.Newest) {
				t.Fatalf("\nwanted:\nrequested after %v\ngot:\n%v", manifest.Newest, pair.Request.RequestedAt)
			}
			if i > 0 && pair.Request.ID.String() <= pairs[i-1].Request.ID.String() {
				t.Fatalf("\nwanted:\nids in order\ngot:\npair %d out of order", i)
			}
			if pair.Response != nil && len(pair.Response.Raw) < 5_000 {
				t.Fatalf("\nwanted:\n5 KB bodies\ngot:\n%d bytes", len(pair.Response.Raw))
			}
			if err := repo.InsertRequest(pair.Request); err != nil {
				t.Fatalf("inserting request: %v", err)
			}
			if pair.Response != nil {
				if err := repo.InsertResponse(pair.Response); err != nil {
					t.Fatalf("inserting response: %v", err)
				}
			}
		}
		count, _ := repo.CountRows()
		if count != 220 {
			t.Fatalf("\nwanted:\n220\ngot:\n%d", count)
		}
	})
}

// pairDigests returns one line per pair with everything stored about it.
func pairDigests(t *testing.T, repo *db.Repository) []string {
	t.Helper()
	rows := pairRows(t, repo)
	digests := make([]string, len(rows))
	for i, row := range rows {
		digests[i] = fmt.Sprintf("%v|%s|%s|%s|%s|%s|%d|%s|%s|%x|%x|%v|%v|%q",
			row.Request.ID, row.Request.Scheme, row.Request.Method, row.Request.Host, row.Request.Path,
			row.Response.Status, row.Response.StatusCode, row.Response.ContentType, row.Response.Length,
			sha256.Sum256(row.Request.Raw), sha256.Sum256(row.Response.Raw),
			row.Request.RequestedAt.UnixNano(), row.Metadata, row.Note)
	}
	return digests
}

// pairRows reads every pair in full.
func pairRows(t *testing.T, repo *db.Repository) []*domain.RequestResponseRow {
	t.Helper()
	summaries, err := repo.GetRequestResponseSummary()
	if err != nil {
		t.Fatalf("reading pairs: %v", err)
	}
	ids := make([]uuid.UUID, len(summaries))
	for i, s := range summaries {
		ids[i] = s.ID
	}
	rows, err := repo.GetRequestResponseRows(ids)
	if err != nil {
		t.Fatalf("reading pairs: %v", err)
	}
	return rows
}

func countDiffering(a, b []string) int {
	n := max(len(a), len(b)) - min(len(a), len(b))
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}
