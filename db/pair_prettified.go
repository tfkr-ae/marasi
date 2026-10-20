package db

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"maps"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// The metadata keys where older binaries stored a pair's prettified copies
// (docs/adr/2026-10-14-prettified-bodies-leave-metadata.md).
const (
	prettifiedRequestKey  = "prettified-request"
	prettifiedResponseKey = "prettified-response"
)

// pairMetadata is a pair's metadata as the request table stores it. Prettified
// copies are not metadata: reading drops the keys a pair that has not moved
// yet still has, and writing never stores them.
type pairMetadata Metadata

// Scan implements sql.Scanner.
func (m *pairMetadata) Scan(value any) error {
	if err := (*Metadata)(m).Scan(value); err != nil {
		return err
	}
	delete(*m, prettifiedRequestKey)
	delete(*m, prettifiedResponseKey)
	return nil
}

// Value implements driver.Valuer.
func (m pairMetadata) Value() (driver.Value, error) {
	_, hasRequest := m[prettifiedRequestKey]
	_, hasResponse := m[prettifiedResponseKey]
	if hasRequest || hasResponse {
		m = maps.Clone(m)
		delete(m, prettifiedRequestKey)
		delete(m, prettifiedResponseKey)
	}
	return Metadata(m).Value()
}

// prettifiedCopy returns prettified, or when it is empty, the copy that a
// caller still passes in metadata under key.
func prettifiedCopy(prettified string, metadata map[string]any, key string) string {
	if prettified != "" {
		return prettified
	}
	legacy, _ := metadata[key].(string)
	return legacy
}

// savePrettified stores one prettified copy of the pair with id, in column
// request or response, in the transaction that writes that side of the pair.
func savePrettified(tx sqlx.Execer, id uuid.UUID, column, prettified string) error {
	if prettified == "" {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO pair_prettified (request_id, `+column+`) VALUES (?, ?)
		ON CONFLICT (request_id) DO UPDATE SET `+column+` = excluded.`+column, id, prettified)
	return err
}

// hasLegacyPrettified is an SQL condition on request r that is true when its
// metadata still holds a prettified copy. instr rules out most rows without
// parsing their JSON.
const hasLegacyPrettified = `instr(r.metadata, '"prettified-') > 0
	AND (json_type(r.metadata, '$."prettified-request"') IS NOT NULL
		OR json_type(r.metadata, '$."prettified-response"') IS NOT NULL)`

// movePrettified moves the prettified copies that the metadata of the pairs
// with ids still holds into pair_prettified, and removes them from metadata,
// in tx. A copy already in pair_prettified wins. Pairs without such copies are
// left alone. It returns how many pairs it moved.
func movePrettified(tx sqlx.Ext, ids ...uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	query, args, err := sqlx.In(`INSERT INTO pair_prettified (request_id, request, response)
		SELECT r.id, json_extract(r.metadata, '$."prettified-request"'), json_extract(r.metadata, '$."prettified-response"')
		FROM request r WHERE r.id IN (?) AND `+hasLegacyPrettified+`
		ON CONFLICT (request_id) DO UPDATE SET
			request = coalesce(pair_prettified.request, excluded.request),
			response = coalesce(pair_prettified.response, excluded.response)`, ids)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(query, args...); err != nil {
		return 0, fmt.Errorf("moving prettified copies: %w", err)
	}
	query, args, err = sqlx.In(`UPDATE request AS r
		SET metadata = json_remove(r.metadata, '$."prettified-request"', '$."prettified-response"')
		WHERE r.id IN (?) AND `+hasLegacyPrettified, ids)
	if err != nil {
		return 0, err
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("removing prettified copies from metadata: %w", err)
	}
	moved, err := result.RowsAffected()
	return int(moved), err
}

// MovePrettified runs one batch of the prettified_move job: it moves the
// prettified copies that older binaries left in metadata into pair_prettified,
// newest pairs first, in one transaction, and reports whether any remain. A
// batch takes pairs while their metadata fits in maxBytes, and always takes at
// least one. Once nothing remains the job is complete and later batches do
// nothing.
func (repo *Repository) MovePrettified(maxBytes int) (int, bool, error) {
	repo.jobMu.Lock()
	defer repo.jobMu.Unlock()
	var ids []uuid.UUID
	var remaining bool
	err := repo.inTx(func(tx *sqlx.Tx) error {
		job, err := loadJob(tx, domain.JobPrettifiedMove)
		if err != nil {
			return fmt.Errorf("reading the prettified move job: %w", err)
		}
		if job.Complete {
			return nil
		}
		ids, remaining, err = unmovedPrettified(tx, repo.prettifiedMovedTo, maxBytes)
		if err != nil {
			return err
		}
		if _, err := movePrettified(tx, ids...); err != nil {
			return err
		}
		return advanceJob(tx, domain.JobPrettifiedMove, len(ids), remaining)
	})
	if err != nil {
		return 0, false, fmt.Errorf("moving prettified copies: %w", err)
	}
	if remaining {
		last := ids[len(ids)-1]
		repo.prettifiedMovedTo = &last
	}
	return len(ids), remaining, nil
}

// unmovedPrettified returns ids of pairs whose metadata still holds a
// prettified copy, newest first and below movedTo when it is set, while their
// metadata fits in maxBytes. It returns at least one id when any pair is
// unmoved, and reports whether more are unmoved than it returned.
func unmovedPrettified(q sqlx.Queryer, movedTo *uuid.UUID, maxBytes int) ([]uuid.UUID, bool, error) {
	query := `SELECT r.id, octet_length(r.metadata) AS size FROM request r WHERE ` + hasLegacyPrettified
	var args []any
	if movedTo != nil {
		query += ` AND r.id < ?`
		args = append(args, *movedTo)
	}
	rows, err := q.Queryx(query+` ORDER BY r.id DESC`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("finding unmoved prettified copies: %w", err)
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
			return nil, false, fmt.Errorf("finding unmoved prettified copies: %w", err)
		}
		if len(ids) > 0 && total+pair.Size > maxBytes {
			return ids, true, nil
		}
		ids = append(ids, pair.ID)
		total += pair.Size
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("finding unmoved prettified copies: %w", err)
	}
	return ids, false, nil
}

// prettifiedColumns selects a pair's prettified copies as prettified_request
// and prettified_response from pair_prettified p, falling back to the metadata
// of request r while the pair has not moved yet.
const prettifiedColumns = `coalesce(p.request, json_extract(r.metadata, '$."prettified-request"')) AS prettified_request,
	coalesce(p.response, json_extract(r.metadata, '$."prettified-response"')) AS prettified_response`

// dbPrettified holds the columns that prettifiedColumns selects.
type dbPrettified struct {
	Request  sql.NullString `db:"prettified_request"`
	Response sql.NullString `db:"prettified_response"`
}
