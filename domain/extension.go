package domain

import (
	"time"

	"github.com/google/uuid"
)

// ExtensionRepository defines the interface for managing Lua extensions.
// It provides methods for retrieving, updating, and managing extension source code and settings.
type ExtensionRepository interface {
	// GetExtensions retrieves all extensions available in the project.
	// It returns a slice of Extension pointers.
	GetExtensions() ([]*Extension, error)

	// GetExtensionByName retrieves a single extension by its unique name.
	// It returns an error if no extension with the specified name is found.
	GetExtensionByName(name string) (*Extension, error)



	// GetExtensionLuaCodeByName retrieves the Lua source code for a specific extension by its name.
	// It returns an error if the extension is not found.
	GetExtensionLuaCodeByName(name string) (string, error)

	// UpdateExtensionLuaCodeByName updates the Lua source code for a specific extension identified by its name.
	// It returns an error if the extension is not found.
	UpdateExtensionLuaCodeByName(name string, code string) error

	// GetExtensionSettingsByUUID retrieves the settings for a specific extension using its UUID.
	// Extension settings are returned as a map[string]any, allowing for flexible configuration.
	GetExtensionSettingsByUUID(id uuid.UUID) (map[string]any, error)

	// SetExtensionSettingsByUUID sets the settings for a specific extension using its UUID.
	// Extension settings are provided as a map[string]any.
	SetExtensionSettingsByUUID(id uuid.UUID, settings map[string]any) error
}

// Extension represents the domain model for a Lua-based extension in Marasi.
// This struct holds all necessary information for the runtime to execute the extension,
// including its source code, metadata, and user-configurable settings.
type Extension struct {
	ID          uuid.UUID      // Unique identifier for the extension.
	Name        string         // The unique name of the extension.
	SourceURL   string         // The URL of the extension's source code repository.
	Author      string         // The name of the extension's author or creator.
	LuaContent  string         // The Lua source code of the extension.
	Enabled     bool           // A flag indicating whether the extension is currently active.
	Description string         // A brief description of the extension's functionality.
	Settings    map[string]any // A map of user-defined settings for the extension.
	UpdatedAt   time.Time      // The timestamp of the last update to the extension.
}
