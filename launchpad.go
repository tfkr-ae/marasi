package marasi

import (
	"net/http"

	"github.com/google/uuid"
)

// Launchpad represents a collection or test suite for organizing related HTTP requests.
// It allows grouping requests for testing scenarios and replay functionality.
type Launchpad struct {
	ID          uuid.UUID // Unique identifier for the launchpad
	Description string    // Description of the launchpad's purpose
	Name        string    // Human-readable name
}

// LaunchpadRequest represents the association between a request and a launchpad.
// It implements the ProxyItem interface for database storage.
type LaunchpadRequest struct {
	LaunchpadID uuid.UUID // ID of the associated launchpad
	RequestID   uuid.UUID // ID of the associated request
}

func (r LaunchpadRequest) GetType() string {
	return "Launchpad Request"
}

// IsLaunchpad checks if the request contains a launchpad ID header, indicating
// it was sent from a launchpad replay operation.
//
// Parameters:
//   - req: The HTTP request to check
//
// Returns:
//   - bool: Whether the request is from a launchpad
//   - uuid.UUID: The launchpad ID, or uuid.Nil if not a launchpad request
func IsLaunchpad(req *http.Request) (bool, uuid.UUID) {
	launchpadId := req.Header.Get("x-launchpad-id")
	if launchpadId, err := uuid.Parse(launchpadId); err == nil {
		return true, launchpadId
	}
	return false, uuid.Nil
}
