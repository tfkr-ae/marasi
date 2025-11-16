package db

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

var _ domain.ExtensionRepository = (*Repository)(nil)

// dbExtension represents the structure of an extension as stored in the database.
// It uses the Metadata type for its settings field to handle JSON serialization.
type dbExtension struct {
	ID          uuid.UUID `db:"id"`
	Name        string    `db:"name"`
	SourceURL   string    `db:"source_url"`
	Author      string    `db:"author"`
	LuaContent  string    `db:"lua_content"`
	Enabled     bool      `db:"enabled"`
	Description string    `db:"description"`
	Settings    Metadata  `db:"settings"`
	UpdatedAt   time.Time `db:"update_at"`
}

// toDomainExtension converts a dbExtension struct to its domain.Extension representation.
func toDomainExtension(dbExt *dbExtension) *domain.Extension {
	return &domain.Extension{
		ID:          dbExt.ID,
		Name:        dbExt.Name,
		SourceURL:   dbExt.SourceURL,
		Author:      dbExt.Author,
		LuaContent:  dbExt.LuaContent,
		Enabled:     dbExt.Enabled,
		Description: dbExt.Description,
		Settings:    map[string]any(dbExt.Settings),
		UpdatedAt:   dbExt.UpdatedAt,
	}
}

// GetExtensions implements the domain.ExtensionRepository interface.
// It retrieves all extensions from the database and converts them to domain.Extension objects.
func (repo *Repository) GetExtensions() ([]*domain.Extension, error) {
	var dbExts []*dbExtension
	query := `SELECT * FROM extensions ORDER BY id ASC`

	err := repo.dbConn.Select(&dbExts, query)
	if err != nil {
		return nil, fmt.Errorf("fetching all extensions: %w", err)
	}

	domainExts := make([]*domain.Extension, len(dbExts))

	for i, dbExt := range dbExts {
		domainExts[i] = toDomainExtension(dbExt)
	}
	return domainExts, nil
}

// GetExtensionByName implements the domain.ExtensionRepository interface.
// It retrieves a single extension by its name and converts it to a domain.Extension object.
func (repo *Repository) GetExtensionByName(name string) (*domain.Extension, error) {
	var dbExt dbExtension
	query := `SELECT * FROM extensions WHERE name = ?`

	err := repo.dbConn.Get(&dbExt, query, name)
	if err != nil {
		return nil, fmt.Errorf("fetching extensions %s: %w", name, err)
	}

	return toDomainExtension(&dbExt), nil
}

// GetExtensionLuaCodeByName implements the domain.ExtensionRepository interface.
// It retrieves the Lua source code of an extension by its name.
func (repo *Repository) GetExtensionLuaCodeByName(name string) (string, error) {
	var code string
	query := `SELECT lua_content FROM extensions WHERE name = ?`

	err := repo.dbConn.Get(&code, query, name)
	if err != nil {
		return "", fmt.Errorf("getting extension %s code: %v", name, err)
	}

	return code, nil
}

// UpdateExtensionLuaCodeByName implements the domain.ExtensionRepository interface.
// It updates the Lua source code of an existing extension identified by its name.
func (repo *Repository) UpdateExtensionLuaCodeByName(name string, code string) error {
	query := `UPDATE extensions SET lua_content = ? WHERE name = ?`

	_, err := repo.dbConn.Exec(query, code, name)

	if err != nil {
		return fmt.Errorf("updating extension %s code: %v", name, err)
	}

	return nil
}

// GetExtensionSettingsByUUID implements the domain.ExtensionRepository interface.
// It retrieves the settings of an extension by its UUID.
func (repo *Repository) GetExtensionSettingsByUUID(id uuid.UUID) (map[string]any, error) {
	var settings Metadata
	query := `SELECT settings FROM extensions WHERE id = ?`

	err := repo.dbConn.Get(&settings, query, id)
	if err != nil {
		return nil, fmt.Errorf("fetching extension %s settings: %w", id, err)
	}

	return map[string]any(settings), nil
}

// SetExtensionSettingsByUUID implements the domain.ExtensionRepository interface.
// It updates the settings of an existing extension identified by its UUID.
func (repo *Repository) SetExtensionSettingsByUUID(id uuid.UUID, settings map[string]any) error {
	dbSettings := Metadata(settings)
	query := `UPDATE extensions SET settings = ? WHERE id = ?`

	_, err := repo.dbConn.Exec(query, dbSettings, id)
	if err != nil {
		return fmt.Errorf("updating settings for extension %s: %w", id, err)
	}

	return nil
}
