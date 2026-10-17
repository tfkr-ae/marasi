package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// formatLevels formats each level as its node, then one line per child:
// "name pairs/errors matching/matching_errors", with - for an unknown
// matching count and a + when the child has children.
func formatLevels(levels domain.SitemapLevels) string {
	var lines []string
	for _, level := range levels.Levels {
		header := "# " + level.Node
		if level.Missing {
			header += " missing"
		}
		if level.NextCursor != nil {
			header += " more"
		}
		lines = append(lines, header)
		for _, child := range level.Children {
			matching := "-"
			if child.Matching != nil {
				matching = fmt.Sprintf("%d/%d", *child.Matching, *child.MatchingErrors)
			}
			line := fmt.Sprintf("%s %d/%d %s", child.Name, child.Pairs, child.Errors, matching)
			if child.HasChildren {
				line += "+"
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func TestSitemap_ListLevels(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	for _, pair := range referencePairs {
		capturePair(t, repo, pair.url, pair.contentType, pair.status)
	}
	selection := &domain.SelectionQuery{Text: `status_code >= 500`, AsOf: newestID(t, repo)}
	capturePair(t, repo, "https://app.x.com/api/v2/users/5", "application/json", 500)
	capturePair(t, repo, "https://new.test/", "application/json", 500)
	nodes := []string{"", "https://app.x.com", "https://app.x.com/api", "https://app.x.com/api/v2"}
	read := func(t *testing.T, q domain.SitemapLevelsQuery) string {
		t.Helper()
		levels, err := repo.ListSitemapLevels(context.Background(), q)
		if err != nil {
			t.Fatalf("ListSitemapLevels(%+v): %v", q, err)
		}
		if !levels.Complete[domain.JobSitemap] {
			t.Fatalf("\nwanted:\na complete sitemap\ngot:\n%v", levels.Complete)
		}
		return formatLevels(levels)
	}

	t.Run("should list the matching children of every node, with their all-traffic counts", func(t *testing.T) {
		got := read(t, domain.SitemapLevelsQuery{Nodes: nodes, Scope: domain.SitemapScopeMatching, Exclusion: []string{"image/*"}, Selection: selection, Limit: domain.MaxSitemapPage})
		want := strings.Join([]string{
			"# ",
			"https://app.x.com 11/5 5/5+",
			"https://cdn.x.com 0/0 1/1+",
			"http://local.x.com:8080 2/1 1/1+",
			"https://other.test 1/1 1/1+",
			"# https://app.x.com",
			"https://app.x.com/api 9/5 4/4+",
			"https://app.x.com/static 1/0 1/1+",
			"# https://app.x.com/api",
			"https://app.x.com/api/v2 7/4 3/3+",
			"# https://app.x.com/api/v2",
			"https://app.x.com/api/v2/users 5/3 2/2+",
		}, "\n")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should list every child of every node, with its matching counts", func(t *testing.T) {
		got := read(t, domain.SitemapLevelsQuery{Nodes: nodes, Scope: domain.SitemapScopeAll, Exclusion: []string{"image/*"}, Selection: selection, Limit: domain.MaxSitemapPage})
		want := strings.Join([]string{
			"# ",
			"https://app.x.com 11/5 5/5+",
			"http://local.x.com:8080 2/1 1/1+",
			"https://other.test 1/1 1/1+",
			"https://new.test 1/1 0/0",
			"# https://app.x.com",
			"https://app.x.com/api 9/5 4/4+",
			"https://app.x.com/static 1/0 1/1+",
			"# https://app.x.com/api",
			"https://app.x.com/api/v2 7/4 3/3+",
			"https://app.x.com/api/v1 1/0 0/0+",
			"# https://app.x.com/api/v2",
			"https://app.x.com/api/v2/users 5/3 2/2+",
			"https://app.x.com/api/v2/orders 1/0 0/0",
		}, "\n")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should leave the matching counts unknown without a selection", func(t *testing.T) {
		got := read(t, domain.SitemapLevelsQuery{Nodes: []string{"https://app.x.com/api"}, Limit: 10})
		want := "# https://app.x.com/api\nhttps://app.x.com/api/v2 7/4 -+\nhttps://app.x.com/api/v1 1/0 -+"
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should mark a node that does not exist and still read the others", func(t *testing.T) {
		got := read(t, domain.SitemapLevelsQuery{Nodes: []string{"https://gone.test", "https://app.x.com/api"}, Scope: domain.SitemapScopeMatching, Selection: selection, Limit: 10})
		want := "# https://gone.test missing\n# https://app.x.com/api\nhttps://app.x.com/api/v2 7/4 3/3+"
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	for _, scope := range []domain.SitemapScope{domain.SitemapScopeMatching, domain.SitemapScopeAll} {
		t.Run(fmt.Sprintf("should page each node from its cursor in the %s scope", scope), func(t *testing.T) {
			q := domain.SitemapLevelsQuery{Nodes: []string{"", "https://app.x.com"}, Scope: scope, Selection: selection, Limit: 1}
			var pages []string
			for range 6 {
				levels, err := repo.ListSitemapLevels(context.Background(), q)
				if err != nil {
					t.Fatal(err)
				}
				pages = append(pages, formatLevels(levels))
				q.Cursors = map[string]string{}
				for _, level := range levels.Levels {
					if level.NextCursor != nil {
						q.Cursors[level.Node] = *level.NextCursor
					}
				}
				if len(q.Cursors) == 0 {
					break
				}
				q.Nodes = nil
				for _, node := range []string{"", "https://app.x.com"} {
					if _, ok := q.Cursors[node]; ok {
						q.Nodes = append(q.Nodes, node)
					}
				}
			}
			all := read(t, domain.SitemapLevelsQuery{Nodes: []string{"", "https://app.x.com"}, Scope: scope, Selection: selection, Limit: domain.MaxSitemapPage})
			if got, want := childLines(strings.Join(pages, "\n")), childLines(all); got != want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
			}
		})
	}

	for _, tt := range []struct {
		name string
		q    domain.SitemapLevelsQuery
		want error
	}{
		{"the matching scope without a selection", domain.SitemapLevelsQuery{Nodes: []string{""}, Scope: domain.SitemapScopeMatching, Limit: 1}, domain.ErrInvalidSitemapLevels},
		{"an unknown scope", domain.SitemapLevelsQuery{Nodes: []string{""}, Scope: "some", Limit: 1}, domain.ErrInvalidSitemapLevels},
		{"no nodes", domain.SitemapLevelsQuery{Limit: 1}, domain.ErrInvalidSitemapLevels},
		{"a malformed cursor", domain.SitemapLevelsQuery{Nodes: []string{""}, Cursors: map[string]string{"": "nope"}, Limit: 1}, domain.ErrInvalidCursor},
	} {
		t.Run("should reject "+tt.name, func(t *testing.T) {
			if _, err := repo.ListSitemapLevels(context.Background(), tt.q); !errors.Is(err, tt.want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, err)
			}
		})
	}

	t.Run("should reject an invalid query", func(t *testing.T) {
		q := domain.SitemapLevelsQuery{Nodes: []string{""}, Selection: &domain.SelectionQuery{Text: `host = `, AsOf: selection.AsOf}, Limit: 1}
		var queryErr *domain.QueryError
		if _, err := repo.ListSitemapLevels(context.Background(), q); !errors.As(err, &queryErr) {
			t.Fatalf("\nwanted:\na *domain.QueryError\ngot:\n%v", err)
		}
	})
}

// childLines keeps the child lines of formatted levels, grouped by node in
// first-seen node order.
func childLines(formatted string) string {
	var order []string
	byNode := map[string][]string{}
	node := ""
	for _, line := range strings.Split(formatted, "\n") {
		if strings.HasPrefix(line, "# ") {
			node = strings.TrimSuffix(strings.TrimPrefix(line, "# "), " more")
			if _, ok := byNode[node]; !ok {
				order = append(order, node)
				byNode[node] = nil
			}
			continue
		}
		byNode[node] = append(byNode[node], line)
	}
	var out []string
	for _, node := range order {
		out = append(out, "# "+node)
		out = append(out, byNode[node]...)
	}
	return strings.Join(out, "\n")
}
