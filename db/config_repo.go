package db

import (
	"encoding/json"
	"fmt"

	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.ConfigRepository = (*Repository)(nil)

// UpdateSPKI implements the domain.ConfigRepository interface.
// It updates the SPKI hash value in the 'app' table of the database.
func (repo *Repository) UpdateSPKI(spki string) error {
	query := `UPDATE app SET spki = ?`
	_, err := repo.dbConn.Exec(query, spki)

	if err != nil {
		return fmt.Errorf("updating spki value %s: %w", spki, err)
	}

	return nil
}

// GetFilters implements the domain.ConfigRepository interface.
// It retrieves the Content-Type filters from the 'app' table,
// which are stored as a JSON string, and unmarshals them into a slice of strings.
func (repo *Repository) GetFilters() ([]string, error) {
	var filtersString string
	query := `SELECT filters FROM app LIMIT 1`
	err := repo.dbConn.Get(&filtersString, query)

	if err != nil {
		return nil, fmt.Errorf("getting filters: %w", err)
	}

	var filters []string
	err = json.Unmarshal([]byte(filtersString), &filters)

	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal filters JSON: %w", err)
	}

	return filters, nil
}

// SetFilters implements the domain.ConfigRepository interface.
// It marshals the provided slice of filter strings into a JSON string
// and updates the 'filters' column in the 'app' table.
func (repo *Repository) SetFilters(filters []string) error {
	marshalledFilters, err := json.Marshal(filters)
	if err != nil {
		return fmt.Errorf("failed to marshal filters: %w", err)
	}

	query := `UPDATE app SET filters = ?`
	_, err = repo.dbConn.Exec(query, marshalledFilters)

	if err != nil {
		return fmt.Errorf("failed to update filters: %w", err)
	}

	return nil
}
