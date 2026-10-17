package db

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// indexQueueDelay is how long a captured pair may wait in
// traffic_index_pending before the repository indexes it, when no text query
// indexes it first (ADR-0026).
const indexQueueDelay = 250 * time.Millisecond

// indexQueueBatchBytes caps the stored text one indexing transaction takes
// from the queue, so a burst of large bodies does not hold the project's
// single connection for long. A pair larger than the cap is indexed alone.
const indexQueueBatchBytes = 4 << 20

// indexQueue indexes captured pairs in batches. Capture records each pair in
// traffic_index_pending in its own transaction instead of writing both
// search indexes there: an FTS5 write flushes a new segment at every commit,
// so indexing at the request and again at the response wrote four segments
// per pair. The queue indexes everything pending in one transaction, within
// indexQueueDelay of the first pair queued and before every text query.
type indexQueue struct {
	// mu guards timer and closed.
	mu     sync.Mutex
	timer  *time.Timer
	closed bool
	// flushMu serialises flushes, so two never index the same pairs.
	flushMu sync.Mutex
}

// queueIndex records that the pair with id needs both of its index rows
// written again. Call it in the transaction that captured the pair's request
// or response, then call indexQueued after it commits.
func queueIndex(tx sqlx.Execer, id uuid.UUID) error {
	_, err := tx.Exec(`INSERT INTO traffic_index_pending (request_id) VALUES (?) ON CONFLICT DO NOTHING`, id)
	if err != nil {
		return fmt.Errorf("queueing pair %s for the index: %w", id, err)
	}
	return nil
}

// indexQueued schedules a flush of the queue within indexQueueDelay, unless
// one is already scheduled or the repository is closed.
func (repo *Repository) indexQueued() {
	queue := &repo.indexQueue
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.closed || queue.timer != nil {
		return
	}
	queue.timer = time.AfterFunc(indexQueueDelay, func() {
		queue.mu.Lock()
		queue.timer = nil
		closed := queue.closed
		queue.mu.Unlock()
		if closed {
			return
		}
		// A failure of the database leaves the pairs queued; try again.
		if err := repo.flushIndexQueue(); err != nil {
			repo.indexQueued()
		}
	})
}

// closeIndexQueue stops scheduled flushes and indexes what is still queued.
// Pairs a failed flush leaves queued are indexed when the project next opens.
func (repo *Repository) closeIndexQueue() {
	queue := &repo.indexQueue
	queue.mu.Lock()
	queue.closed = true
	if queue.timer != nil {
		queue.timer.Stop()
		queue.timer = nil
	}
	queue.mu.Unlock()
	_ = repo.flushIndexQueue()
}

// flushIndexQueue indexes every queued pair, in transactions capped by
// indexQueueBatchBytes. Text queries call it first, so they read every pair
// captured before them.
func (repo *Repository) flushIndexQueue() error {
	repo.indexQueue.flushMu.Lock()
	defer repo.indexQueue.flushMu.Unlock()
	for {
		remaining, failed, err := repo.flushIndexBatch()
		if failed != nil {
			// The pair failed for an error in its own data. It leaves the
			// queue unindexed, as the background build skips such a pair,
			// and the rest of the batch is indexed without it.
			_, err = repo.dbConn.Exec(`DELETE FROM traffic_index_pending WHERE request_id = ?`, *failed)
			remaining = true
		}
		if err != nil {
			return fmt.Errorf("indexing captured traffic: %w", err)
		}
		if !remaining {
			return nil
		}
	}
}

// flushIndexBatch indexes queued pairs, oldest first, in one transaction,
// and reports whether more remain queued. When a pair fails to index for an
// error in its own data, the batch rolls back and returns that pair's id;
// a failure of the database returns only the error.
func (repo *Repository) flushIndexBatch() (bool, *uuid.UUID, error) {
	var remaining bool
	var failed *uuid.UUID
	err := repo.inTx(func(tx *sqlx.Tx) error {
		var pending []struct {
			ID   uuid.UUID `db:"request_id"`
			Size int       `db:"size"`
		}
		err := tx.Select(&pending, `SELECT p.request_id,
			coalesce(octet_length(r.request_raw), 0) + coalesce(octet_length(r.response_raw), 0) AS size
			FROM traffic_index_pending p JOIN request r ON r.id = p.request_id
			ORDER BY p.request_id`)
		if err != nil {
			return fmt.Errorf("reading the index queue: %w", err)
		}
		total := 0
		for i, pair := range pending {
			if i > 0 && total+pair.Size > indexQueueBatchBytes {
				remaining = true
				break
			}
			total += pair.Size
			if err := indexPair(tx, pair.ID); err != nil {
				if !databaseFailure(err) {
					failed = &pair.ID
				}
				return err
			}
			if _, err := tx.Exec(`DELETE FROM traffic_index_pending WHERE request_id = ?`, pair.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if failed != nil {
		return false, failed, nil
	}
	return remaining, nil, err
}

// indexReadyFor indexes every queued pair when match has a text term, so the
// term matches every pair captured before the read.
func (repo *Repository) indexReadyFor(match trafficMatch) error {
	if !match.text {
		return nil
	}
	return repo.flushIndexQueue()
}

// resumeIndexQueue schedules a flush when the project holds pairs a previous
// run queued but never indexed.
func (repo *Repository) resumeIndexQueue() {
	var queued bool
	if err := repo.dbConn.Get(&queued, `SELECT EXISTS (SELECT 1 FROM traffic_index_pending)`); err == nil && queued {
		repo.indexQueued()
	}
}
