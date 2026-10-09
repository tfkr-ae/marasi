package domain

import "fmt"

// QueryError reports an invalid query. Position is the 1-based character
// offset of the problem in the query string.
type QueryError struct {
	Message  string
	Position int
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("invalid query at position %d: %s", e.Position, e.Message)
}
