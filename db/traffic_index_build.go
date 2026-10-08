package db

import (
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// indexBuildState remembers what this repository already knows about the
// traffic index, so the background build does not rescan pairs it has seen.
// It is not stored: a fresh repository rediscovers the missing pairs.
type indexBuildState struct {
	mu sync.Mutex
	// indexedFrom, when set, means every pair with an id at or above it is
	// indexed. Pairs written through this repository are indexed as they are
	// written, so the bound stays true.
	indexedFrom *uuid.UUID
	complete    bool
}

// IndexMissingTraffic indexes up to limit pairs missing from the traffic
// index, newest first, in one transaction, and reports whether any remain.
func (repo *Repository) IndexMissingTraffic(limit int) (int, bool, error) {
	if limit < 1 {
		return 0, false, errors.New("indexing missing traffic: limit must be positive")
	}
	repo.index.mu.Lock()
	defer repo.index.mu.Unlock()
	if repo.index.complete {
		return 0, false, nil
	}

	tx, err := repo.dbConn.Beginx()
	if err != nil {
		return 0, false, fmt.Errorf("indexing missing traffic: starting transaction: %w", err)
	}
	defer tx.Rollback()

	ids, err := repo.missingFromIndex(tx, limit+1)
	if err != nil {
		return 0, false, err
	}
	remaining := len(ids) > limit
	if remaining {
		ids = ids[:limit]
	}
	for _, id := range ids {
		if err := indexPair(tx, id); err != nil {
			return 0, false, fmt.Errorf("indexing missing traffic: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("indexing missing traffic: committing: %w", err)
	}

	if remaining {
		repo.index.indexedFrom = &ids[len(ids)-1]
	} else {
		repo.index.complete = true
	}
	return len(ids), remaining, nil
}

// TrafficIndexComplete reports whether every pair is in the traffic index.
func (repo *Repository) TrafficIndexComplete() (bool, error) {
	repo.index.mu.Lock()
	defer repo.index.mu.Unlock()
	if repo.index.complete {
		return true, nil
	}
	ids, err := repo.missingFromIndex(repo.dbConn, 1)
	if err != nil {
		return false, err
	}
	repo.index.complete = len(ids) == 0
	return repo.index.complete, nil
}

// missingFromIndex returns up to limit ids of pairs without an index row,
// newest first, below the bound already known to be indexed. The FTS5
// docsize table holds one row per indexed rowid.
func (repo *Repository) missingFromIndex(q sqlx.Queryer, limit int) ([]uuid.UUID, error) {
	query := `SELECT r.id FROM request r
		WHERE NOT EXISTS (SELECT 1 FROM traffic_fts_docsize d WHERE d.id = r.rowid)`
	args := []any{}
	if repo.index.indexedFrom != nil {
		query += ` AND r.id < ?`
		args = append(args, *repo.index.indexedFrom)
	}
	query += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit)

	var ids []uuid.UUID
	if err := sqlx.Select(q, &ids, query, args...); err != nil {
		return nil, fmt.Errorf("finding traffic missing from the index: %w", err)
	}
	return ids, nil
}
