package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"

	"github.com/google/uuid"
)

// MaxSelectionIDs is the largest number of pair ids one id selection holds.
const MaxSelectionIDs = 100_000

// ErrInvalidSelection reports a selection body that is neither an id
// selection nor a query selection.
var ErrInvalidSelection = errors.New("invalid selection")

// ErrSelectionIncomplete reports a query selection that depends on a
// background job that has not finished, so it could select pairs the
// finished job would not (ADR-0033). A bulk operation refuses it and changes
// nothing; the client retries once the job completes.
var ErrSelectionIncomplete = errors.New("the selection depends on an unfinished background job")

// SelectionIncompleteError is the ErrSelectionIncomplete a bulk operation
// returns. Jobs are the unfinished jobs the selection's query depends on.
type SelectionIncompleteError struct {
	Jobs []JobName
}

func (e *SelectionIncompleteError) Error() string {
	names := make([]string, len(e.Jobs))
	for i, job := range e.Jobs {
		names[i] = string(job)
	}
	return fmt.Sprintf("%v: %s", ErrSelectionIncomplete, strings.Join(names, ", "))
}

func (e *SelectionIncompleteError) Unwrap() error { return ErrSelectionIncomplete }

// Selection is the set of request/response pairs that one bulk operation
// applies to (ADR-0033). Exactly one of IDs and Query is set.
type Selection struct {
	IDs   []uuid.UUID     // listed pair ids, at most MaxSelectionIDs
	Query *SelectionQuery // every pair matching a query, up to AsOf
}

// SelectionQuery selects every pair that matches Text and is not hidden by
// Exclusion, with an id at or below AsOf.
type SelectionQuery struct {
	Text      string    // AIP-160 traffic query; empty matches every pair
	Exclusion []string  // normalised content-type exclusion
	AsOf      uuid.UUID // newest pair id the client had seen; required
}

// UnmarshalJSON decodes {"ids": [...]} or
// {"query": "...", "exclusion": [...], "as_of": "<uuid>"}. Any other shape,
// an empty or oversized id list, a missing as_of, or an invalid exclusion
// returns an error wrapping ErrInvalidSelection.
func (s *Selection) UnmarshalJSON(data []byte) error {
	var body struct {
		IDs       *[]uuid.UUID `json:"ids"`
		Query     *string      `json:"query"`
		Exclusion []string     `json:"exclusion"`
		AsOf      *uuid.UUID   `json:"as_of"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("%w: expected an object", ErrInvalidSelection)
	}
	if err := decoder.Decode(&body); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSelection, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: unexpected data after the selection", ErrInvalidSelection)
	}

	switch {
	case body.IDs != nil && body.Query == nil && body.Exclusion == nil && body.AsOf == nil:
		ids := *body.IDs
		if len(ids) == 0 {
			return fmt.Errorf("%w: ids is empty", ErrInvalidSelection)
		}
		if len(ids) > MaxSelectionIDs {
			return fmt.Errorf("%w: more than %d ids", ErrInvalidSelection, MaxSelectionIDs)
		}
		*s = Selection{IDs: ids}
		return nil
	case body.IDs == nil && body.Query != nil:
		if body.AsOf == nil {
			return fmt.Errorf("%w: a query selection needs as_of", ErrInvalidSelection)
		}
		exclusion, err := NormalizeExclusion(body.Exclusion)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSelection, err)
		}
		*s = Selection{Query: &SelectionQuery{Text: *body.Query, Exclusion: exclusion, AsOf: *body.AsOf}}
		return nil
	default:
		return fmt.Errorf(`%w: expected either "ids" or "query" with "as_of"`, ErrInvalidSelection)
	}
}

// NormalizeExclusion parses each content-type exclusion value with
// mime.ParseMediaType, lowercases it and drops its parameters. A value must
// be an exact type such as "text/html" or a whole major type such as
// "image/*". Any other value, including one with another "*", returns a
// *QueryError naming it. A nil or empty exclusion returns nil.
func NormalizeExclusion(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]string, len(values))
	for i, value := range values {
		mediaType, _, err := mime.ParseMediaType(value)
		major, minor, ok := strings.Cut(mediaType, "/")
		switch {
		case err != nil || !ok || major == "" || minor == "":
			return nil, &QueryError{Message: fmt.Sprintf("invalid content-type exclusion %q: want type/subtype or type/*", value)}
		case strings.Contains(major, "*") || (strings.Contains(minor, "*") && minor != "*"):
			return nil, &QueryError{Message: fmt.Sprintf("invalid content-type exclusion %q: * may only stand for a whole subtype, as in image/*", value)}
		}
		normalized[i] = mediaType
	}
	return normalized, nil
}
