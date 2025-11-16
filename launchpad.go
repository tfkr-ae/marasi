package marasi

import (
	"net/http"

	"github.com/google/uuid"
)

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
