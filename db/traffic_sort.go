package db

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tfkr-ae/marasi/domain"
)

// lengthKey is a pair's response length as a number. The size sort, its
// index, the length query field and cursors all use this exact expression,
// so SQLite matches the expression index. A pair with no response has a NULL
// length, and so a NULL key.
const lengthKey = "CAST(nullif(length, '') AS INTEGER)"

// sortKey is a ledger column a traffic list can sort by (ADR-0041).
type sortKey struct {
	// expr is the SQL expression of the key. Empty means the key is id.
	expr string
	// integer means the key compares as a number; otherwise it is text in
	// byte order.
	integer bool
	// expression means expr is served by an expression index rather than
	// a column index.
	expression bool
	// hasValue and noValue split pairs into those with a sort value and
	// those with none, which sort last in both directions. They are empty
	// when every pair has a value.
	hasValue, noValue string
}

// sortKeys are the fields order_by accepts.
var sortKeys = map[string]sortKey{
	"requested_at": {},
	"method":       {expr: "method"},
	"host":         {expr: "host"},
	"path":         {expr: "path"},
	// A pair with no response stores -1.
	"status_code": {expr: "status_code", integer: true,
		hasValue: "status_code > -1", noValue: "(status_code IS NULL OR status_code <= -1)"},
	"content_type": {expr: "content_type",
		hasValue: "content_type > ''", noValue: "(content_type IS NULL OR content_type = '')"},
	"length": {expr: lengthKey, integer: true, expression: true,
		hasValue: lengthKey + " IS NOT NULL", noValue: lengthKey + " IS NULL"},
}

// trafficSort is a parsed order_by.
type trafficSort struct {
	// name is the canonical order_by, such as "host" or "length desc".
	name string
	key  sortKey
	desc bool
}

// phased reports whether the sort walks pairs with no value as a second
// phase.
func (s trafficSort) phased() bool { return s.key.noValue != "" }

// column is the key's SQL expression, id for requested_at.
func (s trafficSort) column() string {
	if s.key.expr == "" {
		return "id"
	}
	return s.key.expr
}

// order is the sort's whole display order as one ORDER BY, for reads that
// do not page: pairs with a value by (key, id), then pairs with none by id,
// both in the sort's direction.
func (s trafficSort) order() string {
	dir := "ASC"
	if s.desc {
		dir = "DESC"
	}
	if s.key.expr == "" {
		return "id " + dir
	}
	if !s.phased() {
		return s.key.expr + " " + dir + ", id " + dir
	}
	return "coalesce(" + s.key.noValue + ", 1) ASC, CASE WHEN " + s.key.hasValue + " THEN " + s.key.expr +
		" END " + dir + ", id " + dir
}

// parseTrafficOrderBy parses an AIP-132 order_by: one field and an optional
// desc. Empty is requested_at desc. An invalid sort returns a
// *domain.QueryError at the position of the part at fault.
func parseTrafficOrderBy(orderBy string) (trafficSort, error) {
	if comma := strings.IndexByte(orderBy, ','); comma >= 0 {
		return trafficSort{}, sortError(orderBy, comma, "order_by takes one field")
	}
	type word struct {
		text   string
		offset int
	}
	var words []word
	for offset := 0; offset < len(orderBy); {
		r, size := utf8.DecodeRuneInString(orderBy[offset:])
		if unicode.IsSpace(r) {
			offset += size
			continue
		}
		end := strings.IndexFunc(orderBy[offset:], unicode.IsSpace)
		if end < 0 {
			end = len(orderBy) - offset
		}
		words = append(words, word{orderBy[offset : offset+end], offset})
		offset += end
	}
	if len(words) == 0 {
		return trafficSort{name: "requested_at desc", key: sortKeys["requested_at"], desc: true}, nil
	}
	field := words[0]
	key, ok := sortKeys[field.text]
	if !ok {
		return trafficSort{}, sortError(orderBy, field.offset,
			fmt.Sprintf("cannot sort by %q; sort by requested_at, method, host, path, status_code, content_type or length", field.text))
	}
	sort := trafficSort{name: field.text, key: key}
	if len(words) > 1 {
		if words[1].text != "desc" {
			return trafficSort{}, sortError(orderBy, words[1].offset,
				fmt.Sprintf("unexpected %q; a sort takes only desc after the field", words[1].text))
		}
		sort.name += " desc"
		sort.desc = true
	}
	if len(words) > 2 {
		return trafficSort{}, sortError(orderBy, words[2].offset, "order_by takes one field")
	}
	return sort, nil
}

// sortError is an invalid sort at a byte offset of orderBy, reported as a
// 1-based character position.
func sortError(orderBy string, offset int, message string) error {
	return &domain.QueryError{Message: "invalid order_by: " + message, Position: utf8.RuneCountInString(orderBy[:offset]) + 1}
}
