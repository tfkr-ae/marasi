package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ErrInvalidCursor reports a traffic list cursor that is malformed, or that
// was made under a different sort than the read that received it.
var ErrInvalidCursor = errors.New("invalid cursor")

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
	// OrderBy is an AIP-132 sort on one sortable column, ascending or
	// descending (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md). Empty means "requested_at desc".
	OrderBy string
	// Cursor is an opaque token from an earlier TrafficPage. Empty starts at
	// the first row in display order, or the last one when walking Backward.
	Cursor string
	// Direction is the way the read walks from Cursor. The default is Forward.
	Direction Direction
	// AsOf bounds the page to pairs with an id at or below it (docs/adr/2026-10-14-query-selections-stop-at-as-of.md).
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
	// index or the annotation index is incomplete.
	Complete map[JobName]bool
}

// MaxTrafficCount is the largest count CountTraffic reports exactly. Above
// it, the count is MaxTrafficCount and reported as capped.
const MaxTrafficCount = 100_000

// TrafficCountQuery describes the pairs a traffic count counts.
type TrafficCountQuery struct {
	// Text is an AIP-160 traffic query. Empty matches every pair.
	Text string
	// Exclusion hides pairs with these content types (see NormalizeExclusion).
	// Pairs with no content type are never hidden.
	Exclusion []string
	// AsOf, when set, bounds the count to pairs with an id at or below it.
	AsOf *uuid.UUID
	// After, when set, bounds the count to pairs with an id above it. Set to
	// a snapshot's as_of, it counts the matches newer than the snapshot,
	// whatever the list's sort (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md).
	After *uuid.UUID
}

// TrafficCount is the result of a traffic count.
type TrafficCount struct {
	// Count is the number of matching pairs, at most MaxTrafficCount.
	Count int
	// Capped is true when more than MaxTrafficCount pairs match.
	Capped bool
	// Complete reports, for each background job the answer depends on,
	// whether the job had finished. See TrafficPage.Complete.
	Complete map[JobName]bool
}
