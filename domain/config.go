package domain

// ConfigRepository defines the interface for managing application-level configuration settings.
// It provides methods to interact with persistent configuration data, such as security keys and UI filters.
type ConfigRepository interface {
	// UpdateSPKI saves the Subject Public Key Information (SPKI) hash to a persistent configuration file.
	// This hash is used to verify the integrity and origin of application files,
	// ensuring they are opened by a trusted Marasi instance.
	UpdateSPKI(spki string) error

	// GetFilters retrieves the list of currently configured Content-Type filters from the application's settings.
	// These filters are used by the user interface to control which traffic is displayed.
	// Note: This functionality may be relocated to a more UI-specific configuration in the future.
	GetFilters() ([]string, error)

	// SetFilters updates the list of Content-Type filters in the application's settings.
	// This allows users to customize the traffic visibility in the UI.
	// Note: This functionality may be relocated to a more UI-specific configuration in the future.
	SetFilters(filters []string) error
}
