package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ErrLaunchpadNotFound means no launchpad has the requested id.
var ErrLaunchpadNotFound = errors.New("launchpad not found")

// ErrLaunchpadAlreadyLinked means the request is already a member of the launchpad.
var ErrLaunchpadAlreadyLinked = errors.New("request already linked to launchpad")

// ErrLaunchpadNotLinked means the request is not a member of the launchpad.
var ErrLaunchpadNotLinked = errors.New("request not linked to launchpad")

// LaunchpadRepository defines the interface for managing Launchpads, which are collections of saved requests.
// It provides methods for creating, retrieving, updating, and deleting launchpads,
// as well as managing the requests associated with them.
type LaunchpadRepository interface {
	// GetLaunchpads retrieves all launchpads configured in the application,
	// each with its entry count and newest entry's status code.
	// It returns a slice of Launchpad pointers.
	GetLaunchpads() ([]*Launchpad, error)
	// GetLaunchpad returns the launchpad identified by id, with its entry
	// count and newest entry's status code.
	// It returns ErrLaunchpadNotFound if the launchpad does not exist.
	GetLaunchpad(id uuid.UUID) (*Launchpad, error)

	// CreateLaunchpad creates a new launchpad with the given name and description.
	// It returns the UUID of the newly created launchpad.
	CreateLaunchpad(name string, description string) (uuid.UUID, error)

	// UpdateLaunchpad updates the name and description of an existing launchpad identified by its UUID.
	// It returns an error if the launchpad does not exist.
	UpdateLaunchpad(launchpadID uuid.UUID, name, description *string) error

	// DeleteLaunchpad removes a launchpad identified by its UUID.
	// It returns an error if the launchpad does not exist.
	DeleteLaunchpad(launchpadID uuid.UUID) error

	// GetLaunchpadRequests retrieves oldest-first traffic summaries for
	// requests linked to a launchpad that match a traffic query and are not
	// hidden by a content-type exclusion. An empty query matches every
	// linked request. An invalid query or exclusion returns a *QueryError.
	GetLaunchpadRequests(id uuid.UUID, query string, exclusion []string) ([]*RequestResponseSummary, error)

	// LinkRequestToLaunchpad associates a request with a launchpad using their respective UUIDs.
	// This allows for organizing requests into collections.
	// It returns an error if either the request or the launchpad does not exist.
	LinkRequestToLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error

	// UnlinkRequestFromLaunchpad removes a request from a launchpad. It
	// returns ErrLaunchpadNotLinked if the request is not linked to it.
	UnlinkRequestFromLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error
}

// Launchpad represents a collection of saved requests, allowing users to group and organize them.
type Launchpad struct {
	ID          uuid.UUID // Unique identifier for the launchpad.
	Name        string    // The name of the launchpad.
	Description string    // A brief description of the launchpad's purpose.

	// EntryCount is the number of request/response pairs linked to the launchpad.
	EntryCount int
	// LastStatusCode is the response status code of the newest entry, in the
	// order GetLaunchpadRequests returns entries. It is nil when the launchpad
	// has no entries or its newest entry has no response (in flight, cancelled, or failed).
	LastStatusCode *int
}

// LaunchpadRequest represents the association between a Launchpad and a ProxyRequest.
type LaunchpadRequest struct {
	LaunchpadID uuid.UUID // The ID of the launchpad.
	RequestID   uuid.UUID // The ID of the request linked to the launchpad.
}
