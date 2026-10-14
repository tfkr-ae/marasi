package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// selectionSubquery returns a subquery selecting the id of every pair in sel
// (ADR-0033), with its arguments. Ids are bound as one JSON array read with
// json_each, so a selection of any size uses a single parameter; ids with no
// pair are dropped by the join. A query selection is the translated query,
// plus the exclusion, plus id <= as_of. An invalid query selection returns a
// *domain.QueryError; a selection with neither or both forms wraps
// domain.ErrInvalidSelection.
func selectionSubquery(sel domain.Selection) (string, []any, error) {
	switch {
	case sel.Query != nil && sel.IDs == nil:
		filter, err := newTrafficFilter(sel.Query.Text, sel.Query.Exclusion)
		if err != nil {
			return "", nil, err
		}
		filter.add(`request.id <= ?`, sel.Query.AsOf)
		return `SELECT request.id FROM request` + filter.where(), filter.args, nil
	case sel.Query == nil && len(sel.IDs) > 0:
		ids, err := json.Marshal(sel.IDs)
		if err != nil {
			return "", nil, fmt.Errorf("encoding selection ids: %w", err)
		}
		return `SELECT request.id FROM json_each(?) AS selected JOIN request ON request.id = selected.value`, []any{string(ids)}, nil
	default:
		return "", nil, fmt.Errorf("%w: expected either ids or a query", domain.ErrInvalidSelection)
	}
}

// bulkInTx resolves sel inside one transaction and runs apply with the
// selection subquery and its arguments. apply returns the number of pairs it
// changed, which bulkInTx returns once the transaction commits.
func (repo *Repository) bulkInTx(sel domain.Selection, apply func(tx *sqlx.Tx, selected string, args []any) (int, error)) (int, error) {
	selected, args, err := selectionSubquery(sel)
	if err != nil {
		return 0, err
	}
	var count int
	err = repo.inTx(func(tx *sqlx.Tx) error {
		count, err = apply(tx, selected, args)
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// linkTable describes the table that links pairs to one kind of target.
type linkTable struct {
	table       string // link table name
	column      string // column holding the target id
	targetTable string // table the target id must exist in
}

var linkTables = map[domain.LinkKind]linkTable{
	domain.LinkLaunchpad: {table: "launchpad_request", column: "launchpad_id", targetTable: "launchpad"},
	domain.LinkFinding:   {table: "finding_requests", column: "finding_id", targetTable: "findings"},
	domain.LinkTestCase:  {table: "test_case_requests", column: "test_case_id", targetTable: "test_cases"},
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
// pairs it newly linked.
func (repo *Repository) LinkTraffic(sel domain.Selection, target domain.LinkTarget) (int, error) {
	return repo.bulkInTx(sel, func(tx *sqlx.Tx, selected string, args []any) (int, error) {
		link, err := linkTableFor(tx, target)
		if err != nil {
			return 0, err
		}
		query := `INSERT OR IGNORE INTO ` + link.table + ` (request_id, ` + link.column + `) SELECT id, ? FROM (` + selected + `)`
		return execCount(tx, fmt.Sprintf("linking traffic to %s %s", target.Kind, target.ID), query, append([]any{target.ID}, args...)...)
	})
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
