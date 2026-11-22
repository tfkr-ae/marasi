package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

type dbStreamMessage struct {
	ID         uuid.UUID     `db:"id"`
	RequestID  uuid.UUID     `db:"request_id"`
	Direction  string        `db:"direction"`
	Framing    string        `db:"framing"`
	Flags      sql.NullInt16 `db:"flags"`
	Payload    []byte        `db:"payload"`
	Compressed bool          `db:"compressed"`
	CreatedAt  time.Time     `db:"created_at"`
	Metadata   Metadata      `db:"metadata"`
}

const streamMessageColumns = `id, request_id, direction, framing, flags, payload, compressed, created_at, metadata`

func fromDomainStreamMessage(m *domain.StreamMessage) *dbStreamMessage {
	row := &dbStreamMessage{
		ID: m.ID, RequestID: m.PairID, Direction: m.Direction, Framing: m.Framing,
		Payload: m.Payload, Compressed: m.Compressed, CreatedAt: m.CreatedAt, Metadata: Metadata(m.Metadata),
	}
	if row.Payload == nil {
		row.Payload = []byte{}
	}
	if m.Flags != nil {
		row.Flags = sql.NullInt16{Int16: int16(*m.Flags), Valid: true}
	}
	return row
}

func toDomainStreamMessage(row *dbStreamMessage) *domain.StreamMessage {
	m := &domain.StreamMessage{
		ID: row.ID, PairID: row.RequestID, Direction: row.Direction, Framing: row.Framing,
		Payload: row.Payload, Compressed: row.Compressed, CreatedAt: row.CreatedAt, Metadata: map[string]any(row.Metadata),
	}
	if m.Payload == nil {
		m.Payload = []byte{}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
	if row.Flags.Valid {
		flags := uint8(row.Flags.Int16)
		m.Flags = &flags
	}
	return m
}

// InsertStreamMessage stores one stream message.
func (repo *Repository) InsertStreamMessage(message *domain.StreamMessage) error {
	query := `INSERT INTO stream_messages(` + streamMessageColumns + `) VALUES (
				:id, :request_id, :direction, :framing, :flags, :payload, :compressed, :created_at, :metadata)`
	if _, err := repo.dbConn.NamedExec(query, fromDomainStreamMessage(message)); err != nil {
		return fmt.Errorf("inserting stream message %s: %w", message.ID, err)
	}
	return nil
}

// GetStreamMessage reads one stream message.
func (repo *Repository) GetStreamMessage(id uuid.UUID) (*domain.StreamMessage, error) {
	var row dbStreamMessage
	err := repo.dbConn.Get(&row, `SELECT `+streamMessageColumns+` FROM stream_messages WHERE id = ?`, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("getting stream message %s: %w", id, domain.ErrStreamMessageNotFound)
	case err != nil:
		return nil, fmt.Errorf("getting stream message %s: %w", id, err)
	}
	return toDomainStreamMessage(&row), nil
}

// ListStreamMessages returns a page of a pair's messages in the order they
// arrived, after cursor.
func (repo *Repository) ListStreamMessages(pairID uuid.UUID, cursor *uuid.UUID, limit int) ([]*domain.StreamMessage, *uuid.UUID, error) {
	query := `SELECT ` + streamMessageColumns + ` FROM stream_messages WHERE request_id = ?`
	args := []any{pairID}
	if cursor != nil {
		query += ` AND id > ?`
		args = append(args, *cursor)
	}
	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit+1)
	var rows []dbStreamMessage
	if err := repo.dbConn.Select(&rows, query, args...); err != nil {
		return nil, nil, fmt.Errorf("listing stream messages of pair %s: %w", pairID, err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := make([]*domain.StreamMessage, len(rows))
	for i := range rows {
		items[i] = toDomainStreamMessage(&rows[i])
	}
	var next *uuid.UUID
	if more {
		id := items[len(items)-1].ID
		next = &id
	}
	return items, next, nil
}

// RecordStreamedPair stores record's streamed state in the pair's
// request_framing, response_framing, request_stream_ending and
// response_stream_ending (db/migrations/00031_streamed_pair_state.sql). A
// nil direction or an empty ending keeps the stored value. A pair deleted
// meanwhile matches no row, which is not an error.
func (repo *Repository) RecordStreamedPair(record *domain.StreamedPairRecord) error {
	var args [4]any
	for i, d := range []*domain.StreamedDirectionState{record.State.Request, record.State.Response} {
		if d == nil {
			continue
		}
		args[i] = nullIfEmpty(d.Framing)
		args[2+i] = nullIfEmpty(d.Ending)
	}
	_, err := repo.dbConn.Exec(`UPDATE request SET
		request_framing = coalesce(?, request_framing),
		response_framing = coalesce(?, response_framing),
		request_stream_ending = coalesce(?, request_stream_ending),
		response_stream_ending = coalesce(?, response_stream_ending)
		WHERE id = ?`, args[0], args[1], args[2], args[3], record.PairID)
	if err != nil {
		return fmt.Errorf("recording streamed pair %s: %w", record.PairID, err)
	}
	return nil
}

// nullIfEmpty is value, or NULL when it is "".
func nullIfEmpty(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// dbStreamedState is a pair's streamed state columns.
type dbStreamedState struct {
	RequestFraming       sql.NullString `db:"request_framing"`
	ResponseFraming      sql.NullString `db:"response_framing"`
	RequestStreamEnding  sql.NullString `db:"request_stream_ending"`
	ResponseStreamEnding sql.NullString `db:"response_stream_ending"`
}

// streamedStateColumns are the columns a dbStreamedState reads.
const streamedStateColumns = `request_framing, response_framing, request_stream_ending, response_stream_ending`

// state returns the streamed state, nil for a pair with no streamed
// direction.
func (s dbStreamedState) state() *domain.StreamedState {
	if !s.RequestFraming.Valid && !s.ResponseFraming.Valid {
		return nil
	}
	direction := func(framing, ending sql.NullString) *domain.StreamedDirectionState {
		if !framing.Valid {
			return nil
		}
		return &domain.StreamedDirectionState{Framing: framing.String, Ending: ending.String}
	}
	return &domain.StreamedState{
		Request:  direction(s.RequestFraming, s.RequestStreamEnding),
		Response: direction(s.ResponseFraming, s.ResponseStreamEnding),
	}
}
