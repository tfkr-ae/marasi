package db

import (
	"fmt"

	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.StatsRepository = (*Repository)(nil)

// CountRows returns the total number of request-response rows stored in the repository.
func (repo *Repository) CountRows() (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM request`

	err := repo.dbConn.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("getting request count: %w", err)
	}

	return count, nil
}

// CountNotes returns the total number of notes associated with requests.
func (repo *Repository) CountNotes() (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM notes WHERE note IS NOT NULL AND note != ''`

	err := repo.dbConn.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("getting notes count: %w", err)
	}

	return count, nil
}

// CountLaunchpads returns the total number of created launchpads.
func (repo *Repository) CountLaunchpads() (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM launchpad`

	err := repo.dbConn.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("getting launchpad count: %w", err)
	}

	return count, nil
}

// CountIntercepted returns the total number of intercepted requests.
func (repo *Repository) CountIntercepted() (int, error) {
	var count int
	query := `SELECT COUNT(*)
              FROM request
              WHERE json_extract(metadata, '$.intercepted') = true`

	err := repo.dbConn.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("getting intercepted count: %w", err)
	}

	return count, nil
}
