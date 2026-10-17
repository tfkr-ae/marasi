package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// queryChildren reads one page of a node's children in query mode and
// formats them as children does.
func queryChildren(t *testing.T, repo *Repository, node string, query domain.SelectionQuery) string {
	t.Helper()
	page, err := repo.ListSitemapChildren(context.Background(), domain.SitemapQuery{Node: node, Query: &query, Limit: domain.MaxSitemapPage})
	if err != nil {
		t.Fatalf("ListSitemapChildren(%q, %+v): %v", node, query, err)
	}
	return formatChildren(page.Children)
}

// newestID returns the id of the newest pair.
func newestID(t *testing.T, repo *Repository) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := repo.dbConn.Get(&id, `SELECT id FROM request ORDER BY id DESC LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSitemap_QueryMode(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	for _, pair := range sitemapPairs {
		capturePair(t, repo, pair.url, pair.contentType, pair.status)
	}
	asOf := newestID(t, repo)

	t.Run("should count only the pairs that match the query", func(t *testing.T) {
		query := domain.SelectionQuery{Text: `host = "api.x.com" AND content_type = "application/json"`, AsOf: asOf}
		if got, want := queryChildren(t, repo, "", query), "https://api.x.com 3/1+"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
		if got, want := queryChildren(t, repo, "https://api.x.com", query), "users 3/1+"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
		if got, want := queryChildren(t, repo, "https://api.x.com/users", query), "{int} 2/1, {uuid} 1/0+"; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})
}

// matchCount counts the pairs ListTraffic returns for text, exclusion and
// asOf.
func matchCount(t *testing.T, repo *Repository, text string, exclusion []string, asOf uuid.UUID) int64 {
	t.Helper()
	var count int64
	q := domain.TrafficQuery{Text: text, Exclusion: exclusion, AsOf: &asOf, Limit: 1000}
	for {
		page, err := repo.ListTraffic(q)
		if err != nil {
			t.Fatalf("ListTraffic(%q): %v", text, err)
		}
		count += int64(len(page.Items))
		if page.NextCursor == nil {
			return count
		}
		q.Cursor = *page.NextCursor
	}
}

func TestSitemap_QueryModeAgreesWithTheTable(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	for _, pair := range sitemapPairs {
		capturePair(t, repo, pair.url, pair.contentType, pair.status)
	}
	capturePair(t, repo, "https://api.x.com/users/44", "image/png", 502)
	capturePair(t, repo, "https://api.x.com/users", "application/json", 200)
	asOf := newestID(t, repo)

	for _, query := range []domain.SelectionQuery{
		{},
		{Text: `content_type = "application/json"`},
		{Text: `status_code >= 500`},
		{Text: `NOT host = "api.x.com"`},
		{Text: `sitemap = "https://api.x.com/users"`, Exclusion: []string{"image/*"}},
		{Exclusion: []string{"image/*", "text/html"}},
	} {
		query.AsOf = asOf
		t.Run(fmt.Sprintf("should count %q excluding %v as the table does", query.Text, query.Exclusion), func(t *testing.T) {
			var walk func(node string)
			walk = func(node string) {
				cursor := ""
				for {
					page, err := repo.ListSitemapChildren(context.Background(), domain.SitemapQuery{Node: node, Query: &query, Cursor: cursor, Limit: 2})
					if err != nil {
						t.Fatalf("ListSitemapChildren(%q): %v", node, err)
					}
					for _, child := range page.Children {
						narrowed := fmt.Sprintf("sitemap = %q", child.Name)
						if query.Text != "" {
							narrowed = "(" + query.Text + ") AND " + narrowed
						}
						if want := matchCount(t, repo, narrowed, query.Exclusion, asOf); child.Pairs != want {
							t.Errorf("\nwanted:\n%s with %d pairs\ngot:\n%d", child.Name, want, child.Pairs)
						}
						if want := matchCount(t, repo, narrowed+" AND status_code >= 500", query.Exclusion, asOf); child.Errors != want {
							t.Errorf("\nwanted:\n%s with %d errors\ngot:\n%d", child.Name, want, child.Errors)
						}
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
		})
	}

	t.Run("should leave out pairs inserted after as_of", func(t *testing.T) {
		query := domain.SelectionQuery{Text: `host = "api.x.com"`, AsOf: asOf}
		before := queryChildren(t, repo, "https://api.x.com/users", query)
		capturePair(t, repo, "https://api.x.com/users/45", "application/json", 500)
		capturePair(t, repo, "https://api.x.com/users/new/child", "application/json", 200)
		if got := queryChildren(t, repo, "https://api.x.com/users", query); got != before {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", before, got)
		}
	})
}

func TestSitemap_QueryModeCancel(t *testing.T) {
	// Not parallel: it times reads, which other tests would slow.
	repo, teardown := setupTestDB(t)
	defer teardown()
	// Many pairs under one leaf, written directly so the setup is quick.
	const pairs = 100_000
	setup := []string{
		`INSERT INTO sitemap_node (id, parent_id, name, depth) VALUES (1, NULL, 'https://x.com', 0), (2, 1, 'https://x.com/a', 1)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ` + fmt.Sprint(pairs) + `)
			INSERT INTO request (id, scheme, method, host, path, request_raw, status_code, content_type, metadata, requested_at)
			SELECT printf('00000000-0000-7000-8000-%012d', i), 'https', 'GET', 'x.com', '/a', 'GET / HTTP/1.1', 200, 'text/html', '{}', '2026-01-01' FROM n`,
		`INSERT INTO pair_sitemap (request_id, node_id) SELECT id, 2 FROM request`,
	}
	for _, statement := range setup {
		if _, err := repo.dbConn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	query := domain.SelectionQuery{Text: `path = "*a*"`, AsOf: newestID(t, repo)}
	read := func(ctx context.Context) (time.Duration, error) {
		start := time.Now()
		_, err := repo.ListSitemapChildren(ctx, domain.SitemapQuery{Query: &query, Limit: 10})
		return time.Since(start), err
	}
	full, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("should stop the read promptly when the context ends", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), full/10)
		defer cancel()
		took, err := read(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || took > full/2 {
			t.Fatalf("\nwanted:\ncontext.DeadlineExceeded well within the %v a full read takes\ngot:\n%v after %v", full, err, took)
		}
	})
}
