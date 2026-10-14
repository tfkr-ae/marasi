package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ErrLinkTargetNotFound means no launchpad, finding or test case has the id a
// bulk link or unlink names.
var ErrLinkTargetNotFound = errors.New("link target not found")

// ErrInvalidHighlight means a highlight is not a whole number from
// MinHighlight to MaxHighlight.
var ErrInvalidHighlight = errors.New("invalid highlight")

// The range of a highlight. Marasi gives each number no meaning; the client
// decides what it looks like and stands for.
const (
	MinHighlight = 1
	MaxHighlight = 8
)

// LinkKind names the kind of record a bulk link or unlink targets.
type LinkKind int

const (
	LinkLaunchpad LinkKind = iota + 1 // a launchpad
	LinkFinding                       // a finding
	LinkTestCase                      // a test case
)

// String returns the kind's JSON field stem: "launchpad", "finding" or
// "test_case".
func (k LinkKind) String() string {
	switch k {
	case LinkLaunchpad:
		return "launchpad"
	case LinkFinding:
		return "finding"
	case LinkTestCase:
		return "test_case"
	default:
		return "unknown"
	}
}

// LinkTarget is the launchpad, finding or test case that a bulk link or
// unlink applies to.
type LinkTarget struct {
	Kind LinkKind
	ID   uuid.UUID
}
