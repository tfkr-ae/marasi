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

// sitemapBatchPairs is how many pairs one batch of the sitemap job adds.
const sitemapBatchPairs = 2000

// sitemapJob registers the sitemap with the background job runner. Its
// version is the version of the rules, so new rules rebuild the tree.
var sitemapJob = jobDefinition{
	name:    domain.JobSitemap,
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

// sitemapEdge is a node and its parent: 0 for an origin.
type sitemapEdge struct {
	parent, node int64
}

// sitemapAncestry returns node and each of its ancestors, leaf first, with
// their parents. It walks the tree in Go, one primary-key read per level: a
// recursive CTE builds an ephemeral table for its queue, and allocating that
// table's page cache on every capture cost more than the counts it updated.
func sitemapAncestry(tx *sqlx.Tx, node int64) ([]sitemapEdge, error) {
	var chain []sitemapEdge
	for node != 0 {
		var parent int64
		if err := tx.Get(&parent, `SELECT coalesce(parent_id, 0) FROM sitemap_node WHERE id = ?`, node); err != nil {
			return nil, fmt.Errorf("reading sitemap node %d: %w", node, err)
		}
		chain = append(chain, sitemapEdge{parent: parent, node: node})
		node = parent
	}
	return chain, nil
}

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
	chain, err := sitemapNodes(tx, domain.SitemapPath(scheme, host, path))
	if err != nil {
		return fmt.Errorf("adding pair %s to the sitemap: %w", id, err)
	}
	if _, err := tx.Exec(`INSERT INTO pair_sitemap (request_id, node_id) VALUES (?, ?)`, id, chain[0].node); err != nil {
		return fmt.Errorf("linking pair %s to the sitemap: %w", id, err)
	}
	return countInSitemap(tx, chain, bucket, 1)
}

// sitemapNodes returns the last node in nodes and its ancestors, leaf first,
// as sitemapAncestry does, creating any of them that do not exist. A node
// that exists has all of its ancestors, so only the nodes below the deepest
// existing one are written.
func sitemapNodes(tx *sqlx.Tx, nodes []domain.SitemapNode) ([]sitemapEdge, error) {
	var existing int64
	missing := len(nodes)
	for missing > 0 {
		err := tx.Get(&existing, `SELECT id FROM sitemap_node WHERE name = ?`, nodes[missing-1].Name)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		missing--
	}
	chain, err := sitemapAncestry(tx, existing)
	if err != nil {
		return nil, err
	}
	parent := existing
	for depth := missing; depth < len(nodes); depth++ {
		var id int64
		err := tx.Get(&id, `INSERT INTO sitemap_node (parent_id, name, depth) VALUES (nullif(?, 0), ?, ?) RETURNING id`,
			parent, nodes[depth].Name, depth)
		if err != nil {
			return nil, err
		}
		chain = append([]sitemapEdge{{parent: parent, node: id}}, chain...)
		parent = id
	}
	return chain, nil
}

// countInSitemap adds delta pairs to bucket at each node of chain, from
// sitemapAncestry, and removes buckets that reach zero.
func countInSitemap(tx *sqlx.Tx, chain []sitemapEdge, bucket sitemapBucket, delta int) error {
	errorDelta := 0
	if bucket.isError {
		errorDelta = delta
	}
	// One single-row statement per node: a statement that can change more
	// than one row opens a statement journal, whose allocation cost more
	// per capture than the rows it changed.
	for _, edge := range chain {
		_, err := tx.Exec(`INSERT INTO sitemap_count (parent_id, node_id, content_type, pairs, errors)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (parent_id, node_id, content_type) DO UPDATE SET
				pairs = pairs + excluded.pairs, errors = errors + excluded.errors`,
			edge.parent, edge.node, bucket.contentType, delta, errorDelta)
		if err == nil && delta < 0 {
			_, err = tx.Exec(`DELETE FROM sitemap_count
				WHERE parent_id = ? AND node_id = ? AND content_type = ? AND pairs <= 0`,
				edge.parent, edge.node, bucket.contentType)
		}
		if err != nil {
			return fmt.Errorf("counting in the sitemap: %w", err)
		}
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
	chain, err := sitemapAncestry(tx, node)
	if err != nil {
		return err
	}
	if err := countInSitemap(tx, chain, to, 1); err != nil {
		return err
	}
	return countInSitemap(tx, chain, from, -1)
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
		job, err := loadJob(tx, domain.JobSitemap)
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
		return advanceJob(tx, domain.JobSitemap, len(pairs), remaining)
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
