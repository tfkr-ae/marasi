package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// selectionSubquery returns a subquery selecting the id of every pair in sel
// (ADR-0033), with its arguments. Ids are bound as one JSON array read with
// json_each, so a selection of any size uses a single parameter; ids with no
// pair are dropped by the join and a repeated id is selected once. A query selection is the translated query,
// plus the exclusion, plus id <= as_of, and its jobs are the background jobs
// whose progress changes which pairs the query matches. An invalid query
// selection returns a *domain.QueryError; a selection with neither or both
// forms wraps domain.ErrInvalidSelection.
func selectionSubquery(sel domain.Selection) (string, []any, []domain.JobName, error) {
	switch {
	case sel.Query != nil && sel.IDs == nil:
		match, err := newTrafficMatch(sel.Query.Text, sel.Query.Exclusion)
		if err != nil {
			return "", nil, nil, err
		}
		match.add(`request.id <= ?`, sel.Query.AsOf)
		return `SELECT request.id FROM request` + match.where(), match.args, match.jobs(), nil
	case sel.Query == nil && len(sel.IDs) > 0:
		ids, err := json.Marshal(sel.IDs)
		if err != nil {
			return "", nil, nil, fmt.Errorf("encoding selection ids: %w", err)
		}
		return `SELECT DISTINCT request.id FROM json_each(?) AS selected JOIN request ON request.id = selected.value`, []any{string(ids)}, nil, nil
	default:
		return "", nil, nil, fmt.Errorf("%w: expected either ids or a query", domain.ErrInvalidSelection)
	}
}

// selectionReady returns a *domain.SelectionIncompleteError naming the jobs
// in jobs that are not complete, or nil when all are. Bulk operations check
// it in their transaction, so a job cannot finish or restart between the
// check and the change.
func selectionReady(q sqlx.Queryer, jobs []domain.JobName) error {
	complete, err := jobCompleteness(q, jobs...)
	if err != nil {
		return err
	}
	var unfinished []domain.JobName
	for _, job := range jobs {
		if !complete[job] {
			unfinished = append(unfinished, job)
		}
	}
	if len(unfinished) > 0 {
		return &domain.SelectionIncompleteError{Jobs: unfinished}
	}
	return nil
}

// bulkInTx resolves sel inside one transaction and runs apply with the
// selection subquery and its arguments. apply returns the number of pairs it
// changed, which bulkInTx returns once the transaction commits. A query
// selection whose query depends on an unfinished background job returns a
// *domain.SelectionIncompleteError and changes nothing: while a search index
// or the sitemap is building, a query such as NOT note:"keep" would select
// pairs the finished job would leave out.
func (repo *Repository) bulkInTx(sel domain.Selection, apply func(tx *sqlx.Tx, selected string, args []any) (int, error)) (int, error) {
	selected, args, jobs, err := selectionSubquery(sel)
	if err != nil {
		return 0, err
	}
	// A query selection may have a text term, which must see every pair
	// captured before it.
	if sel.Query != nil {
		if err := repo.flushIndexQueue(); err != nil {
			return 0, err
		}
	}
	if repo.afterBulkFlush != nil {
		repo.afterBulkFlush()
	}
	var count int
	err = repo.inTx(func(tx *sqlx.Tx) error {
		if err := selectionReady(tx, jobs); err != nil {
			return err
		}
		if slices.Contains(jobs, domain.JobTrafficIndex) {
			if err := indexQueuedUpTo(tx, sel.Query.AsOf); err != nil {
				return err
			}
		}
		changed, err := apply(tx, selected, args)
		count = changed
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// indexQueuedUpTo indexes, in tx, the queued pairs with an id at or below
// asOf. The flush before a bulk transaction leaves a window: a response
// written between the flush and the transaction is queued, not indexed, and
// a text query selection would read that pair's old index row. A pair that
// fails to index for an error in its own data refuses the selection as
// incomplete; the queue drops such a pair, and a retry then succeeds.
func indexQueuedUpTo(tx *sqlx.Tx, asOf uuid.UUID) error {
	var queued []uuid.UUID
	err := tx.Select(&queued, `SELECT request_id FROM traffic_index_pending WHERE request_id <= ? ORDER BY request_id`, asOf)
	if err != nil {
		return fmt.Errorf("reading the index queue: %w", err)
	}
	for _, id := range queued {
		if err := indexPair(tx, id); err != nil {
			if databaseFailure(err) {
				return err
			}
			return &domain.SelectionIncompleteError{Jobs: []domain.JobName{domain.JobTrafficIndex}}
		}
		if _, err := tx.Exec(`DELETE FROM traffic_index_pending WHERE request_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// linkTable describes the table that links pairs to one kind of target.
type linkTable struct {
	table       string // link table name
	column      string // column holding the target id
	targetTable string // table the target id must exist in
	noun        string // the target in query messages, as in "a finding id"
}

var linkTables = map[domain.LinkKind]linkTable{
	domain.LinkLaunchpad: {table: "launchpad_request", column: "launchpad_id", targetTable: "launchpad", noun: "launchpad"},
	domain.LinkFinding:   {table: "finding_requests", column: "finding_id", targetTable: "findings", noun: "finding"},
	domain.LinkTestCase:  {table: "test_case_requests", column: "test_case_id", targetTable: "test_cases", noun: "test case"},
}

// linkTableFor returns the link table for target after checking, in tx, that
// the target exists. A missing target wraps domain.ErrLinkTargetNotFound.
func linkTableFor(tx *sqlx.Tx, target domain.LinkTarget) (linkTable, error) {
	link, ok := linkTables[target.Kind]
	if !ok {
		return linkTable{}, fmt.Errorf("unknown link kind %d", target.Kind)
	}
	var exists int
	err := tx.Get(&exists, `SELECT 1 FROM `+link.targetTable+` WHERE id = ?`, target.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return linkTable{}, fmt.Errorf("%w: no %s with id %s", domain.ErrLinkTargetNotFound, target.Kind, target.ID)
	}
	if err != nil {
		return linkTable{}, fmt.Errorf("checking %s %s: %w", target.Kind, target.ID, err)
	}
	return link, nil
}

// LinkTraffic links every pair in sel to target and returns the number of
// pairs it newly linked. A launchpad gains the pairs as its last entries, in
// capture order; a pair that is already an entry keeps its place.
func (repo *Repository) LinkTraffic(sel domain.Selection, target domain.LinkTarget) (int, error) {
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		link, err := linkTableFor(tx, target)
		if err != nil {
			return 0, err
		}
		if target.Kind == domain.LinkLaunchpad {
			return appendEntries(tx, target.ID, selected, args)
		}
		query := `INSERT OR IGNORE INTO ` + link.table + ` (request_id, ` + link.column + `) SELECT id, ? FROM (` + selected + `)`
		return execCount(tx, fmt.Sprintf("linking traffic to %s %s", target.Kind, target.ID), query, append([]any{target.ID}, args...)...)
	})
}

// appendEntries links, in tx, the pairs the selected subquery returns that are
// not yet entries of the launchpad, after its last entry in capture order, and
// returns how many it linked.
func appendEntries(tx *sqlx.Tx, launchpadID uuid.UUID, selected string, args []any) (int, error) {
	var ids []uuid.UUID
	query := `SELECT id FROM (` + selected + `) WHERE id NOT IN (SELECT request_id FROM launchpad_request WHERE launchpad_id = ?) ORDER BY id`
	if err := tx.Select(&ids, query, append(slices.Clone(args), launchpadID)...); err != nil {
		return 0, fmt.Errorf("selecting traffic to link to launchpad %s: %w", launchpadID, err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	keys, err := keysAfterLastEntry(tx, launchpadID, len(ids))
	if err != nil {
		return 0, err
	}
	insert, err := tx.Prepare(`INSERT INTO launchpad_request (request_id, launchpad_id, position) VALUES (?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("linking traffic to launchpad %s: %w", launchpadID, err)
	}
	defer insert.Close()
	for i, id := range ids {
		if _, err := insert.Exec(id, launchpadID, keys[i]); err != nil {
			return 0, fmt.Errorf("linking traffic to launchpad %s: %w", launchpadID, err)
		}
	}
	return len(ids), nil
}

// UnlinkTraffic removes the links between every pair in sel and target and
// returns the number of pairs that were linked.
func (repo *Repository) UnlinkTraffic(sel domain.Selection, target domain.LinkTarget) (int, error) {
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		link, err := linkTableFor(tx, target)
		if err != nil {
			return 0, err
		}
		query := `DELETE FROM ` + link.table + ` WHERE ` + link.column + ` = ? AND request_id IN (` + selected + `)`
		return execCount(tx, fmt.Sprintf("unlinking traffic from %s %s", target.Kind, target.ID), query, append([]any{target.ID}, args...)...)
	})
}

// execCount runs query in tx and returns the number of rows it changed.
func execCount(tx *sqlx.Tx, what, query string, args ...any) (int, error) {
	result, err := tx.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("%s: counting rows: %w", what, err)
	}
	return int(affected), nil
}
