package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// RawField type is used for the request and response raw fields
//
// By default []byte MarshallJSON will encode the []byte value to base64
// MarshalJson is implemented for RawField to directly marshall the "string" bytes
// TODO: Invalid UTF-8 strings need to be handled, the headers and the body should be split
type RawField []byte

// MarshalJSON implements the json.Marshaler interface. It marshals the raw bytes
// as a JSON string, bypassing the default base64 encoding for []byte.
func (r RawField) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}

	return json.Marshal(string(r))
}

// TrafficRepository is the interface that holds all the traffic related repository methods in Marasi
type TrafficRepository interface {
	// InsertRequest will insert the ProxyRequest in the DB
	InsertRequest(req *ProxyRequest) error

	// InsertResponse will update the row with the response data
	// It will use res.ID to find the row ID, it will return an error if the request ID was not found
	InsertResponse(res *ProxyResponse) error

	// GetResponse will return the response data from a row from the request ID
	// It will return an error if the request ID doesn't exist
	// If the request does not have response data it will return the default fields from the DB:
	/*
		ID:         reqID,
		Status:     "N/A",
		StatusCode: -1,
		Length:     "0",
		Metadata:   make(map[string]any),
		Raw:        nil,
	*/
	GetResponse(id uuid.UUID) (*ProxyResponse, error)

	// GetRequestResponseRow will return the entire request - response data for a row given from the ID
	// If the row doesn't exist it will return an error
	// If there is a note on that request ID it will fetch the note contents as well.
	// When there is no response data for the row, it will return the same empty ProxyResponse as GetResponse
	GetRequestResponseRow(id uuid.UUID) (*RequestResponseRow, error)
	// GetRequestResponseRows will return the entire request - response data for a slice of IDs.
	GetRequestResponseRows(ids []uuid.UUID) ([]*RequestResponseRow, error)

	//GetRequestResponseSummary will return the request-response data without the raw and prettified fields
	GetRequestResponseSummary() ([]*RequestResponseSummary, error)

	// ListTraffic returns one page of the pairs that match q, in display
	// order, with cursors for both directions, the as_of bound it used, and
	// the completeness of each background job the answer depends on. An
	// invalid query, exclusion or sort returns a *QueryError; a malformed
	// cursor, or one made under another sort, wraps ErrInvalidCursor.
	ListTraffic(q TrafficQuery) (TrafficPage, error)

	// CountTraffic counts the pairs that match q, exactly up to
	// MaxTrafficCount and capped above it, with the completeness of each
	// background job the answer depends on. An invalid query or exclusion
	// returns a *QueryError.
	CountTraffic(q TrafficCountQuery) (TrafficCount, error)

	// ListSitemapChildren returns one page of the children of the sitemap
	// node named node, or of the origins when node is empty, by pair count,
	// highest first, with the exclusion's content types left out of the
	// counts. An unknown node returns ErrSitemapNodeNotFound, an invalid
	// exclusion a *QueryError, and a malformed cursor wraps ErrInvalidCursor.
	ListSitemapChildren(node string, exclusion []string, cursor string, limit int) (SitemapPage, error)

	// GetMetadata returns the metadata map for a specific request ID.
	GetMetadata(id uuid.UUID) (metadata map[string]any, err error)

	// UpdateMetadata updates the metadata for one or more requests.
	UpdateMetadata(metadata map[string]any, ids ...uuid.UUID) error

	// GetNote retrieves the user-created note for a specific request ID.
	// It returns an error if no note is found.
	GetNote(requestID uuid.UUID) (string, error)

	// UpdateNote creates or updates the user-created note for a specific request ID.
	UpdateNote(requestID uuid.UUID, note string) error

	// DeleteNote removes the note row for a request ID.
	// It returns an error if no note is found.
	DeleteNote(requestID uuid.UUID) error

	// ListNotes returns a newest-first page of summaries that have a non-empty note.
	// A nil cursor starts at the newest row. nextCursor is the last returned
	// item's id when another older page exists, otherwise nil.
	ListNotes(cursor *uuid.UUID, limit int) (items []*RequestResponseSummary, nextCursor *uuid.UUID, err error)

	// SearchByMetadata retrieves requests where the value at the specified JSON path matches the provided value.
	SearchByMetadata(path string, value any) ([]*RequestResponseSummary, error)

	// CountContentTypes returns every distinct response content type with its
	// row count, ordered by count descending and then content type ascending.
	// Pairs with no content type, including in-flight requests, are counted
	// under ContentType "". The counts come from the stored sitemap's
	// buckets, or from the pairs themselves while the sitemap job is
	// incomplete (ADR-0040).
	CountContentTypes() (ContentTypeCounts, error)

	// LinkTraffic links every pair in sel to target in one transaction and
	// returns the number of pairs it newly linked; pairs already linked are
	// not counted. Unknown ids in an id selection are ignored. A missing
	// target returns ErrLinkTargetNotFound; an invalid query selection
	// returns a *QueryError.
	LinkTraffic(sel Selection, target LinkTarget) (int, error)

	// UnlinkTraffic removes the links between every pair in sel and target
	// in one transaction and returns the number of pairs that were linked.
	// It returns the same errors as LinkTraffic.
	UnlinkTraffic(sel Selection, target LinkTarget) (int, error)

	// SetMetadataKey sets the metadata key at path to the JSON value on every
	// pair in sel, in one transaction, and returns the number of pairs whose
	// metadata changed. path is dotted, as in a query: "a.b" is key b inside
	// key a. Every other key is left as it was. An invalid or reserved path
	// wraps ErrInvalidMetadataKey; an invalid query selection returns a
	// *QueryError.
	SetMetadataKey(sel Selection, path string, value json.RawMessage) (int, error)

	// ClearMetadataKey removes the metadata key at path from every pair in
	// sel, in one transaction, and returns the number of pairs that had it.
	// It returns the same errors as SetMetadataKey.
	ClearMetadataKey(sel Selection, path string) (int, error)

	// AppendNote gives every pair in sel with no note the note text, and
	// adds a blank line and text to every note that exists, in one
	// transaction. It returns the number of notes written. An empty text
	// returns ErrEmptyNote; an invalid query selection returns a *QueryError.
	AppendNote(sel Selection, text string) (int, error)

	// ClearNotes deletes the note of every pair in sel in one transaction
	// and returns the number of notes deleted.
	ClearNotes(sel Selection) (int, error)
}

// ProxyRequest represents the data captured from an HTTP request.
type ProxyRequest struct {
	ID          uuid.UUID      // Unique identifier for the request
	Scheme      string         // URL scheme (http or https)
	Method      string         // HTTP method (GET, POST, etc.)
	Host        string         // Request host
	Path        string         // Request path including query parameters
	Raw         RawField       // Complete raw HTTP request
	Metadata    map[string]any // Additional metadata and extension data
	Prettified  string         // Prettified copy of Raw, or "" when the body could not be prettified
	RequestedAt time.Time      // Timestamp when request was made
	// SitemapNode names the pair's leaf node in the sitemap, from
	// SitemapLeaf. It is not stored: the repository computes the leaf from
	// Scheme, Host and Path.
	SitemapNode string
}

// ProxyResponse represents the data captured from an HTTP response.
type ProxyResponse struct {
	ID          uuid.UUID      // Unique identifier matching the associated request
	Status      string         // HTTP status text (e.g., "200 OK")
	StatusCode  int            // HTTP status code (e.g., 200, 404)
	ContentType string         // Response content type
	Length      string         // Content length
	Raw         RawField       // Complete raw HTTP response
	Metadata    map[string]any // Additional metadata and extension data
	Prettified  string         // Prettified copy of Raw, or "" when the body could not be prettified
	RespondedAt time.Time      // Timestamp when response was received
}

// RequestResponseRow represents a complete request-response pair with associated metadata,
// typically used when retrieving data from the database.
type RequestResponseRow struct {
	Request    ProxyRequest   // The HTTP request
	Response   ProxyResponse  // The corresponding HTTP response
	Metadata   map[string]any // Combined metadata from request and response
	Note       string         // Note contents
	Prettified Prettified     // Prettified copies of the request and response
}

// Prettified holds the prettified copies of a pair's request and response
// that the proxy stores at capture (ADR-0036). A copy is "" when the proxy
// could not prettify that body. Prettified copies are never part of metadata.
type Prettified struct {
	Request  string
	Response string
}

// RequestResponseSummary provides a summary of a request-response pair,
// excluding raw body and prettified data
type RequestResponseSummary struct {
	ID          uuid.UUID
	Scheme      string
	Method      string
	Host        string
	Path        string
	Status      string
	StatusCode  int
	ContentType string
	Length      string
	Metadata    map[string]any
	RequestedAt time.Time
	RespondedAt time.Time
	Note        string
}

// ContentTypeCount is the number of request-response pairs with one response
// content type. An empty ContentType means the pair has no content type.
type ContentTypeCount struct {
	ContentType string // Response content type, or "" when absent
	Count       int    // Number of pairs with this content type
}

// ContentTypeCounts is the count of pairs per content type across the
// project.
type ContentTypeCounts struct {
	Counts []*ContentTypeCount
	// Complete reports whether the sitemap job had finished, keyed by
	// JobSitemap. The counts are exact either way.
	Complete map[string]bool
}
