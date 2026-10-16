package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.StatsRepository = (*Repository)(nil)

// The pair and intercepted counts are kept in dashboard_count (migration
// 00023, ADR-0043), so reading them does not scan request.

// pairCount reads the pair count: the pairs counted up to pairs_through and
// the numbers above it, which foldPairCount has not yet counted.
const pairCount = `(SELECT value FROM dashboard_count WHERE name = 'pairs') + (SELECT count(*) FROM pair_number
	WHERE number > (SELECT value FROM dashboard_count WHERE name = 'pairs_through'))`

// foldPairCount counts the pair numbers above pairs_through into pairs, so
// that reading the count does not scan them again. Capture adds nothing to
// the count, which keeps it off the capture path.
func foldPairCount(tx sqlx.Execer) error {
	_, err := tx.Exec(`UPDATE dashboard_count SET value = ` + pairCount + ` WHERE name = 'pairs'`)
	if err == nil {
		_, err = tx.Exec(`UPDATE dashboard_count SET value = max(value, coalesce((SELECT max(number) FROM pair_number), 0))
			WHERE name = 'pairs_through'`)
	}
	if err != nil {
		return fmt.Errorf("folding the pair count: %w", err)
	}
	return nil
}

// isIntercepted is the condition the dashboard counts as intercepted.
// Metadata that is not valid JSON is not intercepted, so that it cannot fail
// a write.
const isIntercepted = `coalesce(CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$.intercepted') = true END, 0)`

// interceptedKey is the top-level metadata key the intercepted count follows.
const interceptedKey = "intercepted"

// interceptedBatchPairs is how many pairs one batch of the intercepted_count
// job checks. Reading a pair's metadata reads past both raw bodies, about
// 35 us a pair on a cold 1 GB project, so a batch holds the connection for
// under 40 ms.
var interceptedBatchPairs = 500

var interceptedCountJob = jobDefinition{
	name:    domain.JobInterceptedCount,
	version: 1,
	batch: func(repo *Repository, _ int) (int, bool, error) {
		return repo.CountMissingIntercepted(interceptedBatchPairs)
	},
	discard: func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`DELETE FROM pair_intercepted`); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE intercepted_count_scan SET above = NULL, below = NULL`)
		return err
	},
	// Pairs an older binary captured are above the job's high-water mark. A
	// complete job checks only those. An incomplete one starts again from
	// the newest pair, down to where it had still to check.
	reopen: func(tx *sqlx.Tx, row jobRow) error {
		if row.Complete {
			_, err := tx.Exec(`UPDATE intercepted_count_scan SET above = ?, below = NULL`, row.HighWater)
			return err
		}
		_, err := tx.Exec(`UPDATE intercepted_count_scan SET below = NULL`)
		return err
	},
	rebuildable: true,
}

// syncIntercepted brings each pair's place in pair_intercepted up to date
// with its stored metadata. Call it in the transaction that writes the
// metadata.
func syncIntercepted(tx sqlx.Execer, ids ...uuid.UUID) error {
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO pair_intercepted (request_id)
			SELECT id FROM request WHERE id = ? AND `+isIntercepted, id); err != nil {
			return fmt.Errorf("counting intercepted pair %s: %w", id, err)
		}
		if _, err := tx.Exec(`DELETE FROM pair_intercepted WHERE request_id = ?
			AND NOT EXISTS (SELECT 1 FROM request WHERE id = ? AND `+isIntercepted+`)`, id, id); err != nil {
			return fmt.Errorf("uncounting intercepted pair %s: %w", id, err)
		}
	}
	return nil
}

// CountRows returns the total number of request-response rows stored in the repository.
func (repo *Repository) CountRows() (int, error) {
	var count int
	err := repo.dbConn.Get(&count, `SELECT `+pairCount)
	if err != nil {
		return 0, fmt.Errorf("getting request count: %w", err)
	}

	return count, nil
}

// CountNotes returns the total number of notes associated with requests.
func (repo *Repository) CountNotes() (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM notes WHERE note IS NOT NULL AND note != ''`

	err := repo.dbConn.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("getting notes count: %w", err)
	}

	return count, nil
}

// CountLaunchpads returns the total number of created launchpads.
func (repo *Repository) CountLaunchpads() (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM launchpad`

	err := repo.dbConn.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("getting launchpad count: %w", err)
	}

	return count, nil
}

// CountIntercepted returns the total number of intercepted requests. It is
// exact: until the intercepted_count job is complete it scans request, which
// is slow on a large project. DashboardCounts instead reports the job.
func (repo *Repository) CountIntercepted() (int, error) {
	var count int
	err := repo.dbConn.Get(&count, `SELECT c.value FROM dashboard_count c, background_job j
		WHERE c.name = 'intercepted' AND j.name = ? AND j.complete`, domain.JobInterceptedCount)
	if errors.Is(err, sql.ErrNoRows) {
		err = repo.dbConn.Get(&count, `SELECT COUNT(*) FROM request WHERE `+isIntercepted)
	}
	if err != nil {
		return 0, fmt.Errorf("getting intercepted count: %w", err)
	}

	return count, nil
}

// DashboardCounts returns every dashboard count in one read.
func (repo *Repository) DashboardCounts() (domain.DashboardCounts, error) {
	var row struct {
		Pairs       int  `db:"pairs"`
		Notes       int  `db:"notes"`
		Launchpads  int  `db:"launchpads"`
		Intercepted int  `db:"intercepted"`
		Complete    bool `db:"complete"`
	}
	err := repo.dbConn.Get(&row, `SELECT
		`+pairCount+` AS pairs,
		(SELECT COUNT(*) FROM notes WHERE note IS NOT NULL AND note != '') AS notes,
		(SELECT COUNT(*) FROM launchpad) AS launchpads,
		(SELECT value FROM dashboard_count WHERE name = 'intercepted') AS intercepted,
		coalesce((SELECT complete FROM background_job WHERE name = ?), 0) AS complete`, domain.JobInterceptedCount)
	if err != nil {
		return domain.DashboardCounts{}, fmt.Errorf("getting dashboard counts: %w", err)
	}
	return domain.DashboardCounts{
		Pairs:       row.Pairs,
		Notes:       row.Notes,
		Launchpads:  row.Launchpads,
		Intercepted: row.Intercepted,
		Complete:    map[domain.JobName]bool{domain.JobInterceptedCount: row.Complete},
	}, nil
}

// CountMissingIntercepted runs one batch of the intercepted_count job: it
// checks up to maxPairs of the pairs it has still to check, newest first,
// and adds the intercepted ones to the count. The repository keeps the pairs
// it writes itself, and a pair checked twice is counted once. It returns how
// many pairs the batch checked and whether work remains.
func (repo *Repository) CountMissingIntercepted(maxPairs int) (int, bool, error) {
	repo.jobMu.Lock()
	defer repo.jobMu.Unlock()

	var checked int
	var remaining bool
	err := repo.inTx(func(tx *sqlx.Tx) error {
		job, err := loadJob(tx, domain.JobInterceptedCount)
		if err != nil {
			return fmt.Errorf("reading the intercepted_count job: %w", err)
		}
		if job.Complete {
			return nil
		}
		var scan struct {
			Above sql.NullString `db:"above"`
			Below sql.NullString `db:"below"`
		}
		if err := tx.Get(&scan, `SELECT above, below FROM intercepted_count_scan WHERE id = 1`); err != nil {
			return fmt.Errorf("reading the job's position: %w", err)
		}
		query := `SELECT id, ` + isIntercepted + ` AS hit FROM request WHERE 1`
		args := []any{}
		if scan.Above.Valid {
			query += ` AND id > ?`
			args = append(args, scan.Above.String)
		}
		if scan.Below.Valid {
			query += ` AND id < ?`
			args = append(args, scan.Below.String)
		}
		var pairs []struct {
			ID  uuid.UUID `db:"id"`
			Hit bool      `db:"hit"`
		}
		if err := tx.Select(&pairs, query+` ORDER BY id DESC LIMIT ?`, append(args, maxPairs+1)...); err != nil {
			return fmt.Errorf("checking pairs: %w", err)
		}
		remaining = len(pairs) > maxPairs
		if remaining {
			pairs = pairs[:maxPairs]
		}
		for _, pair := range pairs {
			if !pair.Hit {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO pair_intercepted (request_id) VALUES (?)`, pair.ID); err != nil {
				return fmt.Errorf("counting intercepted pair %s: %w", pair.ID, err)
			}
		}
		// Once done, nothing is left to check until an open reopens the job:
		// no id is below ''.
		above, below := scan.Above, sql.NullString{Valid: true}
		if remaining {
			below.String = pairs[len(pairs)-1].ID.String()
		} else {
			above = sql.NullString{}
		}
		if _, err := tx.Exec(`UPDATE intercepted_count_scan SET above = ?, below = ? WHERE id = 1`, above, below); err != nil {
			return fmt.Errorf("saving the job's position: %w", err)
		}
		checked = len(pairs)
		return advanceJob(tx, domain.JobInterceptedCount, checked, remaining)
	})
	if err != nil {
		return 0, false, fmt.Errorf("counting intercepted pairs: %w", err)
	}
	return checked, remaining, nil
}
