package domain

import (
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// SitemapRulesVersion is the version of the rules SitemapPath follows. A
// project whose sitemap was built under another version rebuilds it as the
// sitemap background job (docs/adr/2026-10-14-sitemap-is-stored-by-node-and-content-type.md).
const SitemapRulesVersion = 1

// MaxSitemapPage is the most children one sitemap read returns.
const MaxSitemapPage = 500

// ErrSitemapNodeNotFound means no sitemap node has the requested name.
var ErrSitemapNodeNotFound = errors.New("sitemap node not found")

// ErrInvalidSitemapLevels means a sitemap levels read is malformed: it has
// no nodes or too many, an unknown scope, or the matching scope without a
// selection.
var ErrInvalidSitemapLevels = errors.New("invalid sitemap levels read")

// SitemapNode is one node on a pair's path through the sitemap.
type SitemapNode struct {
	// Name is the node's canonical path, such as
	// https://api.x.com/users/{int}/orders. It is the value of the sitemap
	// query field.
	Name string
	// Segment is the last part of Name: the origin for a top-level node,
	// otherwise one escaped path segment or placeholder.
	Segment string
}

// SitemapChild is a child of a sitemap node, with counts over its subtree.
type SitemapChild struct {
	Name    string `json:"name"`
	Segment string `json:"segment"`
	// Pairs and Errors count the pairs, and the pairs with a 5xx response,
	// in the node's subtree, leaving out the read's content-type exclusion.
	Pairs       int64 `json:"pairs"`
	Errors      int64 `json:"errors"`
	HasChildren bool  `json:"has_children"`
}

// SitemapQuery reads one page of a sitemap node's children (docs/adr/2026-10-14-sitemap-is-stored-by-node-and-content-type.md).
//
// Without Query it reads the stored all-traffic sitemap, leaving out the
// content types in Exclusion. With Query it is in query mode: it counts
// exactly the pairs that match Query.Text, are not hidden by
// Query.Exclusion, and have an id at or below Query.AsOf (docs/adr/2026-10-14-query-selections-stop-at-as-of.md). In
// query mode Exclusion must be empty.
type SitemapQuery struct {
	Node      string          // the node's name; "" for the origins
	Exclusion []string        // all-traffic mode only
	Query     *SelectionQuery // query mode when set
	Cursor    string          // continues a previous page; "" for the first
	Limit     int             // capped at MaxSitemapPage
}

// SitemapPage is one page of a sitemap node's children, by pair count,
// highest first.
type SitemapPage struct {
	Children []SitemapChild
	// NextCursor continues after the last child. Nil at the end.
	NextCursor *string
	// Complete reports whether the sitemap job had finished. Keyed by
	// JobSitemap. Pairs the job has not reached are not counted.
	Complete map[JobName]bool
}

// SitemapScope is which children a sitemap levels read lists.
type SitemapScope string

const (
	// SitemapScopeAll lists every child with pairs in all traffic, by its
	// all-traffic pair count. It is the default.
	SitemapScopeAll SitemapScope = "all"
	// SitemapScopeMatching lists only the children with pairs the selection
	// matches, by their matching pair count, as query mode does.
	SitemapScopeMatching SitemapScope = "matching"
)

// MaxSitemapLevels is the most nodes one sitemap levels read takes.
const MaxSitemapLevels = 64

// SitemapLevelsQuery reads one page of the children of each of several
// sitemap nodes in one call, such as every level a client has open. With a
// Selection, the matching pairs are resolved once for the whole call.
type SitemapLevelsQuery struct {
	Nodes []string // node names; "" for the origins
	// Scope chooses the children listed; "" is SitemapScopeAll.
	// SitemapScopeMatching needs a Selection.
	Scope SitemapScope
	// Exclusion is left out of the all-traffic Pairs and Errors.
	Exclusion []string
	// Selection, when set, gives each child its Matching and MatchingErrors
	// counts: the pairs that match Selection.Text, are not hidden by
	// Selection.Exclusion, and have an id at or below Selection.AsOf
	// (docs/adr/2026-10-14-query-selections-stop-at-as-of.md).
	Selection *SelectionQuery
	// Cursors continue a node's earlier page, keyed by node name. A cursor
	// belongs to the scope it came from. ListSitemapChildren takes a
	// matching-scope cursor in query mode and an all-scope cursor without a
	// query.
	Cursors map[string]string
	Limit   int // per node, capped at MaxSitemapPage
}

// SitemapLevelChild is a child in a sitemap levels read.
type SitemapLevelChild struct {
	// SitemapChild holds the all-traffic Pairs and Errors, leaving out the
	// read's Exclusion. HasChildren is over all traffic in the all scope,
	// and over the matching pairs in the matching scope.
	SitemapChild
	// Matching and MatchingErrors count the subtree's pairs, and those with
	// a 5xx response, that the selection matches. Nil without a selection.
	Matching       *int64 `json:"matching"`
	MatchingErrors *int64 `json:"matching_errors"`
}

// SitemapLevel is one page of one node's children in a sitemap levels read,
// by the scope's pair count, highest first.
type SitemapLevel struct {
	Node     string
	Children []SitemapLevelChild
	// NextCursor continues after the last child. Nil at the end.
	NextCursor *string
	// Missing reports that no node has the name Node, for example because
	// its pairs were deleted.
	Missing bool
}

// SitemapLevels is the answer to a SitemapLevelsQuery: one level per node,
// in the order of its Nodes.
type SitemapLevels struct {
	Levels []SitemapLevel
	// Complete reports whether the background jobs the counts depend on had
	// finished: the sitemap job, and the search index jobs when the
	// selection has a text term.
	Complete map[JobName]bool
}

// SitemapPath returns the sitemap nodes from a pair's origin down to its leaf,
// from the pair's stored scheme, host and path (docs/adr/2026-10-14-sitemap-is-stored-by-node-and-content-type.md). Capture, the
// sitemap job and tests all use it, so they build the same tree.
//
//   - The origin is scheme://host, lowercased, keeping any port.
//   - The path loses its query and splits on / in its escaped form, so an
//     escaped slash stays inside its segment. Empty segments are dropped.
//   - A segment that unescapes to a UUID, to decimal digits, or to hex of 32,
//     40 or 64 characters collapses to {uuid}, {int} or {hash}.
//   - A literal { or } is escaped, so no segment collides with a placeholder.
func SitemapPath(scheme, host, path string) []SitemapNode {
	origin := strings.ToLower(scheme + "://" + host)
	nodes := []SitemapNode{{Name: origin, Segment: origin}}
	name := origin
	for _, segment := range sitemapSegments(path) {
		name += "/" + segment
		nodes = append(nodes, SitemapNode{Name: name, Segment: segment})
	}
	return nodes
}

// SitemapLeaf returns the name of a pair's leaf node in the sitemap.
func SitemapLeaf(scheme, host, path string) string {
	nodes := SitemapPath(scheme, host, path)
	return nodes[len(nodes)-1].Name
}

// sitemapSegments splits a stored path into its sitemap segments.
func sitemapSegments(path string) []string {
	path, _, _ = strings.Cut(path, "?")
	path = strings.NewReplacer("{", "%7B", "}", "%7D").Replace(path)
	u := url.URL{Path: path}
	if decoded, err := url.PathUnescape(path); err == nil {
		// EscapedPath keeps the stored escaping when it is valid, so %2F
		// stays escaped and does not split.
		u = url.URL{Path: decoded, RawPath: path}
	}
	var segments []string
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		if segment != "" {
			segments = append(segments, placeholder(segment))
		}
	}
	return segments
}

// placeholder returns the placeholder an escaped segment collapses to, or the
// segment itself.
func placeholder(segment string) string {
	value, err := url.PathUnescape(segment)
	if err != nil {
		value = segment
	}
	switch {
	case len(value) == 36 && isUUID(value):
		return "{uuid}"
	case allBytes(value, isDigit):
		return "{int}"
	case (len(value) == 32 || len(value) == 40 || len(value) == 64) && allBytes(value, isHexDigit):
		return "{hash}"
	}
	return segment
}

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func allBytes(value string, ok func(byte) bool) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		if !ok(value[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
