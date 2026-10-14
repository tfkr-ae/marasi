package db

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// capturePair writes a pair the way capture does: its request, then, when
// status is not 0, its response.
func capturePair(t *testing.T, repo *Repository, url, contentType string, status int) uuid.UUID {
	t.Helper()
	scheme, rest, _ := strings.Cut(url, "://")
	host, path, _ := strings.Cut(rest, "/")
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID: id, Scheme: scheme, Method: "GET", Host: host, Path: "/" + path,
		Raw: []byte("GET /" + path + " HTTP/1.1\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertRequest(%s): %v", url, err)
	}
	if status != 0 {
		respond(t, repo, id, contentType, status)
	}
	return id
}

// respond writes a response for the pair with id.
func respond(t *testing.T, repo *Repository, id uuid.UUID, contentType string, status int) {
	t.Helper()
	err := repo.InsertResponse(&domain.ProxyResponse{
		ID: id, Status: fmt.Sprint(status), StatusCode: status, ContentType: contentType,
		Raw: []byte("HTTP/1.1 200 OK\r\n\r\n"), Metadata: map[string]any{}, RespondedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertResponse(%s): %v", id, err)
	}
}

// children reads one page of a node's children and formats each as
// "segment pairs/errors", with a + when it has children.
func children(t *testing.T, repo *Repository, node string, exclusion ...string) string {
	t.Helper()
	page, err := repo.ListSitemapChildren(node, exclusion, "", domain.MaxSitemapPage)
	if err != nil {
		t.Fatalf("ListSitemapChildren(%q): %v", node, err)
	}
	return formatChildren(page.Children)
}

func formatChildren(children []domain.SitemapChild) string {
	parts := make([]string, len(children))
	for i, child := range children {
		parts[i] = fmt.Sprintf("%s %d/%d", child.Segment, child.Pairs, child.Errors)
		if child.HasChildren {
			parts[i] += "+"
		}
	}
	return strings.Join(parts, ", ")
}

// sitemapTree walks the whole sitemap and formats every node as
// "name pairs/errors", one per line, depth first.
func sitemapTree(t *testing.T, repo *Repository, exclusion ...string) string {
	t.Helper()
	var lines []string
	var walk func(node string)
	walk = func(node string) {
		cursor := ""
		for {
			page, err := repo.ListSitemapChildren(node, exclusion, cursor, 2)
			if err != nil {
				t.Fatalf("ListSitemapChildren(%q): %v", node, err)
			}
			for _, child := range page.Children {
				lines = append(lines, fmt.Sprintf("%s %d/%d", child.Name, child.Pairs, child.Errors))
				if child.HasChildren {
					walk(child.Name)
				}
			}
			if page.NextCursor == nil {
				return
			}
			cursor = *page.NextCursor
		}
	}
	walk("")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func TestSitemap_Capture(t *testing.T) {
	t.Run("should count captured pairs at each node of their path", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		capturePair(t, repo, "https://api.x.com/users/42", "application/json", 200)
		capturePair(t, repo, "https://api.x.com/users/7/orders", "application/json", 500)
		capturePair(t, repo, "https://api.x.com/health", "text/plain", 200)
		capturePair(t, repo, "http://other.com:8080/", "text/html", 200)

		if got, want := children(t, repo, ""), "https://api.x.com 3/1+, http://other.com:8080 1/0"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
		if got, want := children(t, repo, "https://api.x.com"), "users 2/1+, health 1/0"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
		if got, want := children(t, repo, "https://api.x.com/users/{int}"), "orders 1/1"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should count an in-flight pair, then move it to its response's content type", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := capturePair(t, repo, "https://x.com/a", "", 0)
		capturePair(t, repo, "https://x.com/a", "image/png", 200)

		if got, want := children(t, repo, "https://x.com", "image/*"), "a 1/0"; got != want {
			t.Fatalf("\nwanted:\nthe in-flight pair is never excluded: %q\ngot:\n%q", want, got)
		}
		respond(t, repo, id, "image/png", 503)
		if got, want := children(t, repo, "https://x.com"), "a 2/1"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
		if got := children(t, repo, "https://x.com", "image/png"); got != "" {
			t.Fatalf("\nwanted:\nno children once every pair is excluded\ngot:\n%q", got)
		}
	})

	t.Run("should move a pair between buckets when its response is written again", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		id := capturePair(t, repo, "https://x.com/a", "text/html", 500)
		respond(t, repo, id, "application/json", 200)

		if got, want := children(t, repo, "https://x.com"), "a 1/0"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
		if got, want := children(t, repo, "https://x.com", "text/html"), "a 1/0"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
		if got := children(t, repo, "https://x.com", "application/json"); got != "" {
			t.Fatalf("\nwanted:\nnothing left after excluding the new type\ngot:\n%q", got)
		}
	})
}

func TestSitemap_Exclusion(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	types := []string{"text/html", "application/json", "image/png", "image/gif", ""}
	for i := range 40 {
		contentType := types[i%len(types)]
		status := 200
		if i%7 == 0 {
			status = 500
		}
		capturePair(t, repo, fmt.Sprintf("https://x.com/s%d/p%d", i%3, i%4), contentType, status)
	}
	capturePair(t, repo, "https://x.com/s0/in-flight", "", 0)

	// Each pair is in exactly one bucket, so excluding every type but one
	// leaves that type's bucket, and the buckets add up to the totals.
	everyType := []string{"text/html", "application/json", "image/png", "image/gif"}
	type counts struct{ pairs, errors int64 }
	read := func(exclusion []string) map[string]counts {
		page, err := repo.ListSitemapChildren("https://x.com", exclusion, "", domain.MaxSitemapPage)
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]counts{}
		for _, child := range page.Children {
			byName[child.Name] = counts{child.Pairs, child.Errors}
		}
		return byName
	}
	total := read(nil)
	untyped := read(everyType)
	sum := map[string]counts{}
	for name, c := range untyped {
		sum[name] = c
	}
	for _, kept := range everyType {
		only := read(slices.DeleteFunc(slices.Clone(everyType), func(v string) bool { return v == kept }))
		for name, c := range only {
			bucket := counts{c.pairs - untyped[name].pairs, c.errors - untyped[name].errors}
			sum[name] = counts{sum[name].pairs + bucket.pairs, sum[name].errors + bucket.errors}

			excluded := read([]string{kept})[name]
			if want := (counts{total[name].pairs - bucket.pairs, total[name].errors - bucket.errors}); excluded != want {
				t.Fatalf("\nwanted:\nexcluding %s from %s to subtract its bucket: %+v\ngot:\n%+v", kept, name, want, excluded)
			}
		}
	}
	if !maps(sum, total) {
		t.Fatalf("\nwanted:\nthe buckets to add up to %+v\ngot:\n%+v", total, sum)
	}
	if want := (counts{15, 2}); total["https://x.com/s0"] != want {
		t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, total["https://x.com/s0"])
	}
}

func maps[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestSitemap_ListChildren(t *testing.T) {
	t.Run("should page children by pair count, highest first", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		for i, segment := range []string{"a", "b", "c", "d", "e"} {
			for range i + 1 {
				capturePair(t, repo, "https://x.com/"+segment, "text/html", 200)
			}
		}
		var got []string
		cursor := ""
		for {
			page, err := repo.ListSitemapChildren("https://x.com", nil, cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, formatChildren(page.Children))
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		want := []string{"e 5/0, d 4/0", "c 3/0, b 2/0", "a 1/0"}
		if !slices.Equal(got, want) {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should report a node it does not know", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		_, err := repo.ListSitemapChildren("https://nowhere.com", nil, "", 10)
		if !errors.Is(err, domain.ErrSitemapNodeNotFound) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrSitemapNodeNotFound, err)
		}
	})

	t.Run("should reject a bad cursor, limit or exclusion", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		if _, err := repo.ListSitemapChildren("", nil, "not-a-cursor", 10); !errors.Is(err, domain.ErrInvalidCursor) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrInvalidCursor, err)
		}
		if _, err := repo.ListSitemapChildren("", nil, "", 0); err == nil {
			t.Fatalf("\nwanted:\nan error for limit 0\ngot:\nnil")
		}
		var queryErr *domain.QueryError
		if _, err := repo.ListSitemapChildren("", []string{"im*ge/png"}, "", 10); !errors.As(err, &queryErr) {
			t.Fatalf("\nwanted:\na *domain.QueryError\ngot:\n%v", err)
		}
	})

	t.Run("should cap a page at the most children a read returns", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		capturePair(t, repo, "https://x.com/a", "text/html", 200)
		page, err := repo.ListSitemapChildren("", nil, "", domain.MaxSitemapPage+1)
		if err != nil || len(page.Children) != 1 {
			t.Fatalf("\nwanted:\none child\ngot:\n%+v, %v", page, err)
		}
	})

	t.Run("should report the sitemap as complete on a new project", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		page, err := repo.ListSitemapChildren("", nil, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]bool{domain.JobSitemap: true}; !maps(page.Complete, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, page.Complete)
		}
	})
}

// sitemapPairs are pairs that exercise the rules, as url, content type and
// status, where status 0 is in flight.
var sitemapPairs = []struct {
	url         string
	contentType string
	status      int
}{
	{"https://api.x.com/users/42", "application/json", 200},
	{"https://api.x.com/users/43", "application/json", 503},
	{"https://api.x.com/users/0193a1b2-7c3d-7e4f-8a9b-0c1d2e3f4a5b/orders", "application/json", 200},
	{"https://api.x.com/v1/getOrderHistory2024?x=1", "text/html", 200},
	{"https://cdn.x.com/assets/d41d8cd98f00b204e9800998ecf8427e", "image/png", 200},
	{"https://cdn.x.com/files/a%2Fb//raw/", "", 200},
	{"http://local.x.com:8080/2024/{id}", "text/plain", 500},
	{"http://local.x.com:8080/", "", 0},
}

// insertUnlinkedPair writes a pair the way a binary from before the sitemap
// does: into the request table only.
func insertUnlinkedPair(t *testing.T, repo *Repository, url, contentType string, status int) {
	t.Helper()
	scheme, rest, _ := strings.Cut(url, "://")
	host, path, _ := strings.Cut(rest, "/")
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	var code, responseType any
	if status != 0 {
		code, responseType = status, contentType
	}
	_, err = repo.dbConn.Exec(`INSERT INTO request
		(id, scheme, method, host, path, request_raw, status_code, content_type, metadata, requested_at)
		VALUES (?, ?, 'GET', ?, ?, 'GET / HTTP/1.1', coalesce(?, -1), nullif(?, ''), '{}', ?)`,
		id, scheme, host, "/"+path, code, responseType, time.Now())
	if err != nil {
		t.Fatalf("inserting an unlinked pair: %v", err)
	}
}

func sitemapJobState(t *testing.T, repo *Repository) JobState {
	t.Helper()
	jobs, err := repo.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.Name == SitemapJob {
			return job
		}
	}
	t.Fatalf("\nwanted:\na %s job\ngot:\n%+v", SitemapJob, jobs)
	return JobState{}
}

// runSitemapJob runs the sitemap job until it has nothing left, maxPairs at a
// time.
func runSitemapJob(t *testing.T, repo *Repository, maxPairs int) {
	t.Helper()
	for range 1000 {
		_, remaining, err := repo.AddMissingToSitemap(maxPairs)
		if err != nil {
			t.Fatalf("AddMissingToSitemap: %v", err)
		}
		if !remaining {
			return
		}
	}
	t.Fatal("the sitemap job did not finish")
}

func TestSitemap_Job(t *testing.T) {
	captured, teardown := setupTestDB(t)
	defer teardown()
	for _, pair := range sitemapPairs {
		capturePair(t, captured, pair.url, pair.contentType, pair.status)
	}
	want := sitemapTree(t, captured)

	t.Run("should build the same tree from stored pairs as capture does", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		for _, pair := range sitemapPairs {
			insertUnlinkedPair(t, repo, pair.url, pair.contentType, pair.status)
		}
		repo = reopenProject(t, repo)
		if got := sitemapJobState(t, repo); got.Complete {
			t.Fatalf("\nwanted:\nan incomplete sitemap job after an older binary wrote pairs\ngot:\n%+v", got)
		}
		page, err := repo.ListSitemapChildren("", nil, "", 10)
		if err != nil || page.Complete[SitemapJob] {
			t.Fatalf("\nwanted:\nthe read to report the sitemap as incomplete\ngot:\n%v, %v", page.Complete, err)
		}

		runSitemapJob(t, repo, 3)

		if got := sitemapTree(t, repo); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
		if got := sitemapJobState(t, repo); got != (JobState{Name: SitemapJob, Complete: true, Done: 8, Total: 8}) {
			t.Fatalf("\nwanted:\na complete job that added 8 pairs\ngot:\n%+v", got)
		}
		if got, want := sitemapTree(t, repo, "application/json"), sitemapTree(t, captured, "application/json"); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should rebuild the tree on request", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		for _, pair := range sitemapPairs {
			capturePair(t, repo, pair.url, pair.contentType, pair.status)
		}
		if err := repo.RebuildJob(SitemapJob); err != nil {
			t.Fatalf("RebuildJob: %v", err)
		}
		if got := sitemapTree(t, repo); got != "" {
			t.Fatalf("\nwanted:\nan empty tree after the reset\ngot:\n%s", got)
		}
		runSitemapJob(t, repo, 100)
		if got := sitemapTree(t, repo); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should keep pairs captured during a build in the tree", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		for _, pair := range sitemapPairs[:4] {
			insertUnlinkedPair(t, repo, pair.url, pair.contentType, pair.status)
		}
		repo = reopenProject(t, repo)
		runSitemapJob(t, repo, 1)
		for _, pair := range sitemapPairs[4:] {
			capturePair(t, repo, pair.url, pair.contentType, pair.status)
		}
		runSitemapJob(t, repo, 1)
		if got := sitemapTree(t, repo); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})
}

func TestSitemap_CapturedTree(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	for _, pair := range sitemapPairs {
		capturePair(t, repo, pair.url, pair.contentType, pair.status)
	}
	want := strings.Join([]string{
		"http://local.x.com:8080 2/1",
		"http://local.x.com:8080/{int} 1/1",
		"http://local.x.com:8080/{int}/%7Bid%7D 1/1",
		"https://api.x.com 4/1",
		"https://api.x.com/users 3/1",
		"https://api.x.com/users/{int} 2/1",
		"https://api.x.com/users/{uuid} 1/0",
		"https://api.x.com/users/{uuid}/orders 1/0",
		"https://api.x.com/v1 1/0",
		"https://api.x.com/v1/getOrderHistory2024 1/0",
		"https://cdn.x.com 2/0",
		"https://cdn.x.com/assets 1/0",
		"https://cdn.x.com/assets/{hash} 1/0",
		"https://cdn.x.com/files 1/0",
		"https://cdn.x.com/files/a%2Fb 1/0",
		"https://cdn.x.com/files/a%2Fb/raw 1/0",
	}, "\n")
	if got := sitemapTree(t, repo); got != want {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
	}
}
