package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.LaunchpadRepository = (*Repository)(nil)

// dbLaunchpad represents a launchpad as stored in the database.
type dbLaunchpad struct {
	ID          uuid.UUID `db:"id"`          // Unique identifier for the launchpad.
	Description string    `db:"description"` // Description of the launchpad.
	Name        string    `db:"name"`        // Name of the launchpad.

	EntryCount      int            `db:"entry_count"`       // Number of linked request/response pairs.
	NewestRequestID sql.NullString `db:"newest_request_id"` // Request id of the newest entry; NULL if none.
	LastStatusCode  sql.NullInt64  `db:"last_status_code"`  // Newest entry's status code; NULL if none.
}

// toDomainLaunchpad converts a dbLaunchpad to a domain.Launchpad.
func toDomainLaunchpad(dbLaunchpad *dbLaunchpad) *domain.Launchpad {
	launchpad := &domain.Launchpad{
		ID:          dbLaunchpad.ID,
		Description: dbLaunchpad.Description,
		Name:        dbLaunchpad.Name,
		EntryCount:  dbLaunchpad.EntryCount,
	}
	if dbLaunchpad.LastStatusCode.Valid {
		code := int(dbLaunchpad.LastStatusCode.Int64)
		launchpad.LastStatusCode = &code
	}
	return launchpad
}

// launchpadSummaryQuery selects launchpads with their entry count and the
// status code of their newest entry in one aggregate statement. Callers append
// the GROUP BY. The newest entry has the greatest request id, so it is the last
// entry in GetLaunchpadRequests order. Because the query has exactly one max()
// aggregate, SQLite takes the bare r.status_code from the row that holds
// MAX(r.id). A newest entry with no response (status_code -1 or NULL)
// gives NULL; the query does not fall back to an older entry.
const launchpadSummaryQuery = `SELECT
		l.id, l.name, l.description,
		COUNT(r.id) AS entry_count,
		MAX(r.id) AS newest_request_id,
		CASE WHEN r.status_code > 0 THEN r.status_code END AS last_status_code
	FROM launchpad l
	LEFT JOIN launchpad_request lr ON lr.launchpad_id = l.id
	LEFT JOIN request r ON r.id = lr.request_id`

// GetLaunchpads retrieves all launchpads with their entry summaries.
func (repo *Repository) GetLaunchpads() ([]*domain.Launchpad, error) {
	var dbLaunchpads []*dbLaunchpad
	query := launchpadSummaryQuery + ` GROUP BY l.id ORDER BY l.id ASC`

	err := repo.dbConn.Select(&dbLaunchpads, query)
	if err != nil {
		return nil, fmt.Errorf("getting launchpads: %w", err)
	}

	domainLaunchpads := make([]*domain.Launchpad, len(dbLaunchpads))
	for i, dbLp := range dbLaunchpads {
		domainLaunchpads[i] = toDomainLaunchpad(dbLp)
	}
	return domainLaunchpads, nil
}

// GetLaunchpad retrieves one launchpad by id with its entry summary.
func (repo *Repository) GetLaunchpad(id uuid.UUID) (*domain.Launchpad, error) {
	var launchpad dbLaunchpad
	query := launchpadSummaryQuery + ` WHERE l.id = ? GROUP BY l.id`
	if err := repo.dbConn.Get(&launchpad, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrLaunchpadNotFound
		}
		return nil, fmt.Errorf("getting launchpad %s: %w", id, err)
	}
	return toDomainLaunchpad(&launchpad), nil
}

// CreateLaunchpad creates a new launchpad in the database.
func (repo *Repository) CreateLaunchpad(name string, description string) (uuid.UUID, error) {
	launchpadUUID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generating uuid: %w", err)
	}

	query := `INSERT INTO launchpad(id, description, name) VALUES (?,?,?)`

	_, err = repo.dbConn.Exec(query, launchpadUUID, description, name)
	if err != nil {
		return uuid.Nil, fmt.Errorf("creating new launchpad %s: %w", name, err)
	}

	return launchpadUUID, nil
}

// UpdateLaunchpad updates an existing launchpad in the database.
func (repo *Repository) UpdateLaunchpad(launchpadID uuid.UUID, name, description *string) error {
	query := `UPDATE launchpad SET name = COALESCE(?, name), description = COALESCE(?, description) WHERE id = ?`

	result, err := repo.dbConn.Exec(query, name, description, launchpadID)
	if err != nil {
		return fmt.Errorf("updating launchpad: %w", err)
	}

	rowsAffected, err := result.RowsAffected()

	if err != nil {
		return fmt.Errorf("fetching rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return domain.ErrLaunchpadNotFound
	}

	return nil
}

// DeleteLaunchpad removes a launchpad from the database.
func (repo *Repository) DeleteLaunchpad(launchpadID uuid.UUID) error {
	query := `DELETE FROM launchpad WHERE id = ?`

	result, err := repo.dbConn.Exec(query, launchpadID)
	if err != nil {
		return fmt.Errorf("deleting launchpad %s: %w", launchpadID, err)
	}

	rowsAffected, err := result.RowsAffected()

	if err != nil {
		return fmt.Errorf("fetching rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no launchpad with id %s", launchpadID)
	}

	return nil
}

// GetLaunchpadRequests retrieves the requests linked to a launchpad that
// match trafficQuery, oldest first. The read is the launchpad's membership
// plus the translated traffic query. An invalid query returns a
// *domain.QueryError.
func (repo *Repository) GetLaunchpadRequests(id uuid.UUID, trafficQuery string) ([]*domain.RequestResponseSummary, error) {
	translated, err := translateTrafficQuery(trafficQuery)
	if err != nil {
		return nil, err
	}

	query := `SELECT
			  id, scheme, method, host, path, requested_at,
			  status, status_code, content_type, length, responded_at,
			  json_remove(metadata, '$.prettified-request', '$.prettified-response') AS metadata
		      FROM request
		      WHERE ` + launchpadMember
	args := append(make([]any, 0, len(translated.args)+1), id)
	if translated.where != "" {
		query += ` AND (` + translated.where + `)`
		args = append(args, translated.args...)
	}
	query += ` ORDER BY id ASC`

	var dbRequests []*dbRequestResponseSummary
	err = repo.dbConn.Select(&dbRequests, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting launchpad requests: %w", err)
	}

	domainRequests := make([]*domain.RequestResponseSummary, len(dbRequests))
	for i, dbReq := range dbRequests {
		domainRequests[i] = toDomainRequestResponseSummary(dbReq)
	}

	return domainRequests, nil
}

// LinkRequestToLaunchpad creates an association between a request and a launchpad.
func (repo *Repository) LinkRequestToLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error {
	query := `INSERT OR IGNORE INTO launchpad_request (request_id, launchpad_id) VALUES (?, ?)`

	result, err := repo.dbConn.Exec(query, requestID, launchpadID)
	if err != nil {
		return fmt.Errorf("linking request with launchpad: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("fetching linked rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return domain.ErrLaunchpadAlreadyLinked
	}

	return nil
}
