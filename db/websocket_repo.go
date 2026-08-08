package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

type dbWebSocketConnection struct {
	ID          uuid.UUID    `db:"id"`
	RequestID   uuid.UUID    `db:"request_id"`
	State       string       `db:"state"`
	Transport   string       `db:"transport"`
	Host        string       `db:"host"`
	Path        string       `db:"path"`
	StartedAt   time.Time    `db:"started_at"`
	ClosedAt    sql.NullTime `db:"closed_at"`
	CloseCode   int          `db:"close_code"`
	CloseReason string       `db:"close_reason"`
}

type dbWebSocketMessage struct {
	ID           uuid.UUID `db:"id"`
	ConnectionID uuid.UUID `db:"connection_id"`
	Direction    string    `db:"direction"`
	Opcode       int       `db:"opcode"`
	Fin          int       `db:"fin"`
	Payload      []byte    `db:"payload"`
	IsBinary     int       `db:"is_binary"`
	CreatedAt    time.Time `db:"created_at"`
	Metadata     Metadata  `db:"metadata"`
}

func fromDomainWebSocketConnection(c *domain.WebSocketConnection) *dbWebSocketConnection {
	row := &dbWebSocketConnection{
		ID:          c.ID,
		RequestID:   c.RequestID,
		State:       c.State,
		Transport:   c.Transport,
		Host:        c.Host,
		Path:        c.Path,
		StartedAt:   c.StartedAt,
		CloseCode:   c.CloseCode,
		CloseReason: c.CloseReason,
	}

	if c.ClosedAt != nil {
		row.ClosedAt = sql.NullTime{Time: *c.ClosedAt, Valid: true}
	}

	return row
}

func toDomainWebSocketConnection(row *dbWebSocketConnection) *domain.WebSocketConnection {
	c := &domain.WebSocketConnection{
		ID:          row.ID,
		RequestID:   row.RequestID,
		State:       row.State,
		Transport:   row.Transport,
		Host:        row.Host,
		Path:        row.Path,
		StartedAt:   row.StartedAt,
		CloseCode:   row.CloseCode,
		CloseReason: row.CloseReason,
	}

	if row.ClosedAt.Valid {
		t := row.ClosedAt.Time
		c.ClosedAt = &t
	}

	return c
}

func fromDomainWebSocketMessage(msg *domain.WebSocketMessage) *dbWebSocketMessage {
	fin := 0
	isBinary := 0

	if msg.Fin {
		fin = 1
	}

	if msg.IsBinary {
		isBinary = 1
	}

	return &dbWebSocketMessage{
		ID:           msg.ID,
		ConnectionID: msg.ConnectionID,
		Direction:    msg.Direction,
		Opcode:       msg.Opcode,
		Fin:          fin,
		Payload:      msg.Payload,
		IsBinary:     isBinary,
		CreatedAt:    msg.CreatedAt,
		Metadata:     Metadata(msg.Metadata),
	}
}

func toDomainWebSocketMessage(row *dbWebSocketMessage) *domain.WebSocketMessage {
	return &domain.WebSocketMessage{
		ID:           row.ID,
		ConnectionID: row.ConnectionID,
		Direction:    row.Direction,
		Opcode:       row.Opcode,
		Fin:          row.Fin != 0,
		Payload:      row.Payload,
		IsBinary:     row.IsBinary != 0,
		CreatedAt:    row.CreatedAt,
		Metadata:     map[string]any(row.Metadata),
	}
}

func (repo *Repository) InsertConnection(conn *domain.WebSocketConnection) error {
	row := fromDomainWebSocketConnection(conn)
	query := `INSERT INTO websocket_connections(
				id, request_id, state, transport, host, path,
				started_at, closed_at, close_code, close_reason
			  ) VALUES (
				:id, :request_id, :state, :transport, :host, :path,
				:started_at, :closed_at, :close_code, :close_reason
			  )`

	_, err := repo.dbConn.NamedExec(query, row)
	if err != nil {
		return fmt.Errorf("inserting websocket connection %s : %w", conn.ID, err)
	}

	return nil
}

func (repo *Repository) UpdateConnection(conn *domain.WebSocketConnection) error {
	var closedAt sql.NullTime
	if conn.ClosedAt != nil {
		closedAt = sql.NullTime{Time: *conn.ClosedAt, Valid: true}
	}

	query := `UPDATE websocket_connections SET
				state = :state,
				closed_at = :closed_at,
				close_code = :close_code,
				close_reason = :close_reason
			  WHERE id = :id`

	result, err := repo.dbConn.NamedExec(query, map[string]any{
		"id":           conn.ID,
		"state":        conn.State,
		"closed_at":    closedAt,
		"close_code":   conn.CloseCode,
		"close_reason": conn.CloseReason,
	})

	if err != nil {
		return fmt.Errorf("updating websocket connection %s : %w", conn.ID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for websocket connection %s : %w", conn.ID, err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no websocket connection found with id %s to update", conn.ID)
	}

	return nil
}

func (repo *Repository) GetConnection(id uuid.UUID) (*domain.WebSocketConnection, error) {
	var row dbWebSocketConnection
	query := `SELECT id, request_id, state, transport, host, path,
					 started_at, closed_at, close_code, close_reason
			  FROM websocket_connections
			  WHERE id = ?`

	err := repo.dbConn.Get(&row, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting websocket connection %s : %w", id, err)
	}

	return toDomainWebSocketConnection(&row), nil
}

func (repo *Repository) GetConnectionByRequestID(requestID uuid.UUID) (*domain.WebSocketConnection, error) {
	var row dbWebSocketConnection
	query := `SELECT id, request_id, state, transport, host, path,
					 started_at, closed_at, close_code, close_reason
			  FROM websocket_connections
			  WHERE request_id = ?`

	err := repo.dbConn.Get(&row, query, requestID)
	if err != nil {
		return nil, fmt.Errorf("getting websocket connection for request %s : %w", requestID, err)
	}

	return toDomainWebSocketConnection(&row), nil
}

func (repo *Repository) InsertMessage(msg *domain.WebSocketMessage) error {
	row := fromDomainWebSocketMessage(msg)
	query := `INSERT INTO websocket_messages(
				id, connection_id, direction, opcode, fin, payload, is_binary, created_at, metadata
			  ) VALUES (
				:id, :connection_id, :direction, :opcode, :fin, :payload, :is_binary, :created_at, :metadata
			  )`

	_, err := repo.dbConn.NamedExec(query, row)
	if err != nil {
		return fmt.Errorf("inserting websocket message %s : %w", msg.ID, err)
	}

	return nil
}

func (repo *Repository) GetMessage(id uuid.UUID) (*domain.WebSocketMessage, error) {
	var row dbWebSocketMessage
	query := `SELECT id, connection_id, direction, opcode, fin, payload, is_binary, created_at, metadata
			  FROM websocket_messages
			  WHERE id = ?`

	err := repo.dbConn.Get(&row, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting websocket message %s : %w", id, err)
	}

	return toDomainWebSocketMessage(&row), nil
}

func (repo *Repository) GetMessages(connectionID uuid.UUID) ([]*domain.WebSocketMessage, error) {
	var rows []dbWebSocketMessage
	query := `SELECT id, connection_id, direction, opcode, fin, payload, is_binary, created_at, metadata
			  FROM websocket_messages
			  WHERE connection_id = ?
			  ORDER BY id ASC`

	err := repo.dbConn.Select(&rows, query, connectionID)
	if err != nil {
		return nil, fmt.Errorf("getting websocket messages for connection %s : %w", connectionID, err)
	}

	messages := make([]*domain.WebSocketMessage, len(rows))
	for i := range rows {
		messages[i] = toDomainWebSocketMessage(&rows[i])
	}

	return messages, nil
}

func (repo *Repository) CountMessages(connectionID uuid.UUID) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM websocket_messages WHERE connection_id = ?`

	err := repo.dbConn.Get(&count, query, connectionID)
	if err != nil {
		return 0, fmt.Errorf("counting websocket messages for connection %s : %w", connectionID, err)
	}

	return count, nil
}

func (repo *Repository) GetMessagesByRequestID(requestID uuid.UUID) ([]*domain.WebSocketMessage, error) {
	var rows []dbWebSocketMessage
	query := `SELECT m.id, m.connection_id, m.direction, m.opcode, m.fin, m.payload, m.is_binary, m.created_at, m.metadata
			  FROM websocket_messages m
			  JOIN websocket_connections c ON c.id = m.connection_id
			  WHERE c.request_id = ?
			  ORDER BY m.id ASC`

	err := repo.dbConn.Select(&rows, query, requestID)
	if err != nil {
		return nil, fmt.Errorf("getting websocket messages for request %s : %w", requestID, err)
	}

	messages := make([]*domain.WebSocketMessage, len(rows))
	for i := range rows {
		messages[i] = toDomainWebSocketMessage(&rows[i])
	}

	return messages, nil
}
