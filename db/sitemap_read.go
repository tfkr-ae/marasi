package db

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// sitemapRow is one child of a sitemap read, before its segment is cut.
type sitemapRow struct {
	ID          int64  `db:"id"`
	Name        string `db:"name"`
	Pairs       int64  `db:"pairs"`
	Errors      int64  `db:"errors"`
	HasChildren bool   `db:"has_children"`
}

// ListSitemapChildren returns one page of the children of the sitemap node
// named q.Node, or of the origins when it is empty, by pair count, highest
// first. A child with no pairs counted is not listed, and q.Limit is capped
// at domain.MaxSitemapPage.
//
// Without q.Query it reads the stored counts, leaving out the pairs whose
// content type is in q.Exclusion. With q.Query it computes the counts
// exactly from the pairs that match the query and exclusion with an id at
// or below its as-of bound, grouped by the child their sitemap leaf sits
// under (ADR-0040). Pairs the sitemap job has not reached are not counted
// in either mode. The read stops when ctx ends.
//
// An unknown node returns domain.ErrSitemapNodeNotFound, an invalid query or
// exclusion a *domain.QueryError, a malformed cursor wraps
// domain.ErrInvalidCursor, and an exclusion outside the query in query mode
// wraps domain.ErrInvalidSelection.
func (repo *Repository) ListSitemapChildren(ctx context.Context, q domain.SitemapQuery) (domain.SitemapPage, error) {
	if q.Limit < 1 {
		return domain.SitemapPage{}, fmt.Errorf("listing sitemap children: limit %d is not positive", q.Limit)
	}
	if q.Query != nil && len(q.Exclusion) > 0 {
		return domain.SitemapPage{}, fmt.Errorf("%w: in query mode the exclusion belongs to the query", domain.ErrInvalidSelection)
	}
	levels := domain.SitemapLevelsQuery{Nodes: []string{q.Node}, Exclusion: q.Exclusion, Selection: q.Query, Limit: q.Limit}
	if q.Cursor != "" {
		levels.Cursors = map[string]string{q.Node: q.Cursor}
	}
	if q.Query != nil {
		levels.Scope = domain.SitemapScopeMatching
	}
	read, err := repo.listSitemapLevels(ctx, levels, false)
	if err != nil {
		return domain.SitemapPage{}, fmt.Errorf("listing sitemap children: %w", err)
	}
	level := read.Levels[0]
	if level.Missing {
		return domain.SitemapPage{}, fmt.Errorf("listing sitemap children: %w: %s", domain.ErrSitemapNodeNotFound, q.Node)
	}
	page := domain.SitemapPage{Children: make([]domain.SitemapChild, len(level.Children)), NextCursor: level.NextCursor, Complete: read.Complete}
	for i, child := range level.Children {
		page.Children[i] = child.SitemapChild
		if q.Query != nil {
			// Query mode counts the matching pairs.
			page.Children[i].Pairs, page.Children[i].Errors = *child.Matching, *child.MatchingErrors
		}
	}
	return page, nil
}

// ListSitemapLevels returns one page of the children of each node in
// q.Nodes, in that order. Every child carries its all-traffic counts,
// leaving out q.Exclusion, and, with q.Selection, its matching counts. The
// matching pairs are resolved once for the whole call: one indexed read of
// the selection, one sitemap leaf per match, then each leaf is placed under
// each node's child by its name. A node that does not exist comes back
// Missing. The read stops when ctx ends.
func (repo *Repository) ListSitemapLevels(ctx context.Context, q domain.SitemapLevelsQuery) (domain.SitemapLevels, error) {
	levels, err := repo.listSitemapLevels(ctx, q, true)
	if err != nil {
		return domain.SitemapLevels{}, fmt.Errorf("listing sitemap levels: %w", err)
	}
	return levels, nil
}

// listSitemapLevels is ListSitemapLevels. allTraffic false skips the
// all-traffic counts of matching-scope children, which query mode does not
// return.
func (repo *Repository) listSitemapLevels(ctx context.Context, q domain.SitemapLevelsQuery, allTraffic bool) (domain.SitemapLevels, error) {
	switch {
	case len(q.Nodes) == 0 || len(q.Nodes) > domain.MaxSitemapLevels:
		return domain.SitemapLevels{}, fmt.Errorf("%w: %d nodes, wanted 1 to %d", domain.ErrInvalidSitemapLevels, len(q.Nodes), domain.MaxSitemapLevels)
	case q.Scope != "" && q.Scope != domain.SitemapScopeAll && q.Scope != domain.SitemapScopeMatching:
		return domain.SitemapLevels{}, fmt.Errorf("%w: unknown scope %q", domain.ErrInvalidSitemapLevels, q.Scope)
	case q.Scope == domain.SitemapScopeMatching && q.Selection == nil:
		return domain.SitemapLevels{}, fmt.Errorf("%w: the matching scope needs a selection", domain.ErrInvalidSitemapLevels)
	case q.Limit < 1:
		return domain.SitemapLevels{}, fmt.Errorf("limit %d is not positive", q.Limit)
	}
	limit := min(q.Limit, domain.MaxSitemapPage)
	cursors := map[string]sitemapCursor{}
	for node, token := range q.Cursors {
		cursor, err := decodeSitemapCursor(token)
		if err != nil {
			return domain.SitemapLevels{}, err
		}
		cursors[node] = cursor
	}
	exclusion, err := domain.NormalizeExclusion(q.Exclusion)
	if err != nil {
		return domain.SitemapLevels{}, err
	}
	reader := sitemapReader{ctx: ctx, allTraffic: allTraffic}
	reader.kept, reader.keptArgs = exclusionCondition(exclusion)
	if reader.kept == "" {
		reader.kept = "1"
	}
	jobs := []domain.JobName{domain.JobSitemap}
	var match *trafficMatch
	if q.Selection != nil {
		m, err := newTrafficMatch(q.Selection.Text, q.Selection.Exclusion)
		if err != nil {
			return domain.SitemapLevels{}, err
		}
		if err := repo.indexReadyFor(m); err != nil {
			return domain.SitemapLevels{}, err
		}
		m.add(`id <= ?`, q.Selection.AsOf)
		match = &m
		jobs = append(jobs, m.jobs()...)
	}

	result := domain.SitemapLevels{Levels: make([]domain.SitemapLevel, len(q.Nodes))}
	err = repo.inTxContext(ctx, func(tx *sqlx.Tx) error {
		reader.tx = tx
		complete, err := jobCompleteness(tx, jobs...)
		if err != nil {
			return err
		}
		result.Complete = complete
		if match != nil {
			if reader.leaves, err = matchedLeaves(ctx, tx, *match); err != nil {
				return fmt.Errorf("resolving the selection: %w", err)
			}
		}
		for i, node := range q.Nodes {
			var after *sitemapCursor
			if cursor, ok := cursors[node]; ok {
				after = &cursor
			}
			level, err := reader.level(node, q.Scope == domain.SitemapScopeMatching, match != nil, after, limit)
			if err != nil {
				return fmt.Errorf("reading %q: %w", node, err)
			}
			result.Levels[i] = level
		}
		return nil
	})
	if err != nil {
		return domain.SitemapLevels{}, err
	}
	return result, nil
}

// matchedLeaf is a sitemap leaf with the pairs a selection matches there.
type matchedLeaf struct {
	Name   string
	Pairs  int64
	Errors int64
}

// matchedLeaves resolves the pairs match selects and counts them per sitemap
// leaf, in one read linear in the matches. The CROSS JOINs fix the join
// order: left to choose, the planner walks every pair_sitemap row in node
// order to skip sorting the GROUP BY, and looks each pair up in request,
// which reads the whole project for a query that matches a few pairs.
func matchedLeaves(ctx context.Context, tx *sqlx.Tx, match trafficMatch) ([]matchedLeaf, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.name, l.pairs, l.errors
		FROM (
			SELECT ps.node_id AS leaf, count(*) AS pairs, sum(coalesce(m.status_code, 0) >= 500) AS errors
			FROM (SELECT id, status_code FROM request`+match.where()+`) m
			CROSS JOIN pair_sitemap ps ON ps.request_id = m.id
			GROUP BY ps.node_id) l
		CROSS JOIN sitemap_node n ON n.id = l.leaf`, match.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Scanned by hand: a broad selection has a leaf per few pairs, and
	// sqlx's reflection cost more than placing them.
	var leaves []matchedLeaf
	for rows.Next() {
		var leaf matchedLeaf
		if err := rows.Scan(&leaf.Name, &leaf.Pairs, &leaf.Errors); err != nil {
			return nil, err
		}
		leaves = append(leaves, leaf)
	}
	return leaves, rows.Err()
}

// sitemapChildOf returns the name of the child of node, "" for the origins,
// that the node named leaf is in the subtree of, and whether leaf is below
// that child. ok is false when leaf is not below node. Names are canonical
// paths, and a segment never holds a literal slash, so the child's name is
// leaf's name cut after the segment that follows node's.
func sitemapChildOf(node, leaf string) (child string, below, ok bool) {
	start := len(node) + 1
	if node == "" {
		scheme := strings.Index(leaf, "://")
		if scheme < 0 {
			return "", false, false
		}
		start = scheme + len("://")
	} else if len(leaf) <= start || !strings.HasPrefix(leaf, node) || leaf[len(node)] != '/' {
		return "", false, false
	}
	slash := strings.IndexByte(leaf[start:], '/')
	if slash < 0 {
		return leaf, false, true
	}
	return leaf[:start+slash], true, true
}

// matchedChild is the matching pairs in one child's subtree.
type matchedChild struct {
	pairs, errors int64
	hasChildren   bool
}

// sitemapReader reads the levels of one sitemap levels read in one
// transaction.
type sitemapReader struct {
	ctx context.Context
	tx  *sqlx.Tx
	// kept is the condition on sitemap_count that leaves out the
	// all-traffic exclusion, with its arguments.
	kept     string
	keptArgs []any
	// leaves are the selection's matching pairs per leaf; nil without one.
	leaves     []matchedLeaf
	allTraffic bool
}

// level reads one page of node's children: by matching pairs when
// matchingScope, else by all-traffic pairs, with matching counts when
// selected.
func (r *sitemapReader) level(node string, matchingScope, selected bool, after *sitemapCursor, limit int) (domain.SitemapLevel, error) {
	level := domain.SitemapLevel{Node: node, Children: []domain.SitemapLevelChild{}}
	var parent int64 // origins have parent 0 in sitemap_count
	if node != "" {
		err := r.tx.GetContext(r.ctx, &parent, `SELECT id FROM sitemap_node WHERE name = ?`, node)
		if errors.Is(err, sql.ErrNoRows) {
			level.Missing = true
			return level, nil
		}
		if err != nil {
			return level, err
		}
	}
	var matched map[string]*matchedChild
	if selected {
		matched = r.matchedChildren(node)
	}

	var rows []sitemapRow
	var err error
	if matchingScope {
		rows, err = r.matchingPage(matched, after, limit)
	} else {
		rows, err = r.storedPage(parent, after, limit)
	}
	if err != nil {
		return level, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := sitemapCursor{Version: 1, Pairs: last.Pairs, ID: last.ID}.encode()
		level.NextCursor = &next
	}
	var stored map[int64]sitemapRow
	if matchingScope && r.allTraffic && len(rows) > 0 {
		if stored, err = r.storedCounts(parent, rows); err != nil {
			return level, err
		}
	}
	for _, row := range rows {
		segment := row.Name
		if node != "" {
			segment = row.Name[len(node)+1:]
		}
		child := domain.SitemapLevelChild{SitemapChild: domain.SitemapChild{Name: row.Name, Segment: segment, HasChildren: row.HasChildren}}
		if matchingScope {
			pairs, errs := row.Pairs, row.Errors
			child.Matching, child.MatchingErrors = &pairs, &errs
			child.Pairs, child.Errors = stored[row.ID].Pairs, stored[row.ID].Errors
		} else {
			child.Pairs, child.Errors = row.Pairs, row.Errors
			if selected {
				var pairs, errs int64
				if m := matched[row.Name]; m != nil {
					pairs, errs = m.pairs, m.errors
				}
				child.Matching, child.MatchingErrors = &pairs, &errs
			}
		}
		level.Children = append(level.Children, child)
	}
	return level, nil
}

// matchedChildren places each matching leaf under node's child it sits
// under, keyed by the child's name.
func (r *sitemapReader) matchedChildren(node string) map[string]*matchedChild {
	children := map[string]*matchedChild{}
	for _, leaf := range r.leaves {
		name, below, ok := sitemapChildOf(node, leaf.Name)
		if !ok {
			continue
		}
		child := children[name]
		if child == nil {
			child = &matchedChild{}
			children[name] = child
		}
		child.pairs += leaf.Pairs
		child.errors += leaf.Errors
		child.hasChildren = child.hasChildren || below
	}
	return children
}

// matchingPage returns up to limit+1 of the matched children after the
// cursor, by matching pairs, highest first, then by node id.
func (r *sitemapReader) matchingPage(matched map[string]*matchedChild, after *sitemapCursor, limit int) ([]sitemapRow, error) {
	if len(matched) == 0 {
		return nil, nil
	}
	names, _ := json.Marshal(slices.Collect(maps.Keys(matched)))
	var nodes []struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	err := r.tx.SelectContext(r.ctx, &nodes, `SELECT n.id, n.name FROM json_each(?) AS j
		CROSS JOIN sitemap_node n ON n.name = j.value`, string(names))
	if err != nil {
		return nil, err
	}
	rows := make([]sitemapRow, 0, len(nodes))
	for _, node := range nodes {
		m := matched[node.Name]
		row := sitemapRow{ID: node.ID, Name: node.Name, Pairs: m.pairs, Errors: m.errors, HasChildren: m.hasChildren}
		if after == nil || row.Pairs < after.Pairs || (row.Pairs == after.Pairs && row.ID > after.ID) {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b sitemapRow) int {
		return cmp.Or(cmp.Compare(b.Pairs, a.Pairs), cmp.Compare(a.ID, b.ID))
	})
	return rows[:min(len(rows), limit+1)], nil
}

// storedPage returns up to limit+1 of parent's children after the cursor,
// from the stored counts, leaving out the exclusion. sitemap_count is
// clustered by parent, so the children's buckets are one range read.
func (r *sitemapReader) storedPage(parent int64, after *sitemapCursor, limit int) ([]sitemapRow, error) {
	args := append(append([]any{}, r.keptArgs...), parent)
	args = append(args, r.keptArgs...)
	bound := `pairs > 0`
	if after != nil {
		bound += ` AND (pairs < ? OR (pairs = ? AND id > ?))`
		args = append(args, after.Pairs, after.Pairs, after.ID)
	}
	args = append(args, limit+1)
	// Names, and has_children, are read for the page only.
	var rows []sitemapRow
	err := r.tx.SelectContext(r.ctx, &rows, `SELECT p.id, n.name, p.pairs, p.errors,
			EXISTS (SELECT 1 FROM sitemap_count c WHERE c.parent_id = p.id AND c.pairs > 0 AND (`+r.kept+`)) AS has_children
		FROM (SELECT * FROM (
				SELECT node_id AS id, sum(pairs) AS pairs, sum(errors) AS errors
				FROM sitemap_count WHERE parent_id = ? AND (`+r.kept+`)
				GROUP BY node_id)
			WHERE `+bound+` ORDER BY pairs DESC, id LIMIT ?) p
		JOIN sitemap_node n ON n.id = p.id
		ORDER BY p.pairs DESC, p.id`, args...)
	return rows, err
}

// storedCounts returns the stored all-traffic counts of the listed children
// of parent, leaving out the exclusion, by node id.
func (r *sitemapReader) storedCounts(parent int64, children []sitemapRow) (map[int64]sitemapRow, error) {
	ids := make([]int64, len(children))
	for i, child := range children {
		ids[i] = child.ID
	}
	encoded, _ := json.Marshal(ids)
	var rows []sitemapRow
	err := r.tx.SelectContext(r.ctx, &rows, `SELECT c.node_id AS id, sum(c.pairs) AS pairs, sum(c.errors) AS errors
		FROM json_each(?) AS j
		CROSS JOIN sitemap_count c ON c.parent_id = ? AND c.node_id = j.value
		WHERE `+r.kept+`
		GROUP BY c.node_id`, append([]any{string(encoded), parent}, r.keptArgs...)...)
	counts := make(map[int64]sitemapRow, len(rows))
	for _, row := range rows {
		counts[row.ID] = row
	}
	return counts, err
}
