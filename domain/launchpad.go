package domain

import "github.com/google/uuid"

// LaunchpadRepository defines the interface for managing Launchpads, which are collections of saved requests.
// It provides methods for creating, retrieving, updating, and deleting launchpads,
// as well as managing the requests associated with them.
type LaunchpadRepository interface {
	// GetLaunchpads retrieves all launchpads configured in the application.
	// It returns a slice of Launchpad pointers.
	GetLaunchpads() ([]*Launchpad, error)

	// CreateLaunchpad creates a new launchpad with the given name and description.
	// It returns the UUID of the newly created launchpad.
	CreateLaunchpad(name string, description string) (uuid.UUID, error)

	// UpdateLaunchpad updates the name and description of an existing launchpad identified by its UUID.
	// It returns an error if the launchpad does not exist.
	UpdateLaunchpad(launchpadID uuid.UUID, name, description string) error

	// DeleteLaunchpad removes a launchpad identified by its UUID.
	// It returns an error if the launchpad does not exist.
	DeleteLaunchpad(launchpadID uuid.UUID) error

	// GetLaunchpadRequests retrieves all requests linked to a specific launchpad, identified by its UUID.
	// It returns a slice of ProxyRequest pointers. If the launchpad has no requests, it returns an empty slice.
	GetLaunchpadRequests(id uuid.UUID) ([]*ProxyRequest, error)

	// LinkRequestToLaunchpad associates a request with a launchpad using their respective UUIDs.
	// This allows for organizing requests into collections.
	// It returns an error if either the request or the launchpad does not exist.
	LinkRequestToLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error
}

// Launchpad represents a collection of saved requests, allowing users to group and organize them.
type Launchpad struct {
	ID          uuid.UUID // Unique identifier for the launchpad.
	Name        string    // The name of the launchpad.
	Description string    // A brief description of the launchpad's purpose.
}

// LaunchpadRequest represents the association between a Launchpad and a ProxyRequest.
type LaunchpadRequest struct {
	LaunchpadID uuid.UUID // The ID of the launchpad.
	RequestID   uuid.UUID // The ID of the request linked to the launchpad.
}
