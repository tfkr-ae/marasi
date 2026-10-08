package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.LogRepository = (*Repository)(nil)

// dbLog represents a log entry as stored in the database.
type dbLog struct {
	ID          uuid.UUID      `db:"id"`           // Unique identifier for the log entry.
	Timestamp   time.Time      `db:"timestamp"`    // The time at which the log entry was created.
	Level       string         `db:"level"`        // The severity level of the log.
	Message     string         `db:"message"`      // The main content of the log message.
	Context     Metadata       `db:"context"`      // A map of additional key-value data for structured logging.
	RequestID   sql.NullString `db:"request_id"`   // An optional ID of an associated HTTP request.
	ExtensionID sql.NullString `db:"extension_id"` // An optional ID of an associated extension.
}

// toDomainLog converts a dbLog to a domain.Log.
func toDomainLog(dbLog *dbLog) *domain.Log {
	log := &domain.Log{
		ID:        dbLog.ID,
		Timestamp: dbLog.Timestamp,
		Level:     dbLog.Level,
		Message:   dbLog.Message,
		Context:   map[string]any(dbLog.Context),
	}

	if dbLog.RequestID.Valid {
		if id, err := uuid.Parse(dbLog.RequestID.String); err == nil {
			log.RequestID = &id
		}
	}

	if dbLog.ExtensionID.Valid {
		if id, err := uuid.Parse(dbLog.ExtensionID.String); err == nil {
			log.ExtensionID = &id
		}
	}

	return log
}

// fromDomainLog converts a domain.Log to a dbLog.
func fromDomainLog(log *domain.Log) *dbLog {
	dbLog := &dbLog{
		ID:        log.ID,
		Timestamp: log.Timestamp,
		Level:     log.Level,
		Message:   log.Message,
		Context:   Metadata(log.Context),
	}

	if log.RequestID != nil {
		dbLog.RequestID = sql.NullString{String: log.RequestID.String(), Valid: true}
	}

	if log.ExtensionID != nil {
		dbLog.ExtensionID = sql.NullString{String: log.ExtensionID.String(), Valid: true}
	}

	return dbLog
}

// InsertLog saves a new log entry to the database.
func (repo *Repository) InsertLog(log *domain.Log) error {
	dbLog := fromDomainLog(log)
	query := `INSERT INTO logs (id, level, timestamp, message, context, request_id, extension_id)
	          VALUES (:id, :level, :timestamp, :message, :context, :request_id, :extension_id)`

	_, err := repo.dbConn.NamedExec(query, dbLog)
	if err != nil {
		return fmt.Errorf("inserting log %s: %w", log.ID, err)
	}

	return err
}

// GetLogs retrieves all log entries from the database.
func (repo *Repository) GetLogs() ([]*domain.Log, error) {
	var dbLogs []*dbLog
	query := `SELECT * FROM logs`

	err := repo.dbConn.Select(&dbLogs, query)
	if err != nil {
		return nil, fmt.Errorf("fetching all logs: %w", err)
	}

	domainLogs := make([]*domain.Log, len(dbLogs))
	for i, dbLog := range dbLogs {
		domainLogs[i] = toDomainLog(dbLog)
	}

	return domainLogs, nil
}

// ListLogs returns a newest-first page of logs older than cursor.
func (repo *Repository) ListLogs(cursor *uuid.UUID, limit int) ([]*domain.Log, *uuid.UUID, error) {
	query := `SELECT id, timestamp, level, message, context, request_id, extension_id FROM logs`
	var args []any
	if cursor != nil {
		query += ` WHERE id < ?`
		args = append(args, *cursor)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)

	var rows []*dbLog
	if err := repo.dbConn.Select(&rows, query, args...); err != nil {
		return nil, nil, fmt.Errorf("listing logs: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]*domain.Log, len(rows))
	for i, row := range rows {
		items[i] = toDomainLog(row)
	}
	var nextCursor *uuid.UUID
	if hasMore {
		id := items[len(items)-1].ID
		nextCursor = &id
	}
	return items, nextCursor, nil
}
