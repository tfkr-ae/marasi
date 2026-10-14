package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ErrInvalidCursor reports a traffic list cursor that is malformed, or that
// was made under a different sort than the read that received it.
var ErrInvalidCursor = errors.New("invalid cursor")

// JobTrafficIndex names the background job that builds the traffic index.
const JobTrafficIndex = "traffic_index"

// Direction is the way a traffic list read walks from its cursor.
type Direction int

const (
	// Forward returns the rows after the cursor, in display order.
	Forward Direction = iota
	// Backward returns the rows before the cursor, also in display order.
	Backward
)

// TrafficQuery describes one page of a traffic list read.
type TrafficQuery struct {
	// Text is an AIP-160 traffic query. Empty matches every pair.
	Text string
	// Exclusion hides pairs with these content types (see NormalizeExclusion).
	// Pairs with no content type are never hidden.
	Exclusion []string
	// OrderBy is an AIP-132 sort. Empty means "requested_at desc", which is
	// the only sort accepted until sortable columns land (ADR-0041).
	OrderBy string
	// Cursor is an opaque token from an earlier TrafficPage. Empty starts at
	// the first row in display order, or the last one when walking Backward.
	Cursor string
	// Direction is the way the read walks from Cursor. The default is Forward.
	Direction Direction
	// AsOf bounds the page to pairs with an id at or below it (ADR-0033).
	// Nil uses the newest pair id at read time; the page reports it so the
	// client can pass it back on every later page of the same results.
	AsOf *uuid.UUID
	// Limit is the largest number of items in the page.
	Limit int
}

// TrafficPage is one page of a traffic list read.
type TrafficPage struct {
	// Items are the page's pairs in display order.
	Items []*RequestResponseSummary
	// NextCursor continues Forward after the last item. Nil at the end.
	NextCursor *string
	// PrevCursor continues Backward before the first item. Nil at the start.
	PrevCursor *string
	// AsOf is the bound the page used. Nil only when the project had no pairs.
	AsOf *uuid.UUID
	// Complete reports, for each background job the answer depends on,
	// whether the job had finished. Keys are job names such as
	// JobTrafficIndex. Text conditions can miss pairs while the traffic
	// index is incomplete.
	Complete map[string]bool
}
