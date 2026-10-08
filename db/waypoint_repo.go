package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.WaypointRepository = (*Repository)(nil)

var (
	// ErrNoWaypointForHostname is returned when a waypoint is not found for a given hostname.
	ErrNoWaypointForHostname = domain.ErrNoWaypointForHostname
)

// dbWaypoint represents a waypoint as stored in the database.
type dbWaypoint struct {
	Hostname string `db:"hostname"` // The original "host:port" to match on incoming requests.
	Override string `db:"override"` // The new "host:port" destination.
}

// toDomainWaypoint converts a dbWaypoint to a domain.Waypoint.
func toDomainWaypoint(dbWaypoint *dbWaypoint) *domain.Waypoint {
	return &domain.Waypoint{
		Hostname: dbWaypoint.Hostname,
		Override: dbWaypoint.Override,
	}
}

// GetWaypoints retrieves all configured waypoints from the database.
func (repo *Repository) GetWaypoints() ([]*domain.Waypoint, error) {
	var dbWaypoints []*dbWaypoint
	query := `SELECT hostname, override FROM waypoint ORDER BY hostname`

	err := repo.dbConn.Select(&dbWaypoints, query)
	if err != nil {
		return nil, fmt.Errorf("retrieving waypoints: %w", err)
	}

	domainWaypoints := make([]*domain.Waypoint, len(dbWaypoints))
	for i, dbWaypoint := range dbWaypoints {
		domainWaypoints[i] = toDomainWaypoint(dbWaypoint)
	}

	return domainWaypoints, nil
}

// CreateWaypoint inserts a waypoint without replacing an existing hostname.
func (repo *Repository) CreateWaypoint(hostname string, override string) error {
	result, err := repo.dbConn.Exec(`INSERT OR IGNORE INTO waypoint(hostname, override) VALUES (?, ?)`, hostname, override)
	if err != nil {
		return fmt.Errorf("creating waypoint for %s: %w", hostname, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking waypoint creation for %s: %w", hostname, err)
	}
	if rowsAffected == 0 {
		return domain.ErrWaypointAlreadyExists
	}
	return nil
}

// CreateOrUpdateWaypoint creates a new waypoint or updates an existing one.
func (repo *Repository) CreateOrUpdateWaypoint(hostname string, override string) error {
	query := `INSERT INTO waypoint(hostname, override)
		      VALUES (?, ?)
		      ON CONFLICT(hostname) DO UPDATE SET override=excluded.override`

	_, err := repo.dbConn.Exec(query, hostname, override)
	if err != nil {
		return fmt.Errorf("creating or updating waypoint for %s: %w", hostname, err)
	}

	return nil
}

// UpdateWaypoint changes an existing waypoint and reports whether the override changed.
func (repo *Repository) UpdateWaypoint(hostname string, override string) (bool, error) {
	var current string
	if err := repo.dbConn.Get(&current, `SELECT override FROM waypoint WHERE hostname = ?`, hostname); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, domain.ErrNoWaypointForHostname
		}
		return false, fmt.Errorf("retrieving waypoint for %s: %w", hostname, err)
	}
	if current == override {
		return false, nil
	}
	result, err := repo.dbConn.Exec(`UPDATE waypoint SET override = ? WHERE hostname = ?`, override, hostname)
	if err != nil {
		return false, fmt.Errorf("updating waypoint for %s: %w", hostname, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking waypoint update for %s: %w", hostname, err)
	}
	if rowsAffected == 0 {
		return false, domain.ErrNoWaypointForHostname
	}
	return true, nil
}

// DeleteWaypoint removes the waypoint associated with the specified hostname.
func (repo *Repository) DeleteWaypoint(hostname string) error {
	query := `DELETE FROM waypoint WHERE hostname = ?`

	result, err := repo.dbConn.Exec(query, hostname)
	if err != nil {
		return fmt.Errorf("deleting waypoint for %s: %w", hostname, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking deletion rows affected for %s: %w", hostname, err)
	}

	if rowsAffected == 0 {
		return ErrNoWaypointForHostname
	}

	return nil
}
