package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// DeleteTraffic deletes every pair in sel, and everything each one owns, in
// one transaction, and returns the number of pairs deleted (ADR-0034).
//
// The contentless search indexes have no foreign key, so their rows go
// first, by index key; the pairs' sitemap counts are then taken off their
// leaf and every ancestor. Deleting the request rows cascades to the rest:
// notes, launchpad, finding, test case and Armory links, WebSocket
// connections and messages, proxy logs, the highlight, the prettified
// bodies, the sitemap link and the index key. A sitemap node left with no
// pairs stays until the next rebuild.
func (repo *Repository) DeleteTraffic(sel domain.Selection) (int, error) {
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		// The selection is resolved once into temp.deleting, so a query
		// selection is not evaluated again by each step.
		steps := []struct {
			what, query string
			args        []any
		}{
			{"preparing the delete", `CREATE TEMP TABLE IF NOT EXISTS deleting (id TEXT PRIMARY KEY) WITHOUT ROWID`, nil},
			{"preparing the delete", `DELETE FROM temp.deleting`, nil},
			{"resolving the pairs to delete", `INSERT INTO temp.deleting (id) ` + selected, args},
			{"deleting body index rows", `DELETE FROM traffic_index WHERE rowid IN
				(SELECT k.id FROM temp.deleting d JOIN traffic_index_key k ON k.request_id = d.id)`, nil},
			{"deleting annotation index rows", `DELETE FROM traffic_annotation_index WHERE rowid IN
				(SELECT k.id FROM temp.deleting d JOIN traffic_index_key k ON k.request_id = d.id)`, nil},
			{"taking deleted pairs off the sitemap", uncountDeleted, nil},
			{"taking deleted pairs off the sitemap", `DELETE FROM sitemap_count WHERE pairs <= 0`, nil},
		}
		for _, step := range steps {
			if _, err := tx.Exec(step.query, step.args...); err != nil {
				return 0, fmt.Errorf("%s: %w", step.what, err)
			}
		}
		return execCount(tx, "deleting traffic", `DELETE FROM request WHERE id IN (SELECT id FROM temp.deleting)`)
	})
}

// uncountDeleted subtracts the pairs in temp.deleting from the sitemap
// counts: it sums them per leaf and bucket, carries each sum up to every
// ancestor, and subtracts the totals per node and bucket. Pairs the sitemap
// job has not linked yet are not counted, so there is nothing to subtract.
const uncountDeleted = `WITH RECURSIVE
	leaf(node, content_type, pairs, errors) AS (
		SELECT ps.node_id, coalesce(r.content_type, ''), count(*), sum(coalesce(r.status_code, 0) >= 500)
		FROM temp.deleting d
		JOIN request r ON r.id = d.id
		JOIN pair_sitemap ps ON ps.request_id = d.id
		GROUP BY ps.node_id, coalesce(r.content_type, '')),
	up(id, parent, content_type, pairs, errors) AS (
		SELECT n.id, coalesce(n.parent_id, 0), leaf.content_type, leaf.pairs, leaf.errors
		FROM leaf JOIN sitemap_node n ON n.id = leaf.node
		UNION ALL
		SELECT n.id, coalesce(n.parent_id, 0), up.content_type, up.pairs, up.errors
		FROM up JOIN sitemap_node n ON n.id = up.parent),
	total(id, parent, content_type, pairs, errors) AS (
		SELECT id, parent, content_type, sum(pairs), sum(errors) FROM up
		GROUP BY id, parent, content_type)
UPDATE sitemap_count SET pairs = sitemap_count.pairs - total.pairs, errors = sitemap_count.errors - total.errors
FROM total
WHERE sitemap_count.parent_id = total.parent AND sitemap_count.node_id = total.id
	AND sitemap_count.content_type = total.content_type`
