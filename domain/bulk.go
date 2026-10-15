package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ErrLinkTargetNotFound means no launchpad, finding or test case has the id a
// bulk link or unlink names.
var ErrLinkTargetNotFound = errors.New("link target not found")

// ErrInvalidMetadataKey means a bulk metadata key path is empty, has an empty
// segment or a segment with a quote or backslash, or names a key that only
// Marasi writes: prettified-request, prettified-response or has_note.
var ErrInvalidMetadataKey = errors.New("invalid metadata key")

// ErrEmptyNote means a bulk note append was given no text.
var ErrEmptyNote = errors.New("note text is empty")

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
