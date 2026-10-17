package domain

import "fmt"

// QueryError reports an invalid query. Position is the 1-based character
// offset of the problem in the query string, or 0 when the problem is not in
// the query string, such as an invalid content-type exclusion.
type QueryError struct {
	Message  string
	Position int
}

func (e *QueryError) Error() string {
	if e.Position == 0 {
		return "invalid query: " + e.Message
	}
	return fmt.Sprintf("invalid query at position %d: %s", e.Position, e.Message)
}
