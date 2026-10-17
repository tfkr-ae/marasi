package db

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// referenceQueriedChildren is the query-mode read from before ticket 24: one
// SQL statement that counts the matching pairs per leaf and maps each leaf to
// the node's child by cutting its name. It is slow, because the planner
// drives it from pair_sitemap, but its answers are the ones the fast read
// must keep. It returns every child, by pair count, highest first, formatted
// as "name pairs/errors", with a + when the child has matching children.
func referenceQueriedChildren(t *testing.T, repo *Repository, node string, query domain.SelectionQuery) []string {
	t.Helper()
	match, err := newTrafficMatch(query.Text, query.Exclusion)
	if err != nil {
		t.Fatalf("newTrafficMatch(%q): %v", query.Text, err)
	}
	if err := repo.indexReadyFor(match); err != nil {
		t.Fatal(err)
	}
	match.add(`id <= ?`, query.AsOf)
	args := append([]any{}, match.args...)
	prefix := `(instr(ln.name, '://') + 2)`
	subtree := `1`
	if node != "" {
		prefix = strconv.Itoa(utf8.RuneCountInString(node) + 1)
		subtree = `ln.name >= ? AND ln.name < ?`
		args = append(args, node+"/", node+"0")
	}
	slash := `instr(substr(ln.name, ` + prefix + ` + 1), '/')`
	child := `CASE WHEN ` + slash + ` = 0 THEN ln.name ELSE substr(ln.name, 1, ` + prefix + ` + ` + slash + ` - 1) END`
	var rows []struct {
		Name        string `db:"name"`
		Pairs       int64  `db:"pairs"`
		Errors      int64  `db:"errors"`
		HasChildren bool   `db:"has_children"`
	}
	err = repo.dbConn.Select(&rows, `SELECT c.name, sum(l.pairs) AS pairs, sum(l.errors) AS errors, max(l.leaf <> c.id) AS has_children
		FROM (
			SELECT ps.node_id AS leaf, count(*) AS pairs, sum(coalesce(m.status_code, 0) >= 500) AS errors
			FROM (SELECT id, status_code FROM request`+match.where()+`) m
			JOIN pair_sitemap ps ON ps.request_id = m.id
			GROUP BY ps.node_id) l
		JOIN sitemap_node ln ON ln.id = l.leaf
		JOIN sitemap_node c ON c.name = `+child+`
		WHERE `+subtree+`
		GROUP BY c.id
		ORDER BY pairs DESC, c.id`, args...)
	if err != nil {
		t.Fatalf("reference read of %q: %v", node, err)
	}
	formatted := make([]string, len(rows))
	for i, row := range rows {
		formatted[i] = fmt.Sprintf("%s %d/%d", row.Name, row.Pairs, row.Errors)
		if row.HasChildren {
			formatted[i] += "+"
		}
	}
	return formatted
}

// referencePairs is traffic for the reference comparison: several origins,
// a deep tree, placeholders, every status class, in-flight pairs and pairs
// sitting on inner nodes.
var referencePairs = []struct {
	url         string
	contentType string
	status      int
}{
	{"https://app.x.com/", "text/html", 200},
	{"https://app.x.com/api", "application/json", 500},
	{"https://app.x.com/api/v2/users/1", "application/json", 200},
	{"https://app.x.com/api/v2/users/2", "application/json", 503},
	{"https://app.x.com/api/v2/users/3/orders", "application/json", 502},
	{"https://app.x.com/api/v2/orders", "application/json", 404},
	{"https://app.x.com/api/v2", "application/json", 500},
	{"https://app.x.com/api/v1/search?q=son", "application/json", 200},
	{"https://app.x.com/static/app.js", "application/javascript", 200},
	{"https://app.x.com/static/logo.png", "image/png", 500},
	{"https://app.x.com/api/v2/users/4", "application/json", 0},
	{"https://cdn.x.com/a/b/c/d", "image/png", 200},
	{"https://cdn.x.com/a/b", "image/jpeg", 501},
	{"http://local.x.com:8080/2024/{id}", "text/plain", 500},
	{"http://local.x.com:8080/", "", 0},
	{"https://other.test/api/v2/users/9", "application/json", 500},
}

func TestSitemap_QueryModeAgreesWithTheReference(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	var ids []uuid.UUID
	for round := range 3 {
		for _, pair := range referencePairs {
			status := pair.status
			if status != 0 && round == 1 {
				status += 1
			}
			ids = append(ids, capturePair(t, repo, pair.url, pair.contentType, status))
		}
	}
	nodes := []string{""}
	var all []string
	if err := repo.dbConn.Select(&all, `SELECT name FROM sitemap_node ORDER BY name`); err != nil {
		t.Fatal(err)
	}
	nodes = append(nodes, all...)

	queries := []domain.SelectionQuery{
		{},
		{Text: `status_code >= 500`},
		{Text: `status_code >= 500`, Exclusion: []string{"image/*"}},
		{Text: `host = "app.x.com"`},
		{Text: `host = "cdn.x.com"`},
		{Text: `content_type = "application/json"`},
		{Text: `son`},
		{Text: `sitemap = "https://app.x.com/api/v2"`},
		{Text: `sitemap = "https://app.x.com/api" AND status_code < 500`},
		{Text: `NOT host = "app.x.com"`, Exclusion: []string{"text/plain"}},
		{Exclusion: []string{"application/json", "image/*"}},
		{Text: `host = "nowhere.test"`},
	}
	for _, query := range queries {
		for _, asOf := range []uuid.UUID{ids[len(ids)-1], ids[len(ids)/2], ids[0]} {
			query.AsOf = asOf
			t.Run(fmt.Sprintf("should count %q excluding %v up to %s as the reference does", query.Text, query.Exclusion, asOf), func(t *testing.T) {
				for _, node := range nodes {
					want := strings.Join(referenceQueriedChildren(t, repo, node, query), "\n")
					if got := strings.Join(pagedQueryChildren(t, repo, node, query), "\n"); got != want {
						t.Errorf("node %q\nwanted:\n%s\ngot:\n%s", node, want, got)
					}
				}
			})
		}
	}
}

// pagedQueryChildren reads every child of node in query mode, two to a page,
// formatted as referenceQueriedChildren formats them.
func pagedQueryChildren(t *testing.T, repo *Repository, node string, query domain.SelectionQuery) []string {
	t.Helper()
	var formatted []string
	cursor := ""
	for {
		page, err := repo.ListSitemapChildren(context.Background(), domain.SitemapQuery{Node: node, Query: &query, Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("ListSitemapChildren(%q, %+v): %v", node, query, err)
		}
		for _, child := range page.Children {
			line := fmt.Sprintf("%s %d/%d", child.Name, child.Pairs, child.Errors)
			if child.HasChildren {
				line += "+"
			}
			formatted = append(formatted, line)
		}
		if page.NextCursor == nil {
			return formatted
		}
		cursor = *page.NextCursor
	}
}
