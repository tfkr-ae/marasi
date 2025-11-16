package db

import (
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
}

// toDomainLaunchpad converts a dbLaunchpad to a domain.Launchpad.
func toDomainLaunchpad(dbLaunchpad *dbLaunchpad) *domain.Launchpad {
	return &domain.Launchpad{
		ID:          dbLaunchpad.ID,
		Description: dbLaunchpad.Description,
		Name:        dbLaunchpad.Name,
	}
}

// GetLaunchpads retrieves all launchpads from the database.
func (repo *Repository) GetLaunchpads() ([]*domain.Launchpad, error) {
	var dbLaunchpads []*dbLaunchpad
	query := `SELECT * FROM launchpad`

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
func (repo *Repository) UpdateLaunchpad(launchpadID uuid.UUID, name, description string) error {
	query := `UPDATE launchpad SET name = COALESCE(NULLIF(?, ''), name), description = COALESCE(NULLIF(?, ''), description) WHERE id = ?`

	result, err := repo.dbConn.Exec(query, name, description, launchpadID)
	if err != nil {
		return fmt.Errorf("updating launchpad: %w", err)
	}

	rowsAffected, err := result.RowsAffected()

	if err != nil {
		return fmt.Errorf("fetching rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no launchpad found with ID %s", launchpadID)
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

// GetLaunchpadRequests retrieves all requests associated with a specific launchpad.
func (repo *Repository) GetLaunchpadRequests(id uuid.UUID) ([]*domain.ProxyRequest, error) {
	var dbRequests []*dbRequestResponse
	query := `SELECT r.id, r.scheme, r.method, r.host, r.path, r.request_raw, r.metadata, r.requested_at
		      FROM request r
		      JOIN launchpad_request lr ON r.id = lr.request_id
		      WHERE lr.launchpad_id = ?`

	err := repo.dbConn.Select(&dbRequests, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting launchpad requests: %w", err)
	}

	domainRequests := make([]*domain.ProxyRequest, len(dbRequests))
	for i, dbReq := range dbRequests {
		domainRequests[i] = toDomainProxyRequest(dbReq)
	}

	return domainRequests, nil
}

// LinkRequestToLaunchpad creates an association between a request and a launchpad.
func (repo *Repository) LinkRequestToLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error {
	query := `INSERT INTO launchpad_request (request_id, launchpad_id) VALUES (?, ?)`

	_, err := repo.dbConn.Exec(query, requestID, launchpadID)
	if err != nil {
		return fmt.Errorf("linking request with launchpad: %w", err)
	}

	return nil
}
