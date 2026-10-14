package db

import (
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
