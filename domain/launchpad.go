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

// ErrInvalidPlacement means a move names an entry to place the moved entries
// before that is not an entry of the target launchpad, or is itself moved.
var ErrInvalidPlacement = errors.New("invalid placement")

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
	// It returns an error if the launchpad does not exist. It does not
	// check for pending launches; Proxy.DeleteLaunchpad does (ADR-0053).
	DeleteLaunchpad(launchpadID uuid.UUID) error

	// GetLaunchpadRequests retrieves traffic summaries for requests linked
	// to a launchpad that match a traffic query and are not hidden by a
	// content-type exclusion, in the AIP-132 orderBy order (see
	// TrafficQuery.OrderBy). An empty orderBy lists them in entry order. An
	// empty query matches every linked request. complete reports, for each
	// background job the answer depends on, whether it had finished, as
	// TrafficPage.Complete does. An invalid query, exclusion or orderBy
	// returns a *QueryError.
	GetLaunchpadRequests(id uuid.UUID, query string, exclusion []string, orderBy string) (items []*RequestResponseSummary, complete map[JobName]bool, err error)

	// LinkRequestToLaunchpad makes a request the launchpad's last entry.
	// It returns ErrLaunchpadAlreadyLinked, and leaves the entry in its
	// place, if the request is already an entry, and an error if either the
	// request or the launchpad does not exist.
	LinkRequestToLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error

	// UnlinkRequestFromLaunchpad removes a request from a launchpad. It
	// returns ErrLaunchpadNotLinked if the request is not linked to it.
	// It does not check for pending launches;
	// Proxy.UnlinkRequestFromLaunchpad does (ADR-0053).
	UnlinkRequestFromLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error

	// MoveLaunchpadEntries moves entries in one transaction (ADR-0052) and
	// returns the moved pair ids, deduplicated, in their new entry order.
	// Only the moved entries change place. Moved to another launchpad, the
	// pairs leave the source; a move never copies. It refuses the whole
	// move, and changes nothing, with ErrLaunchpadNotFound if the source or
	// the target launchpad does not exist, ErrLaunchpadNotLinked if a pair
	// is not an entry of the source, ErrLaunchpadAlreadyLinked if another
	// target already holds a pair, and ErrInvalidPlacement if BeforeID is
	// not an entry of the target or is one of the moved pairs. It leaves
	// the pairs' metadata, launchpad_id included, unchanged (ADR-0053). It
	// does not check for pending launches; Proxy.MoveLaunchpadEntries does.
	MoveLaunchpadEntries(move LaunchpadMove) ([]uuid.UUID, error)
}

// LaunchpadMove moves entries of a source launchpad to a place in a target
// launchpad, which may be the source. The moved entries keep their entry
// order relative to each other, whatever the order of RequestIDs; a
// repeated id counts once.
type LaunchpadMove struct {
	SourceID   uuid.UUID   // Launchpad the entries are in.
	TargetID   uuid.UUID   // Launchpad the entries move to; may equal SourceID.
	RequestIDs []uuid.UUID // Pair ids of the entries to move.
	// BeforeID is the entry of the target to place the moved entries
	// before. uuid.Nil places them after the target's last entry.
	BeforeID uuid.UUID
}

// Launchpad represents a collection of saved requests, allowing users to group and organize them.
type Launchpad struct {
	ID          uuid.UUID // Unique identifier for the launchpad.
	Name        string    // The name of the launchpad.
	Description string    // A brief description of the launchpad's purpose.

	// EntryCount is the number of request/response pairs linked to the launchpad.
	EntryCount int
	// LastStatusCode is the response status code of the newest entry by
	// capture, whatever its place in entry order. It is nil when the launchpad
	// has no entries or its newest entry has no response (in flight, cancelled, or failed).
	LastStatusCode *int
}

// LaunchpadRequest represents the association between a Launchpad and a ProxyRequest.
type LaunchpadRequest struct {
	LaunchpadID uuid.UUID // The ID of the launchpad.
	RequestID   uuid.UUID // The ID of the request linked to the launchpad.
}
