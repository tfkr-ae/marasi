package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrLaunchNotFound means no launch has the requested id.
var ErrLaunchNotFound = errors.New("launch not found")

// ErrLaunchFinished means the launch has already reached a terminal state.
var ErrLaunchFinished = errors.New("launch finished")

// ErrLaunchPending means an operation was refused because it would remove a
// pending launch's pair from its launchpad, delete that pair, or delete the
// launchpad a pending launch was sent from (ADR-0049). It changes nothing;
// the client retries once the launch is completed, cancelled or failed.
var ErrLaunchPending = errors.New("launch pending")

// LaunchPendingError is the ErrLaunchPending a refused operation returns.
// IDs are the pending launches' ids, which are their pairs' UUIDs, in the
// order the registry sorts them: oldest first.
type LaunchPendingError struct {
	IDs []uuid.UUID
}

func (e *LaunchPendingError) Error() string {
	ids := make([]string, len(e.IDs))
	for i, id := range e.IDs {
		ids[i] = id.String()
	}
	return fmt.Sprintf("%v: %s", ErrLaunchPending, strings.Join(ids, ", "))
}

func (e *LaunchPendingError) Unwrap() error { return ErrLaunchPending }

// LaunchStatus identifies the lifecycle state of a launch.
type LaunchStatus string

const (
	// LaunchPending indicates that the launch has not reached a terminal state.
	LaunchPending LaunchStatus = "pending"
	// LaunchCompleted indicates that the response was received and drained,
	// or that the WebSocket handshake finished.
	LaunchCompleted LaunchStatus = "completed"
	// LaunchCancelled indicates that the launch was cancelled while pending.
	LaunchCancelled LaunchStatus = "cancelled"
	// LaunchFailed indicates that the launch ended without completing. Reason says why.
	LaunchFailed LaunchStatus = "failed"
)

// LaunchReason says why a launch failed.
type LaunchReason string

const (
	// LaunchReasonDuplicateID means the launch's pair could not be stored
	// because a pair with the same UUID already exists.
	LaunchReasonDuplicateID LaunchReason = "duplicate_id"
	// LaunchReasonUpstreamError means the proxy could not complete the round trip to the upstream.
	LaunchReasonUpstreamError LaunchReason = "upstream_error"
	// LaunchReasonNetworkError means the launch could not be sent through the proxy.
	LaunchReasonNetworkError LaunchReason = "network_error"
	// LaunchReasonDropped means an extension or a Checkpoint drop stopped the launch in the proxy.
	LaunchReasonDropped LaunchReason = "dropped"
	// LaunchReasonInterrupted means a project switch or service stop ended the launch.
	LaunchReasonInterrupted LaunchReason = "interrupted"
)

// Launch is a raw request sent from a launchpad through the proxy.
// Its ID is the UUID of the request/response pair it creates.
type Launch struct {
	ID          uuid.UUID    // Launch id, equal to the pair's UUID.
	LaunchpadID uuid.UUID    // Launchpad the launch belongs to.
	Status      LaunchStatus // Lifecycle state.
	Reason      LaunchReason // Why a failed launch failed. Empty otherwise.
}
