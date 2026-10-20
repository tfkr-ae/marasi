package db

import (
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// numberJoin joins a pair's number (docs/adr/2026-10-16-pairs-are-numbered-in-capture-order.md) to a read of request.
const numberJoin = ` LEFT JOIN pair_number ON pair_number.request_id = request.id`

// NextPairNumber takes the next pair number (docs/adr/2026-10-16-pairs-are-numbered-in-capture-order.md). The capture path
// takes it before the request event fires and stores it on the
// ProxyRequest, so the event already carries the number. A number taken for
// a pair whose insert fails is never handed out again, which leaves a gap.
func (repo *Repository) NextPairNumber() int64 {
	return repo.pairNumber.Add(1)
}

// highestPairNumber returns the highest pair number ever issued in the
// project: AUTOINCREMENT's sequence, which remembers deleted numbers, or the
// highest stored number when that is higher.
func highestPairNumber(q sqlx.Queryer) (int64, error) {
	var highest int64
	err := sqlx.Get(q, &highest, `SELECT max(
		coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'pair_number'), 0),
		coalesce((SELECT max(number) FROM pair_number), 0))`)
	return highest, err
}

// insertPairNumber stores a captured pair's number in the pair's insert
// transaction.
func insertPairNumber(tx sqlx.Execer, id uuid.UUID, number int64) error {
	if _, err := tx.Exec(`INSERT INTO pair_number (number, request_id) VALUES (?, ?)`, number, id); err != nil {
		return fmt.Errorf("storing pair number %d: %w", number, err)
	}
	return nil
}

// numberUnnumberedPairs numbers the pairs an older Marasi binary captured
// without numbers, in id order after the highest number (docs/adr/2026-10-16-pairs-are-numbered-in-capture-order.md). Such
// pairs are newer than the background jobs' high-water mark (docs/adr/2026-10-14-background-jobs-save-their-state.md), so
// only those are checked. With no mark, as when every pair came from an
// older binary, every pair is checked. Call it before the jobs are
// reconciled, which moves the mark past those pairs.
func numberUnnumberedPairs(tx *sqlx.Tx) error {
	var mark sql.NullString
	if err := tx.Get(&mark, `SELECT CASE WHEN count(*) = count(high_water) THEN min(high_water) END FROM background_job`); err != nil {
		return fmt.Errorf("reading the high-water mark: %w", err)
	}
	query := `INSERT INTO pair_number (request_id)
		SELECT id FROM request
		WHERE id NOT IN (SELECT request_id FROM pair_number)
		ORDER BY id`
	var args []any
	if mark.Valid {
		query = `INSERT INTO pair_number (request_id)
			SELECT id FROM request
			WHERE id > ? AND id NOT IN (SELECT request_id FROM pair_number WHERE request_id > ?)
			ORDER BY id`
		args = []any{mark.String, mark.String}
	}
	if _, err := tx.Exec(query, args...); err != nil {
		return fmt.Errorf("numbering pairs captured without a number: %w", err)
	}
	return nil
}
