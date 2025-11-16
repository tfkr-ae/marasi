package domain

// WaypointRepository defines the interface for managing Waypoints, which are rules for redirecting traffic.
// Both hostname and override are expected to be in the "host:port" format.
type WaypointRepository interface {
	// GetWaypoints retrieves all configured waypoints from the repository.
	GetWaypoints() ([]*Waypoint, error)

	// CreateOrUpdateWaypoint creates a new waypoint or updates an existing one.
	// If a waypoint for the given hostname already exists, its override value will be updated.
	CreateOrUpdateWaypoint(hostname string, override string) error

	// DeleteWaypoint removes the waypoint associated with the specified hostname.
	// It returns an error if no waypoint is configured for that hostname.
	DeleteWaypoint(hostname string) error
}

// Waypoint represents a traffic redirection rule.
// It maps an original destination (Hostname) to a new destination (Override).
// When a request's host matches the Waypoint's Hostname, it will be redirected to the Override address.
type Waypoint struct {
	Hostname string // The original "host:port" to match on incoming requests.
	Override string // The new "host:port" destination to which the request will be redirected.
}
