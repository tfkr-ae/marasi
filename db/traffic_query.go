package db

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tfkr-ae/marasi/domain"
	"go.einride.tech/aip/filtering"
	expr "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// maxQueryLength caps a query in bytes so a pathological query cannot make the
// recursive parser and translator run away.
const maxQueryLength = 8192

// trafficFieldKind is how a declared traffic field is compared.
type trafficFieldKind int

const (
	// fieldExactText fields compare with = and !=, keep exact case, and allow
	// a * wildcard at the start or end of the value.
	fieldExactText trafficFieldKind = iota
	// fieldInteger fields compare with = != < <= > >=.
	fieldInteger
	// fieldTimestamp fields compare with = != < <= > >= against RFC 3339 values.
	fieldTimestamp
	// fieldMetadata is the root of metadata.<key>, which compares the JSON
	// value at that key with = and !=.
	fieldMetadata
	// fieldIndexedText fields are text parts of a pair in the traffic index.
	// They match a term with : (contains), ignoring case.
	fieldIndexedText
)

// minTextTermLength is the shortest text term the trigram index can find.
// Shorter terms would silently match nothing, so they are rejected.
const minTextTermLength = 3

type trafficField struct {
	kind trafficFieldKind
	// column is the SQL expression for the field's value, or the index column
	// for fieldIndexedText.
	column string
}

// trafficFields declares the fields a traffic query can name.
var trafficFields = map[string]trafficField{
	"host":         {kind: fieldExactText, column: "host"},
	"method":       {kind: fieldExactText, column: "method"},
	"scheme":       {kind: fieldExactText, column: "scheme"},
	"path":         {kind: fieldExactText, column: "path"},
	"content_type": {kind: fieldExactText, column: "content_type"},
	// In-flight pairs store -1. Treat that as no status code.
	"status_code":  {kind: fieldInteger, column: "nullif(status_code, -1)"},
	"requested_at": {kind: fieldTimestamp, column: sqlEpochSeconds("requested_at")},
	"responded_at": {kind: fieldTimestamp, column: sqlEpochSeconds("responded_at")},
	"metadata":     {kind: fieldMetadata, column: "metadata"},

	"request_head":  {kind: fieldIndexedText, column: "request_head"},
	"request_body":  {kind: fieldIndexedText, column: "request_body"},
	"response_head": {kind: fieldIndexedText, column: "response_head"},
	"response_body": {kind: fieldIndexedText, column: "response_body"},
	"note":          {kind: fieldIndexedText, column: "note"},
}

var comparisonOperators = map[string]string{
	filtering.FunctionEquals:        "=",
	filtering.FunctionNotEquals:     "!=",
	filtering.FunctionLessThan:      "<",
	filtering.FunctionLessEquals:    "<=",
	filtering.FunctionGreaterThan:   ">",
	filtering.FunctionGreaterEquals: ">=",
}

// trafficQuery is a translated query. where is an SQL condition over the
// request table, empty when the query has no constraint.
type trafficQuery struct {
	where string
	args  []any
}

// translateTrafficQuery parses query as an AIP-160 filter, checks it against
// the declared traffic fields, and translates it to SQL. Invalid queries
// return a *domain.QueryError.
func translateTrafficQuery(query string) (trafficQuery, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return trafficQuery{}, nil
	}
	t := &queryTranslator{
		raw:  query,
		lead: len(query) - len(strings.TrimLeft(query, " \t\r\n\v\f")),
	}
	if len(query) > maxQueryLength {
		return trafficQuery{}, &domain.QueryError{
			Message:  fmt.Sprintf("query is longer than %d bytes", maxQueryLength),
			Position: 1,
		}
	}

	var parser filtering.Parser
	parser.Init(query)
	parsed, err := parser.Parse()
	if err != nil {
		return trafficQuery{}, t.parseError(err)
	}
	t.positions = parsed.GetSourceInfo().GetPositions()

	where, err := t.condition(parsed.GetExpr())
	if err != nil {
		return trafficQuery{}, err
	}
	return trafficQuery{where: where, args: t.args}, nil
}

type queryTranslator struct {
	raw       string
	lead      int
	positions map[int64]int32
	args      []any
}

// position converts a byte offset in the trimmed query to a 1-based character
// position in the raw query.
func (t *queryTranslator) position(offset int32) int {
	end := t.lead + int(offset)
	if end > len(t.raw) {
		end = len(t.raw)
	}
	return utf8.RuneCountInString(t.raw[:end]) + 1
}

func (t *queryTranslator) errorAt(e *expr.Expr, format string, args ...any) error {
	return &domain.QueryError{
		Message:  fmt.Sprintf(format, args...),
		Position: t.position(t.positions[e.GetId()]),
	}
}

// parseError turns a parser error into a QueryError at the deepest position
// the parser reported.
func (t *queryTranslator) parseError(err error) error {
	type positioned interface {
		Position() filtering.Position
		Message() string
	}
	var deepest positioned
	var cause error
	for e := err; e != nil; e = errors.Unwrap(e) {
		if p, ok := e.(positioned); ok {
			deepest = p
			cause = nil
			continue
		}
		cause = e
	}
	if deepest == nil {
		return &domain.QueryError{Message: err.Error(), Position: 1}
	}
	message := deepest.Message()
	switch {
	case errors.Is(cause, io.EOF):
		message = "unexpected end of query"
	case cause != nil:
		message = cause.Error()
	}
	return &domain.QueryError{Message: message, Position: t.position(deepest.Position().Offset)}
}

// condition translates a boolean expression.
func (t *queryTranslator) condition(e *expr.Expr) (string, error) {
	call := e.GetCallExpr()
	if call == nil {
		// Bare text searches every indexed text part.
		return t.textMatch(e, "")
	}
	args := call.GetArgs()
	switch call.GetFunction() {
	case filtering.FunctionAnd, filtering.FunctionFuzzyAnd:
		return t.junction("AND", args)
	case filtering.FunctionOr:
		return t.junction("OR", args)
	case filtering.FunctionNot:
		inner, err := t.condition(args[0])
		if err != nil {
			return "", err
		}
		// A NULL comparison counts as no match, so NOT of it is a match.
		return "NOT coalesce((" + inner + "), 0)", nil
	case filtering.FunctionHas:
		return t.has(e, args[0], args[1])
	}
	if op, ok := comparisonOperators[call.GetFunction()]; ok {
		return t.comparison(e, op, args[0], args[1])
	}
	return "", t.errorAt(e, "unknown function %q", call.GetFunction())
}

func (t *queryTranslator) junction(op string, args []*expr.Expr) (string, error) {
	parts := make([]string, len(args))
	for i, arg := range args {
		part, err := t.condition(arg)
		if err != nil {
			return "", err
		}
		parts[i] = "(" + part + ")"
	}
	return strings.Join(parts, " "+op+" "), nil
}

// has translates field:value, which searches one indexed text part.
func (t *queryTranslator) has(e, lhs, rhs *expr.Expr) (string, error) {
	name, path, err := t.fieldName(lhs)
	if err != nil {
		return "", err
	}
	field, ok := trafficFields[name]
	if !ok {
		return "", t.errorAt(lhs, "unknown field %q", strings.Join(append([]string{name}, path...), "."))
	}
	switch field.kind {
	case fieldIndexedText:
		if len(path) > 0 {
			return "", t.errorAt(lhs, "field %q has no member %q", name, path[0])
		}
		return t.textMatch(rhs, field.column)
	case fieldExactText:
		return "", t.errorAt(e, "operator : is not supported on %s; use = or !=, with * at the start or end", name)
	case fieldMetadata:
		if len(path) > 0 {
			return "", t.errorAt(e, "operator : is not supported on metadata keys; use = or !=")
		}
		// metadata:"text" searches the whole indexed metadata.
		return t.textMatch(rhs, "metadata")
	}
	return "", t.errorAt(e, "operator : is not supported on %s; use = or another comparison", name)
}

// textMatch matches pairs whose indexed text contains the term e, in one index
// column or, when column is empty, in any of them.
func (t *queryTranslator) textMatch(e *expr.Expr, column string) (string, error) {
	term, ok := t.textTerm(e)
	if !ok {
		return "", t.errorAt(e, "expected text to search for, for example \"token\"")
	}
	if utf8.RuneCountInString(term) < minTextTermLength {
		return "", t.errorAt(e, "text term %q is shorter than %d characters; search for a longer term", term, minTextTermLength)
	}
	// Quoting makes the term one literal phrase, so FTS5 syntax characters
	// such as - and : in it are plain text.
	match := `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	if column != "" {
		match = column + " : " + match
	}
	t.bind(match)
	return "request.rowid IN (SELECT rowid FROM traffic_fts WHERE traffic_fts MATCH ?)", nil
}

// textTerm returns the text of a quoted string, an unquoted word, or a number
// as written in the query.
func (t *queryTranslator) textTerm(e *expr.Expr) (string, bool) {
	if text, ok := literalText(e); ok {
		return text, true
	}
	switch e.GetConstExpr().GetConstantKind().(type) {
	case *expr.Constant_Int64Value, *expr.Constant_DoubleValue, *expr.Constant_Uint64Value:
		return t.sourceToken(e), true
	}
	return "", false
}

// sourceToken returns the query text of e from its position up to the next
// space or parenthesis.
func (t *queryTranslator) sourceToken(e *expr.Expr) string {
	start := t.lead + int(t.positions[e.GetId()])
	if start > len(t.raw) {
		return ""
	}
	token := t.raw[start:]
	if end := strings.IndexAny(token, " \t\r\n\v\f()"); end >= 0 {
		token = token[:end]
	}
	return token
}

func (t *queryTranslator) comparison(e *expr.Expr, op string, lhs, rhs *expr.Expr) (string, error) {
	name, path, err := t.fieldName(lhs)
	if err != nil {
		return "", err
	}
	field, ok := trafficFields[name]
	if !ok {
		return "", t.errorAt(lhs, "unknown field %q", strings.Join(append([]string{name}, path...), "."))
	}
	if field.kind != fieldMetadata && len(path) > 0 {
		return "", t.errorAt(lhs, "field %q has no member %q", name, path[0])
	}

	switch field.kind {
	case fieldIndexedText:
		return "", t.errorAt(e, "operator %s is not supported on %s; use :, for example %s:\"text\"", op, name, name)

	case fieldExactText:
		if op != "=" && op != "!=" {
			return "", t.errorAt(e, "operator %s is not supported on %s; use = or !=", op, name)
		}
		value, ok := literalText(rhs)
		if !ok {
			return "", t.errorAt(rhs, "%s needs a text value, for example %s = \"value\"", name, name)
		}
		return t.negate(op, t.matchExactText(field.column, value)), nil

	case fieldInteger:
		value := rhs.GetConstExpr()
		if value == nil {
			return "", t.errorAt(rhs, "%s needs a whole number", name)
		}
		if _, ok := value.GetConstantKind().(*expr.Constant_Int64Value); !ok {
			return "", t.errorAt(rhs, "%s needs a whole number", name)
		}
		return t.compare(field.column, op, value.GetInt64Value()), nil

	case fieldTimestamp:
		value, err := t.timestamp(rhs, name)
		if err != nil {
			return "", err
		}
		return t.compare(field.column, op, float64(value.UnixNano())/1e9), nil

	case fieldMetadata:
		if len(path) == 0 {
			return "", t.errorAt(lhs, "compare a metadata key, for example metadata.extension = \"value\"")
		}
		if op != "=" && op != "!=" {
			return "", t.errorAt(e, "operator %s is not supported on metadata keys; use = or !=", op)
		}
		jsonPath, err := t.metadataPath(lhs, path)
		if err != nil {
			return "", err
		}
		cond, err := t.metadataEquals(rhs, jsonPath)
		if err != nil {
			return "", err
		}
		return t.negate(op, cond), nil
	}
	return "", t.errorAt(lhs, "unknown field %q", name)
}

// fieldName returns the root field name and any .member path of a field
// reference.
func (t *queryTranslator) fieldName(e *expr.Expr) (string, []string, error) {
	var path []string
	for {
		switch kind := e.GetExprKind().(type) {
		case *expr.Expr_IdentExpr:
			return kind.IdentExpr.GetName(), path, nil
		case *expr.Expr_SelectExpr:
			path = append([]string{kind.SelectExpr.GetField()}, path...)
			e = kind.SelectExpr.GetOperand()
		default:
			return "", nil, t.errorAt(e, "expected a field name before the comparison")
		}
	}
}

// literalText returns the text of a quoted string or an unquoted word such as
// GET, /api/v1, or a.example.com.
func literalText(e *expr.Expr) (string, bool) {
	switch kind := e.GetExprKind().(type) {
	case *expr.Expr_ConstExpr:
		if s, ok := kind.ConstExpr.GetConstantKind().(*expr.Constant_StringValue); ok {
			return s.StringValue, true
		}
	case *expr.Expr_IdentExpr:
		return kind.IdentExpr.GetName(), true
	case *expr.Expr_SelectExpr:
		operand, ok := literalText(kind.SelectExpr.GetOperand())
		if !ok {
			return "", false
		}
		return operand + "." + kind.SelectExpr.GetField(), true
	}
	return "", false
}

func (t *queryTranslator) bind(values ...any) {
	t.args = append(t.args, values...)
}

func (t *queryTranslator) negate(op, cond string) string {
	if op == "!=" {
		return "NOT coalesce((" + cond + "), 0)"
	}
	return cond
}

// compare treats != as NOT =, so a pair without a value, such as an in-flight
// pair's status code, matches != like it matches NOT.
func (t *queryTranslator) compare(column, op string, value any) string {
	t.bind(value)
	if op == "!=" {
		return t.negate(op, column+" = ?")
	}
	return column + " " + op + " ?"
}

// matchExactText matches column against value with exact case. A * at the
// start or end of value matches any run of characters there. It never uses
// LIKE, which ignores case.
func (t *queryTranslator) matchExactText(column, value string) string {
	core := value
	suffix := strings.HasPrefix(core, "*")
	if suffix {
		core = core[1:]
	}
	prefix := strings.HasSuffix(core, "*")
	if prefix {
		core = core[:len(core)-1]
	}
	switch {
	case (suffix || prefix) && core == "":
		return column + " IS NOT NULL"
	case suffix && prefix:
		t.bind(core)
		return "instr(" + column + ", ?) > 0"
	case prefix:
		t.bind(core, core)
		return "substr(" + column + ", 1, length(?)) = ?"
	case suffix:
		t.bind(core, core)
		return "substr(" + column + ", -length(?)) = ?"
	default:
		t.bind(core)
		return column + " = ?"
	}
}

func (t *queryTranslator) timestamp(e *expr.Expr, name string) (time.Time, error) {
	value := e
	if call := e.GetCallExpr(); call != nil && call.GetFunction() == filtering.FunctionTimestamp && len(call.GetArgs()) == 1 {
		value = call.GetArgs()[0]
	}
	text := value.GetConstExpr().GetStringValue()
	if _, ok := value.GetConstExpr().GetConstantKind().(*expr.Constant_StringValue); !ok {
		return time.Time{}, t.errorAt(e, "%s needs an RFC 3339 timestamp, for example %s > \"2024-01-02T15:04:05Z\"", name, name)
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, t.errorAt(value, "%q is not an RFC 3339 timestamp, for example \"2024-01-02T15:04:05Z\"", text)
	}
	return parsed, nil
}

func (t *queryTranslator) metadataPath(lhs *expr.Expr, keys []string) (string, error) {
	var b strings.Builder
	b.WriteString("$")
	for _, key := range keys {
		if strings.ContainsAny(key, `"\`) {
			return "", t.errorAt(lhs, "metadata key %q cannot contain a quote or backslash", key)
		}
		b.WriteString(`."`)
		b.WriteString(key)
		b.WriteString(`"`)
	}
	return b.String(), nil
}

// metadataEquals compares the JSON value at path with a query value. A string
// matches only a JSON string, a number only a JSON number, and true, false,
// and null only the JSON literal.
func (t *queryTranslator) metadataEquals(rhs *expr.Expr, path string) (string, error) {
	switch kind := rhs.GetExprKind().(type) {
	case *expr.Expr_ConstExpr:
		switch value := kind.ConstExpr.GetConstantKind().(type) {
		case *expr.Constant_StringValue:
			t.bind(path, path, value.StringValue)
			return "(json_type(metadata, ?) = 'text' AND json_extract(metadata, ?) = ?)", nil
		case *expr.Constant_Int64Value:
			t.bind(path, path, value.Int64Value)
			return "(json_type(metadata, ?) IN ('integer', 'real') AND json_extract(metadata, ?) = ?)", nil
		case *expr.Constant_DoubleValue:
			t.bind(path, path, value.DoubleValue)
			return "(json_type(metadata, ?) IN ('integer', 'real') AND json_extract(metadata, ?) = ?)", nil
		}
	case *expr.Expr_IdentExpr:
		switch name := kind.IdentExpr.GetName(); name {
		case "true", "false", "null":
			t.bind(path)
			return "json_type(metadata, ?) = '" + name + "'", nil
		default:
			t.bind(path, path, name)
			return "(json_type(metadata, ?) = 'text' AND json_extract(metadata, ?) = ?)", nil
		}
	}
	return "", t.errorAt(rhs, "metadata keys compare with a string, number, true, false, or null")
}

// sqlEpochSeconds converts a timestamp column to Unix seconds. The SQLite
// driver stores time.Time as Go's time.String() text, for example
// "2024-01-02 03:04:05.123 +0400 +04", which SQLite date functions do not
// read. The expression reads the date, fraction, and offset parts itself.
// NULL columns stay NULL.
func sqlEpochSeconds(column string) string {
	x := column
	zone := "19+instr(substr(" + x + ",20),' ')"
	return "(unixepoch(substr(" + x + ",1,19))" +
		" + coalesce(CASE WHEN substr(" + x + ",20,1)='.' THEN CAST('0'||substr(" + x + ",20,instr(substr(" + x + ",20),' ')-1) AS REAL) END,0)" +
		" - (CASE substr(" + x + "," + zone + "+1,1) WHEN '-' THEN -1 ELSE 1 END)" +
		" * (CAST(substr(" + x + "," + zone + "+2,2) AS INTEGER)*3600" +
		" + CAST(substr(" + x + "," + zone + "+4,2) AS INTEGER)*60))"
}
