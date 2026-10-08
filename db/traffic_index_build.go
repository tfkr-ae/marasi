package db

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// indexBuildState remembers what this repository already knows about the
// traffic index, so the background build does not rescan pairs it has seen.
// It is not stored: a fresh repository rediscovers the missing pairs.
//
// mu guards only the fields, never a query, so reading the state does not
// wait for a build batch.
type indexBuildState struct {
	mu sync.Mutex
	// indexedFrom, when set, means every pair with an id at or above it is
	// indexed. Pairs written through this repository are indexed as they are
	// written, so the bound stays true.
	indexedFrom *uuid.UUID
	complete    bool
}

// snapshot returns whether the index is known to be complete and the bound
// already known to be indexed.
func (state *indexBuildState) snapshot() (bool, *uuid.UUID) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.complete, state.indexedFrom
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

// IndexMissingTraffic indexes up to limit pairs missing from the traffic
// index, newest first, in one transaction, and reports whether any remain.
func (repo *Repository) IndexMissingTraffic(limit int) (int, bool, error) {
	if limit < 1 {
		return 0, false, errors.New("indexing missing traffic: limit must be positive")
	}
	complete, indexedFrom := repo.index.snapshot()
	if complete {
		return 0, false, nil
	}

	var ids []uuid.UUID
	err := repo.inTx(func(tx *sqlx.Tx) error {
		var err error
		ids, err = missingFromIndex(tx, indexedFrom, limit+1)
		if err != nil {
			return err
		}
		for _, id := range ids[:min(len(ids), limit)] {
			if err := indexPair(tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("indexing missing traffic: %w", err)
	}

	remaining := len(ids) > limit
	if remaining {
		ids = ids[:limit]
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
	complete, indexedFrom := repo.index.snapshot()
	if complete {
		return true, nil
	}
	ids, err := missingFromIndex(q, indexedFrom, 1)
	if err != nil {
		return false, fmt.Errorf("checking the traffic index: %w", err)
	}
	if len(ids) > 0 {
		return false, nil
	}
	repo.index.markComplete()
	return true, nil
}

// missingFromIndex returns up to limit ids of pairs without an index row,
// newest first, below indexedFrom when it is set. The FTS5 docsize table
// holds one row per indexed rowid.
func missingFromIndex(q sqlx.Queryer, indexedFrom *uuid.UUID, limit int) ([]uuid.UUID, error) {
	query := `SELECT r.id FROM request r
		WHERE NOT EXISTS (SELECT 1 FROM traffic_fts_docsize d WHERE d.id = r.rowid)`
	args := []any{}
	if indexedFrom != nil {
		query += ` AND r.id < ?`
		args = append(args, *indexedFrom)
	}
	query += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit)

	var ids []uuid.UUID
	if err := sqlx.Select(q, &ids, query, args...); err != nil {
		return nil, fmt.Errorf("finding traffic missing from the index: %w", err)
	}
	return ids, nil
}
