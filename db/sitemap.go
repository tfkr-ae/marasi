package db

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// SitemapJob is the background job that adds pairs without a sitemap link to
// the stored sitemap (ADR-0040).
const SitemapJob = domain.JobSitemap

// sitemapBatchPairs is how many pairs one batch of the sitemap job adds.
const sitemapBatchPairs = 2000

// sitemapJob registers the sitemap with the background job runner. Its
// version is the version of the rules, so new rules rebuild the tree.
var sitemapJob = jobDefinition{
	name:    SitemapJob,
	version: domain.SitemapRulesVersion,
	batch: func(repo *Repository, _ int) (int, bool, error) {
		return repo.AddMissingToSitemap(sitemapBatchPairs)
	},
	discard: func(tx *sqlx.Tx) error {
		for _, table := range []string{"pair_sitemap", "sitemap_count", "sitemap_node"} {
			if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
				return err
			}
		}
		return nil
	},
	forget:      func(repo *Repository) { repo.sitemapFrom = nil },
	rebuildable: true,
}

// ancestry is a recursive CTE over a node and its ancestors, from the node
// bound to its ?, with each one's parent: 0 for an origin.
const ancestry = `WITH RECURSIVE up(id, parent) AS (
	SELECT id, coalesce(parent_id, 0) FROM sitemap_node WHERE id = ? UNION ALL
	SELECT n.id, coalesce(n.parent_id, 0) FROM sitemap_node n JOIN up ON n.id = up.parent)`

// sitemapBucket is where a pair is counted: its content type, "" when it has
// none, and whether its response is a server error.
type sitemapBucket struct {
	contentType string
	isError     bool
}

func bucketOf(contentType sql.NullString, statusCode sql.NullInt64) sitemapBucket {
	return sitemapBucket{contentType: contentType.String, isError: statusCode.Int64 >= 500}
}

// linkPairToSitemap adds a pair to the stored sitemap from its stored scheme,
// host and path: it creates the nodes along its path, links the pair to its
// leaf, and counts it in bucket at the leaf and every ancestor. Capture and
// the sitemap job both use it, so they build the same tree.
func linkPairToSitemap(tx *sqlx.Tx, id uuid.UUID, scheme, host, path string, bucket sitemapBucket) error {
	leaf, err := sitemapNodeID(tx, domain.SitemapPath(scheme, host, path))
	if err != nil {
		return fmt.Errorf("adding pair %s to the sitemap: %w", id, err)
	}
	if _, err := tx.Exec(`INSERT INTO pair_sitemap (request_id, node_id) VALUES (?, ?)`, id, leaf); err != nil {
		return fmt.Errorf("linking pair %s to the sitemap: %w", id, err)
	}
	return countInSitemap(tx, leaf, bucket, 1)
}

// sitemapNodeID returns the id of the last node in nodes, creating any of
// them that do not exist. A leaf that exists has all of its ancestors.
func sitemapNodeID(tx *sqlx.Tx, nodes []domain.SitemapNode) (int64, error) {
	var id int64
	err := tx.Get(&id, `SELECT id FROM sitemap_node WHERE name = ?`, nodes[len(nodes)-1].Name)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	var parent sql.NullInt64
	for depth, node := range nodes {
		err := tx.Get(&id, `INSERT INTO sitemap_node (parent_id, name, depth) VALUES (?, ?, ?)
			ON CONFLICT (name) DO UPDATE SET name = excluded.name RETURNING id`, parent, node.Name, depth)
		if err != nil {
			return 0, err
		}
		parent = sql.NullInt64{Int64: id, Valid: true}
	}
	return id, nil
}

// countInSitemap adds delta pairs to bucket at node and every ancestor, and
// removes buckets that reach zero.
func countInSitemap(tx *sqlx.Tx, node int64, bucket sitemapBucket, delta int) error {
	errorDelta := 0
	if bucket.isError {
		errorDelta = delta
	}
	_, err := tx.Exec(ancestry+`
		INSERT INTO sitemap_count (parent_id, node_id, content_type, pairs, errors)
		SELECT parent, id, ?, ?, ? FROM up WHERE true
		ON CONFLICT (parent_id, node_id, content_type) DO UPDATE SET
			pairs = pairs + excluded.pairs, errors = errors + excluded.errors`,
		node, bucket.contentType, delta, errorDelta)
	if err == nil && delta < 0 {
		_, err = tx.Exec(ancestry+`
			DELETE FROM sitemap_count WHERE content_type = ? AND pairs <= 0
				AND (parent_id, node_id) IN (SELECT parent, id FROM up)`,
			node, bucket.contentType)
	}
	if err != nil {
		return fmt.Errorf("counting in the sitemap: %w", err)
	}
	return nil
}

// sitemapState reads the pair's sitemap leaf and its current bucket, in tx.
// The leaf is invalid when the pair is not in the sitemap yet.
func sitemapState(tx *sqlx.Tx, id uuid.UUID) (sql.NullInt64, sitemapBucket, error) {
	var row struct {
		Node        sql.NullInt64  `db:"node_id"`
		ContentType sql.NullString `db:"content_type"`
		StatusCode  sql.NullInt64  `db:"status_code"`
	}
	err := tx.Get(&row, `SELECT ps.node_id, r.content_type, r.status_code
		FROM request r LEFT JOIN pair_sitemap ps ON ps.request_id = r.id WHERE r.id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullInt64{}, sitemapBucket{}, nil
	}
	return row.Node, bucketOf(row.ContentType, row.StatusCode), err
}

// rebucketInSitemap moves a linked pair from bucket from to bucket to.
func rebucketInSitemap(tx *sqlx.Tx, node int64, from, to sitemapBucket) error {
	if from == to {
		return nil
	}
	if err := countInSitemap(tx, node, to, 1); err != nil {
		return err
	}
	return countInSitemap(tx, node, from, -1)
}

// AddMissingToSitemap runs one batch of the sitemap job: it adds up to
// maxPairs pairs that have no sitemap link, newest first, in one transaction,
// and reports whether any remain. The batch records its progress in the saved
// job state, and once nothing remains the job is complete and later batches
// do nothing.
func (repo *Repository) AddMissingToSitemap(maxPairs int) (int, bool, error) {
	repo.jobMu.Lock()
	defer repo.jobMu.Unlock()

	var pairs []struct {
		ID          uuid.UUID      `db:"id"`
		Scheme      string         `db:"scheme"`
		Host        string         `db:"host"`
		Path        string         `db:"path"`
		ContentType sql.NullString `db:"content_type"`
		StatusCode  sql.NullInt64  `db:"status_code"`
	}
	var remaining bool
	err := repo.inTx(func(tx *sqlx.Tx) error {
		job, err := loadJob(tx, SitemapJob)
		if err != nil {
			return fmt.Errorf("reading the sitemap job: %w", err)
		}
		if job.Complete {
			return nil
		}
		// Pairs at or above sitemapFrom are linked: earlier batches linked
		// them, or this repository wrote them.
		query := `SELECT id, scheme, host, path, content_type, status_code FROM request r
			WHERE NOT EXISTS (SELECT 1 FROM pair_sitemap ps WHERE ps.request_id = r.id)`
		args := []any{}
		if repo.sitemapFrom != nil {
			query += ` AND id < ?`
			args = append(args, *repo.sitemapFrom)
		}
		if err := tx.Select(&pairs, query+` ORDER BY id DESC LIMIT ?`, append(args, maxPairs+1)...); err != nil {
			return fmt.Errorf("finding pairs missing from the sitemap: %w", err)
		}
		remaining = len(pairs) > maxPairs
		if remaining {
			pairs = pairs[:maxPairs]
		}
		for _, pair := range pairs {
			err := linkPairToSitemap(tx, pair.ID, pair.Scheme, pair.Host, pair.Path, bucketOf(pair.ContentType, pair.StatusCode))
			if err != nil {
				return err
			}
		}
		return advanceJob(tx, SitemapJob, len(pairs), remaining)
	})
	if err != nil {
		return 0, false, fmt.Errorf("adding missing traffic to the sitemap: %w", err)
	}
	if remaining {
		repo.sitemapFrom = &pairs[len(pairs)-1].ID
	}
	return len(pairs), remaining, nil
}

// sitemapCursor is the decoded form of an opaque sitemap cursor: the pair
// count and node id of the last child on the page.
type sitemapCursor struct {
	Version int   `json:"v"`
	Pairs   int64 `json:"p"`
	ID      int64 `json:"id"`
}

func (c sitemapCursor) encode() string {
	raw, _ := json.Marshal(c) // a struct of plain fields cannot fail to marshal
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSitemapCursor(token string) (sitemapCursor, error) {
	var cursor sitemapCursor
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err == nil {
		err = json.Unmarshal(raw, &cursor)
	}
	if err != nil || cursor.Version != 1 || cursor.ID < 1 {
		return sitemapCursor{}, fmt.Errorf("%w: not a sitemap cursor", domain.ErrInvalidCursor)
	}
	return cursor, nil
}

// ListSitemapChildren returns one page of the children of the sitemap node
// named node, or of the origins when node is empty, by pair count, highest
// first. Counts leave out the pairs whose content type is in exclusion, and a
// child with no pairs left is not listed. limit is capped at
// domain.MaxSitemapPage. An unknown node returns domain.ErrSitemapNodeNotFound,
// an invalid exclusion a *domain.QueryError, and a malformed cursor wraps
// domain.ErrInvalidCursor.
func (repo *Repository) ListSitemapChildren(node string, exclusion []string, cursor string, limit int) (domain.SitemapPage, error) {
	if limit < 1 {
		return domain.SitemapPage{}, fmt.Errorf("listing sitemap children: limit %d is not positive", limit)
	}
	limit = min(limit, domain.MaxSitemapPage)
	normalized, err := domain.NormalizeExclusion(exclusion)
	if err != nil {
		return domain.SitemapPage{}, err
	}
	var after *sitemapCursor
	if cursor != "" {
		decoded, err := decodeSitemapCursor(cursor)
		if err != nil {
			return domain.SitemapPage{}, err
		}
		after = &decoded
	}
	kept, keptArgs := exclusionCondition(normalized)
	if kept == "" {
		kept = "1"
	}

	var rows []struct {
		ID          int64  `db:"id"`
		Name        string `db:"name"`
		Pairs       int64  `db:"pairs"`
		Errors      int64  `db:"errors"`
		HasChildren bool   `db:"has_children"`
	}
	page := domain.SitemapPage{Children: []domain.SitemapChild{}}
	err = repo.inTx(func(tx *sqlx.Tx) error {
		job, err := loadJob(tx, SitemapJob)
		if err != nil {
			return fmt.Errorf("reading the sitemap job: %w", err)
		}
		page.Complete = map[string]bool{SitemapJob: job.Complete}

		var parent int64 // origins have parent 0 in sitemap_count
		if node != "" {
			err := tx.Get(&parent, `SELECT id FROM sitemap_node WHERE name = ?`, node)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", domain.ErrSitemapNodeNotFound, node)
			}
			if err != nil {
				return err
			}
		}
		// sitemap_count is clustered by parent, so the children's buckets
		// are one range read. Names are read for the page only.
		args := append([]any{}, keptArgs...)
		args = append(args, parent)
		args = append(args, keptArgs...)
		bound := `pairs > 0`
		if after != nil {
			bound += ` AND (pairs < ? OR (pairs = ? AND id > ?))`
			args = append(args, after.Pairs, after.Pairs, after.ID)
		}
		args = append(args, limit+1)
		query := `SELECT p.id, n.name, p.pairs, p.errors,
			EXISTS (SELECT 1 FROM sitemap_count c
				WHERE c.parent_id = p.id AND c.pairs > 0 AND (` + kept + `)) AS has_children
			FROM (
				SELECT id, pairs, errors FROM (
					SELECT node_id AS id, sum(pairs) AS pairs, sum(errors) AS errors
					FROM sitemap_count
					WHERE parent_id = ? AND (` + kept + `)
					GROUP BY node_id)
				WHERE ` + bound + `
				ORDER BY pairs DESC, id LIMIT ?) p
			JOIN sitemap_node n ON n.id = p.id
			ORDER BY p.pairs DESC, p.id`
		return tx.Select(&rows, query, args...)
	})
	if err != nil {
		return domain.SitemapPage{}, fmt.Errorf("listing sitemap children: %w", err)
	}

	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := sitemapCursor{Version: 1, Pairs: last.Pairs, ID: last.ID}.encode()
		page.NextCursor = &next
	}
	for _, row := range rows {
		segment := row.Name
		if node != "" {
			segment = row.Name[len(node)+1:]
		}
		page.Children = append(page.Children, domain.SitemapChild{
			Name: row.Name, Segment: segment, Pairs: row.Pairs, Errors: row.Errors, HasChildren: row.HasChildren,
		})
	}
	return page, nil
}
