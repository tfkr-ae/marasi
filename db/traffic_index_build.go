package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// indexPairAttempts is how many batches may fail on one pair before the build
// skips it.
const indexPairAttempts = 3

// databaseFailure reports whether err is a failure of the database itself,
// such as a lock, a full disk, or an I/O error, rather than of the pair being
// indexed. Such failures never count against a pair, so a failing database
// cannot make the build skip healthy pairs one after another.
func databaseFailure(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_NOMEM, sqlite3.SQLITE_READONLY,
		sqlite3.SQLITE_INTERRUPT, sqlite3.SQLITE_IOERR, sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_FULL,
		sqlite3.SQLITE_CANTOPEN, sqlite3.SQLITE_PROTOCOL, sqlite3.SQLITE_NOTADB:
		return true
	}
	return false
}

// SkippedPairError reports that the build gave up on a pair that failed to
// index indexPairAttempts times. Later batches leave the pair out, so it stays
// unsearchable by text until the project is opened again.
type SkippedPairError struct {
	ID  uuid.UUID
	Err error
}

func (e *SkippedPairError) Error() string {
	return fmt.Sprintf("skipped pair %s after %d failed attempts to index it: %v", e.ID, indexPairAttempts, e.Err)
}

func (e *SkippedPairError) Unwrap() error { return e.Err }

// indexBuildState remembers what this repository already knows about the
// background traffic index build, so the build does not rescan pairs it has
// seen. Whether the index is complete is saved in background_job (ADR-0039);
// this state is not: a fresh repository rediscovers the missing pairs, and
// retries the ones it skipped.
//
// mu guards only the fields, never a query, so reading the state does not
// wait for a build batch.
type indexBuildState struct {
	mu sync.Mutex
	// indexedFrom, when set, means every pair with an id at or above it is
	// indexed or skipped. Pairs written through this repository are indexed
	// as they are written, so the bound stays true.
	indexedFrom *uuid.UUID
	// failures counts the failed batches of each pair that failed to index.
	failures map[uuid.UUID]int
	// skipped holds the pairs the build gave up on.
	skipped []uuid.UUID
}

// snapshot returns the bound already known to be indexed, and the skipped
// pairs.
func (state *indexBuildState) snapshot() (*uuid.UUID, []uuid.UUID) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.indexedFrom, state.skipped
}

// reset forgets everything the build has learned, after the index was
// discarded.
func (state *indexBuildState) reset() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.indexedFrom = nil
	state.failures = nil
	state.skipped = nil
}

// failed records a failed batch for the pair with id, and reports whether the
// build now skips it.
func (state *indexBuildState) failed(id uuid.UUID) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failures == nil {
		state.failures = map[uuid.UUID]int{}
	}
	state.failures[id]++
	if state.failures[id] < indexPairAttempts {
		return false
	}
	delete(state.failures, id)
	// A new slice, so a snapshot taken earlier does not change.
	state.skipped = append(state.skipped[:len(state.skipped):len(state.skipped)], id)
	return true
}

// lowerBound records that every pair at or above id is indexed. The bound
// only moves down, as batches index older pairs.
func (state *indexBuildState) lowerBound(id uuid.UUID) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.indexedFrom == nil || bytes.Compare(id[:], state.indexedFrom[:]) < 0 {
		state.indexedFrom = &id
	}
}


// indexBuild describes one background job that fills a search index with the
// pairs missing from it.
type indexBuild struct {
	// job is the background job's name.
	job string
	// docsize is the index's FTS5 docsize table, which holds one row per
	// indexed key.
	docsize string
	// size is the SQL size, in bytes, of the stored text the job indexes for
	// the pair r.
	size string
	// index writes the pair's rows in the index.
	index func(tx sqlx.Ext, id uuid.UUID) error
	// state is what the repository remembers about the build.
	state func(repo *Repository) *indexBuildState
}

// trafficIndexBuild indexes pairs missing from the body index. It writes both
// indexes for each pair, as capture does.
var trafficIndexBuild = indexBuild{
	job:     TrafficIndexJob,
	docsize: "traffic_index_docsize",
	size: `coalesce(octet_length(r.request_raw), 0) + coalesce(octet_length(r.response_raw), 0) +
		coalesce(octet_length(r.metadata), 0) +
		coalesce((SELECT octet_length(n.note) FROM notes n WHERE n.request_id = r.id), 0)`,
	index: indexPair,
	state: func(repo *Repository) *indexBuildState { return &repo.index },
}

// annotationIndexBuild backfills the annotation index for pairs indexed
// before it existed (ADR-0038).
var annotationIndexBuild = indexBuild{
	job:     AnnotationIndexJob,
	docsize: "traffic_annotation_index_docsize",
	size: `coalesce(octet_length(r.metadata), 0) +
		coalesce((SELECT octet_length(n.note) FROM notes n WHERE n.request_id = r.id), 0)`,
	index: indexAnnotations,
	state: func(repo *Repository) *indexBuildState { return &repo.annotationIndex },
}

// IndexMissingTraffic runs one batch of the traffic index build: it indexes
// pairs missing from the traffic index, newest first, in one transaction, and
// reports whether any remain. A batch takes pairs while their stored text fits
// in maxBytes, and always takes at least one, so a pair larger than maxBytes is
// indexed in a batch of its own. The batch records its progress in the saved
// job state, and once nothing remains the job is complete and later batches
// do nothing.
//
// A pair that fails to index rolls back the batch. After it has failed
// indexPairAttempts batches, IndexMissingTraffic returns a *SkippedPairError
// and later batches leave it out. A failure of the database itself is
// returned without counting against the pair.
func (repo *Repository) IndexMissingTraffic(maxBytes int) (int, bool, error) {
	return repo.buildIndex(trafficIndexBuild, maxBytes)
}

// IndexMissingAnnotations runs one batch of the annotation index backfill, the
// way IndexMissingTraffic runs one of the traffic index build. A batch is
// capped by the size of the notes and metadata it indexes.
func (repo *Repository) IndexMissingAnnotations(maxBytes int) (int, bool, error) {
	return repo.buildIndex(annotationIndexBuild, maxBytes)
}

func (repo *Repository) buildIndex(build indexBuild, maxBytes int) (int, bool, error) {
	repo.jobMu.Lock()
	defer repo.jobMu.Unlock()
	state := build.state(repo)
	indexedFrom, skipped := state.snapshot()

	var ids []uuid.UUID
	var remaining bool
	var failed *uuid.UUID
	err := repo.inTx(func(tx *sqlx.Tx) error {
		job, err := loadJob(tx, build.job)
		if err != nil {
			return fmt.Errorf("reading the %s job: %w", build.job, err)
		}
		if job.Complete {
			return nil
		}
		ids, remaining, err = missingFromIndex(tx, build, indexedFrom, skipped, maxBytes)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := build.index(tx, id); err != nil {
				if !databaseFailure(err) {
					failed = &id
				}
				return err
			}
		}
		return advanceJob(tx, build.job, len(ids), remaining)
	})
	if err != nil {
		if failed != nil && state.failed(*failed) {
			return 0, false, &SkippedPairError{ID: *failed, Err: err}
		}
		return 0, false, fmt.Errorf("running the %s job: %w", build.job, err)
	}

	if remaining {
		state.lowerBound(ids[len(ids)-1])
	}
	return len(ids), remaining, nil
}

// indexComplete reports whether every pair is in both search indexes, from the
// saved job states. Traffic lists call it in the same transaction as their
// page query, so the flag describes the snapshot the page was read from.
func (repo *Repository) indexComplete(q sqlx.Queryer) (bool, error) {
	for _, name := range SearchIndexJobs {
		job, err := loadJob(q, name)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("checking the %s job: %w", name, err)
		}
		if !job.Complete {
			return false, nil
		}
	}
	return true, nil
}

// missingFromIndex returns ids of pairs without a row in the build's index,
// newest first, below indexedFrom when it is set and leaving out skipped,
// while the text the build indexes fits in maxBytes. It returns at least one
// id when any pair is missing, and reports whether more pairs are missing than
// it returned. A pair is missing when it has no key yet, or its key has no
// row in the index's docsize table.
func missingFromIndex(q sqlx.Queryer, build indexBuild, indexedFrom *uuid.UUID, skipped []uuid.UUID, maxBytes int) ([]uuid.UUID, bool, error) {
	query := `SELECT r.id, ` + build.size + ` AS size
		FROM request r
		LEFT JOIN traffic_index_key k ON k.request_id = r.id
		WHERE (k.id IS NULL OR NOT EXISTS (SELECT 1 FROM ` + build.docsize + ` d WHERE d.id = k.id))`
	args := []any{}
	if indexedFrom != nil {
		query += ` AND r.id < ?`
		args = append(args, *indexedFrom)
	}
	if len(skipped) > 0 {
		query += ` AND r.id NOT IN (?` + strings.Repeat(", ?", len(skipped)-1) + `)`
		for _, id := range skipped {
			args = append(args, id)
		}
	}
	query += ` ORDER BY r.id DESC`

	rows, err := q.Queryx(query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("finding traffic missing from %s: %w", build.job, err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	total := 0
	for rows.Next() {
		var pair struct {
			ID   uuid.UUID `db:"id"`
			Size int       `db:"size"`
		}
		if err := rows.StructScan(&pair); err != nil {
			return nil, false, fmt.Errorf("finding traffic missing from %s: %w", build.job, err)
		}
		if len(ids) > 0 && total+pair.Size > maxBytes {
			return ids, true, nil
		}
		ids = append(ids, pair.ID)
		total += pair.Size
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("finding traffic missing from %s: %w", build.job, err)
	}
	return ids, false, nil
}
