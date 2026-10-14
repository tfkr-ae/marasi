package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// TrafficIndexJob is the background job that indexes pairs missing from the
// traffic index (ADR-0026).
const TrafficIndexJob = domain.JobTrafficIndex

// AnnotationIndexJob is the background job that backfills the annotation
// index with the notes and metadata of pairs indexed before it (ADR-0038).
const AnnotationIndexJob = domain.JobAnnotationIndex

// SearchIndexJobs are the jobs that text search depends on. A traffic read's
// index.complete is true when all of them are complete.
var SearchIndexJobs = []string{TrafficIndexJob, AnnotationIndexJob}

var (
	// ErrUnknownJob means no background job has the requested name.
	ErrUnknownJob = errors.New("unknown background job")
	// ErrJobNotRebuildable means the background job cannot be rebuilt.
	ErrJobNotRebuildable = errors.New("background job cannot be rebuilt")
)

// JobState is the saved state of one background job. Total is 0 until the
// job has counted its work.
type JobState struct {
	Name     string
	Complete bool
	Done     int64
	Total    int64
}

// jobDefinition registers a background job with this binary. Every job works
// through the project's pairs, so a project without pairs has nothing to do.
type jobDefinition struct {
	name string
	// version is the job's rule or decoder version. A project whose saved
	// version differs resets the job when it opens.
	version int
	// batch runs one batch of the job in one transaction, which records the
	// batch with advanceJob. It returns how many pairs the batch processed
	// and whether work remains.
	batch func(repo *Repository, maxBytes int) (int, bool, error)
	// discard, when set, removes the job's output so that a reset job redoes
	// all of it. It runs in the reset transaction on a version change or a
	// rebuild.
	discard func(tx *sqlx.Tx) error
	// forget, when set, drops what the repository remembers about the job's
	// progress after a reset.
	forget func(repo *Repository)
	// rebuildable means a client may ask for the job to be rebuilt.
	rebuildable bool
}

// jobRegistry lists the background jobs this binary runs, in the order they
// run.
var jobRegistry = []jobDefinition{
	{
		name:    TrafficIndexJob,
		version: bodyDecoderVersion,
		batch: func(repo *Repository, maxBytes int) (int, bool, error) {
			return repo.IndexMissingTraffic(maxBytes)
		},
		discard: func(tx *sqlx.Tx) error {
			_, err := tx.Exec(`INSERT INTO traffic_index (traffic_index) VALUES ('delete-all')`)
			return err
		},
		forget:      func(repo *Repository) { repo.index.reset() },
		rebuildable: true,
	},
	{
		name:    AnnotationIndexJob,
		version: 1,
		batch: func(repo *Repository, maxBytes int) (int, bool, error) {
			return repo.IndexMissingAnnotations(maxBytes)
		},
		discard: func(tx *sqlx.Tx) error {
			_, err := tx.Exec(`INSERT INTO traffic_annotation_index (traffic_annotation_index) VALUES ('delete-all')`)
			return err
		},
		forget:      func(repo *Repository) { repo.annotationIndex.reset() },
		rebuildable: true,
	},
}

func findJob(name string) (jobDefinition, bool) {
	for _, job := range jobRegistry {
		if job.name == name {
			return job, true
		}
	}
	return jobDefinition{}, false
}

// jobRow is a background_job row.
type jobRow struct {
	Name      string         `db:"name"`
	Version   int            `db:"version"`
	Complete  bool           `db:"complete"`
	Done      int64          `db:"done"`
	Total     sql.NullInt64  `db:"total"`
	HighWater sql.NullString `db:"high_water"`
}

func loadJob(q sqlx.Queryer, name string) (jobRow, error) {
	var row jobRow
	err := sqlx.Get(q, &row, `SELECT name, version, complete, done, total, high_water FROM background_job WHERE name = ?`, name)
	return row, err
}

// newestPair returns the id of the newest pair, which is invalid when the
// project has no pairs.
func newestPair(q sqlx.Queryer) (sql.NullString, error) {
	var newest sql.NullString
	err := sqlx.Get(q, &newest, `SELECT max(id) FROM request`)
	return newest, err
}

// reconcileJobs brings the saved state of every registered job up to date
// when a project opens (ADR-0039). A job this binary has not run on the
// project before starts, complete when there are no pairs. A job whose saved
// version differs resets and discards its output. A job whose high-water mark
// is behind the newest pair was skipped by a binary that does not maintain it,
// so it resets to process the pairs above the mark.
func reconcileJobs(conn *sqlx.DB) error {
	tx, err := conn.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	newest, err := newestPair(tx)
	if err != nil {
		return fmt.Errorf("finding the newest pair: %w", err)
	}
	for _, job := range jobRegistry {
		row, err := loadJob(tx, job.name)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`INSERT INTO background_job (name, version, complete, done, total, high_water)
				VALUES (?, ?, ?, 0, CASE WHEN ? THEN 0 END, ?)`,
				job.name, job.version, !newest.Valid, !newest.Valid, newest)
		case err != nil:
		case row.Version != job.version:
			err = restartJob(tx, job, newest)
		case newest.Valid && (!row.HighWater.Valid || newest.String > row.HighWater.String):
			err = reopenJob(tx, row, newest)
		}
		if err != nil {
			return fmt.Errorf("reconciling background job %s: %w", job.name, err)
		}
	}
	return tx.Commit()
}

// restartJob resets job so that it redoes all of its work from scratch.
func restartJob(tx *sqlx.Tx, job jobDefinition, newest sql.NullString) error {
	if job.discard != nil {
		if err := job.discard(tx); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO background_job (name, version, complete, done, total, high_water, updated_at)
		VALUES (?, ?, ?, 0, CASE WHEN ? THEN 0 END, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (name) DO UPDATE SET version = excluded.version, complete = excluded.complete,
			done = 0, total = excluded.total, high_water = excluded.high_water, updated_at = excluded.updated_at`,
		job.name, job.version, !newest.Valid, !newest.Valid, newest)
	return err
}

// reopenJob resets a job to process the pairs newer than its high-water mark.
// A complete job starts over with those pairs as its work. An incomplete job
// keeps its progress and adds them to its work.
func reopenJob(tx *sqlx.Tx, row jobRow, newest sql.NullString) error {
	var added int64
	if err := tx.Get(&added, `SELECT count(*) FROM request WHERE ? IS NULL OR id > ?`, row.HighWater, row.HighWater); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE background_job SET
		done = CASE WHEN complete THEN 0 ELSE done END,
		total = CASE WHEN complete THEN ? ELSE total + ? END,
		complete = 0, high_water = ?, updated_at = CURRENT_TIMESTAMP
		WHERE name = ?`, added, added, newest, row.Name)
	return err
}

// advanceJob records a batch of the job that processed pairs, in the batch's
// transaction. A job counts its work on its first batch. When no work
// remains, the job is complete and done equals total.
func advanceJob(tx *sqlx.Tx, name string, processed int, remaining bool) error {
	_, err := tx.Exec(`UPDATE background_job SET
		total = coalesce(total, (SELECT count(*) FROM request)),
		done = done + ?,
		updated_at = CURRENT_TIMESTAMP
		WHERE name = ?`, processed, name)
	if err != nil || remaining {
		return err
	}
	_, err = tx.Exec(`UPDATE background_job SET
		complete = 1, total = max(total, done), done = max(total, done)
		WHERE name = ?`, name)
	return err
}

// advanceHighWater records that the pair with id was written by this binary,
// which maintains every registered job as it writes. Call it in the
// transaction that inserts the pair.
func advanceHighWater(tx sqlx.Execer, id uuid.UUID) error {
	args := []any{id, id}
	for _, job := range jobRegistry {
		args = append(args, job.name)
	}
	_, err := tx.Exec(`UPDATE background_job SET high_water = ?
		WHERE (high_water IS NULL OR high_water < ?)
		AND name IN (?`+strings.Repeat(", ?", len(jobRegistry)-1)+`)`, args...)
	if err != nil {
		return fmt.Errorf("advancing the background job high-water mark: %w", err)
	}
	return nil
}

// ListJobs returns the saved state of each background job this binary runs.
func (repo *Repository) ListJobs() ([]JobState, error) {
	var rows []jobRow
	if err := repo.dbConn.Select(&rows, `SELECT name, version, complete, done, total, high_water FROM background_job`); err != nil {
		return nil, fmt.Errorf("listing background jobs: %w", err)
	}
	saved := make(map[string]jobRow, len(rows))
	for _, row := range rows {
		saved[row.Name] = row
	}
	jobs := make([]JobState, 0, len(jobRegistry))
	for _, job := range jobRegistry {
		row, ok := saved[job.name]
		if !ok {
			continue
		}
		jobs = append(jobs, JobState{Name: row.Name, Complete: row.Complete, Done: row.Done, Total: row.Total.Int64})
	}
	return jobs, nil
}

// RunJobBatch runs one batch of the named background job, newest pairs first,
// with stored text capped at maxBytes. It returns how many pairs the batch
// processed and whether work remains. A complete job does nothing.
func (repo *Repository) RunJobBatch(name string, maxBytes int) (int, bool, error) {
	job, ok := findJob(name)
	if !ok {
		return 0, false, fmt.Errorf("%w: %s", ErrUnknownJob, name)
	}
	return job.batch(repo, maxBytes)
}

// RebuildJob resets the named background job so that it redoes all of its
// work. It waits for the job's batch in flight. The job's output is incomplete
// until the job runs again.
func (repo *Repository) RebuildJob(name string) error {
	job, ok := findJob(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownJob, name)
	}
	if !job.rebuildable {
		return fmt.Errorf("%w: %s", ErrJobNotRebuildable, name)
	}
	repo.jobMu.Lock()
	defer repo.jobMu.Unlock()
	err := repo.inTx(func(tx *sqlx.Tx) error {
		newest, err := newestPair(tx)
		if err != nil {
			return err
		}
		return restartJob(tx, job, newest)
	})
	if err != nil {
		return fmt.Errorf("rebuilding background job %s: %w", name, err)
	}
	if job.forget != nil {
		job.forget(repo)
	}
	return nil
}
