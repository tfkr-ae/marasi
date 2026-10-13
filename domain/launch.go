package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ErrLaunchNotFound means no launch has the requested id.
var ErrLaunchNotFound = errors.New("launch not found")

// ErrLaunchFinished means the launch has already reached a terminal state.
var ErrLaunchFinished = errors.New("launch finished")

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
