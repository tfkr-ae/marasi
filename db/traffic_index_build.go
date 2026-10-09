package db

import (
	"bytes"
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
// traffic index, so the background build does not rescan pairs it has seen.
// It is not stored: a fresh repository rediscovers the missing pairs, and
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
	// complete means no pair is missing from the index except skipped ones.
	complete bool
	// failures counts the failed batches of each pair that failed to index.
	failures map[uuid.UUID]int
	// skipped holds the pairs the build gave up on.
	skipped []uuid.UUID
}

// snapshot returns whether the index is known to be complete, the bound
// already known to be indexed, and the skipped pairs.
func (state *indexBuildState) snapshot() (bool, *uuid.UUID, []uuid.UUID) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.complete, state.indexedFrom, state.skipped
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

func (state *indexBuildState) markComplete() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.complete = true
}

// IndexMissingTraffic indexes pairs missing from the traffic index, newest
// first, in one transaction, and reports whether any remain. A batch takes
// pairs while their stored text fits in maxBytes, and always takes at least
// one, so a pair larger than maxBytes is indexed in a batch of its own.
//
// A pair that fails to index rolls back the batch. After it has failed
// indexPairAttempts batches, IndexMissingTraffic returns a *SkippedPairError
// and later batches leave it out. A failure of the database itself is
// returned without counting against the pair.
func (repo *Repository) IndexMissingTraffic(maxBytes int) (int, bool, error) {
	complete, indexedFrom, skipped := repo.index.snapshot()
	if complete {
		return 0, false, nil
	}

	var ids []uuid.UUID
	var remaining bool
	var failed *uuid.UUID
	err := repo.inTx(func(tx *sqlx.Tx) error {
		var err error
		ids, remaining, err = missingFromIndex(tx, indexedFrom, skipped, maxBytes)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := indexPair(tx, id); err != nil {
				if !databaseFailure(err) {
					failed = &id
				}
				return err
			}
		}
		return nil
	})
	if err != nil {
		if failed != nil && repo.index.failed(*failed) {
			return 0, false, &SkippedPairError{ID: *failed, Err: err}
		}
		return 0, false, fmt.Errorf("indexing missing traffic: %w", err)
	}

	if remaining {
		repo.index.lowerBound(ids[len(ids)-1])
	} else {
		repo.index.markComplete()
	}
	return len(ids), remaining, nil
}

// indexComplete reports whether every pair is in the traffic index. Traffic
// lists call it in the same transaction as their page query, so the flag
// describes the snapshot the page was read from.
func (repo *Repository) indexComplete(q sqlx.Queryer) (bool, error) {
	complete, indexedFrom, skipped := repo.index.snapshot()
	if complete {
		return true, nil
	}
	ids, _, err := missingFromIndex(q, indexedFrom, skipped, 0)
	if err != nil {
		return false, fmt.Errorf("checking the traffic index: %w", err)
	}
	if len(ids) > 0 {
		return false, nil
	}
	repo.index.markComplete()
	return true, nil
}

// missingFromIndex returns ids of pairs without an index row, newest first,
// below indexedFrom when it is set and leaving out skipped, while their stored
// text fits in maxBytes. It returns at least one id when any pair is missing,
// and reports whether more pairs are missing than it returned. A pair is
// missing when it has no key yet, or its key has no index row; the FTS5
// docsize table holds one row per indexed key.
func missingFromIndex(q sqlx.Queryer, indexedFrom *uuid.UUID, skipped []uuid.UUID, maxBytes int) ([]uuid.UUID, bool, error) {
	query := `SELECT r.id,
		coalesce(octet_length(r.request_raw), 0) + coalesce(octet_length(r.response_raw), 0) +
		coalesce(octet_length(r.metadata), 0) +
		coalesce((SELECT octet_length(n.note) FROM notes n WHERE n.request_id = r.id), 0) AS size
		FROM request r
		LEFT JOIN traffic_index_key k ON k.request_id = r.id
		WHERE (k.id IS NULL OR NOT EXISTS (SELECT 1 FROM traffic_index_docsize d WHERE d.id = k.id))`
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
		return nil, false, fmt.Errorf("finding traffic missing from the index: %w", err)
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
			return nil, false, fmt.Errorf("finding traffic missing from the index: %w", err)
		}
		if len(ids) > 0 && total+pair.Size > maxBytes {
			return ids, true, nil
		}
		ids = append(ids, pair.ID)
		total += pair.Size
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("finding traffic missing from the index: %w", err)
	}
	return ids, false, nil
}
