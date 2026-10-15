package db

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"go.einride.tech/aip/filtering"
	expr "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// maxQueryLength caps a query in bytes so a pathological query cannot make the
// recursive parser and translator run away.
const maxQueryLength = 8192

// maxQueryDepth caps how deeply a query's conditions nest. AND and OR chains
// nest one level per condition, as does each NOT, and SQLite rejects an
// expression nested more than 1000 levels, so a query within the length cap
// could still fail as a database error instead of an invalid query.
const maxQueryDepth = 200

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
	// fieldLink matches a pair's links to a launchpad, finding or test case:
	// = and != against the target's id, and :* for a link to any target.
	fieldLink
	// fieldSitemap matches a sitemap node's subtree with =, and its
	// complement with !=, by node name (ADR-0040).
	fieldSitemap
	// fieldHighlight matches a pair's highlight (ADR-0035): = and != against
	// a number from 1 to 8, and :* for any highlight.
	fieldHighlight
)

// member is true for a pair linked to the target bound to its ?. It is an
// uncorrelated IN rather than a correlated EXISTS, so a sparse link can
// drive the plan instead of being checked on every row.
func (l linkTable) member() string {
	return "request.id IN (SELECT request_id FROM " + l.table + " WHERE " + l.column + " = ?)"
}

// anyMember is true for a pair linked to at least one target.
func (l linkTable) anyMember() string {
	return "request.id IN (SELECT request_id FROM " + l.table + ")"
}

var (
	launchpadLinks = linkTables[domain.LinkLaunchpad]
	findingLinks   = linkTables[domain.LinkFinding]
	testCaseLinks  = linkTables[domain.LinkTestCase]
)

// launchpadMember is true for a pair linked to the launchpad bound to its ?.
// Every launchpad-scoped read uses it, so the launchpad query field and a
// launchpad's entries agree on what membership means.
var launchpadMember = launchpadLinks.member()

// noteExists is true for a pair that has a note.
const noteExists = "request.id IN (SELECT request_id FROM notes)"

// sitemapSubtree is true for a pair linked to the node named by the first ?
// or to a node below it. Names below a node extend its name with /, so the
// subtree is the name itself and the range [name/, name0), which the unique
// index on name serves.
const sitemapSubtree = "request.id IN (SELECT ps.request_id FROM pair_sitemap ps JOIN sitemap_node n ON n.id = ps.node_id WHERE n.name = ? OR (n.name >= ? AND n.name < ?))"

// highlightIs is true for a pair whose highlight is the number bound to its
// ?. It is a set of ids rather than a correlated EXISTS, so the
// (highlight, request_id) index can drive a sparse highlight's plan.
const highlightIs = "request.id IN (SELECT request_id FROM pair_highlight WHERE highlight = ?)"

// anyHighlight is true for a pair with a highlight.
const anyHighlight = "request.id IN (SELECT request_id FROM pair_highlight)"

// minTextTermLength is the shortest text term the trigram index can find.
// Shorter terms would silently match nothing, so they are rejected.
const minTextTermLength = 3

type trafficField struct {
	kind trafficFieldKind
	// column is the SQL expression for the field's value, or the index column
	// for fieldIndexedText.
	column string
	// links is the link table of a fieldLink.
	links linkTable
	// indexed fields have an index on (column, id), so an equality or range
	// on them can drive a read.
	indexed bool
}

// trafficFields declares the fields a traffic query can name.
var trafficFields = map[string]trafficField{
	"host":         {kind: fieldExactText, column: "host", indexed: true},
	"method":       {kind: fieldExactText, column: "method"},
	"scheme":       {kind: fieldExactText, column: "scheme"},
	"path":         {kind: fieldExactText, column: "path"},
	"content_type": {kind: fieldExactText, column: "content_type", indexed: true},
	// In-flight pairs store -1. Treat that as no status code.
	"status_code": {kind: fieldInteger, column: "nullif(status_code, -1)"},
	// The size sort's expression, so the length index serves it. A pair
	// with no response has a NULL length.
	"length":       {kind: fieldInteger, column: lengthKey},
	"requested_at": {kind: fieldTimestamp, column: sqlEpochSeconds("requested_at")},
	"responded_at": {kind: fieldTimestamp, column: sqlEpochSeconds("responded_at")},
	"metadata":     {kind: fieldMetadata, column: "metadata"},
	// Membership is a row in launchpad_request, not the launchpad_id
	// metadata, which pairs linked from captured traffic do not have.
	// finding and test_case read their link tables the same way.
	"launchpad": {kind: fieldLink, links: launchpadLinks},
	"finding":   {kind: fieldLink, links: findingLinks},
	"test_case": {kind: fieldLink, links: testCaseLinks},
	"sitemap":   {kind: fieldSitemap, column: "pair_sitemap"},
	"highlight": {kind: fieldHighlight, column: "pair_highlight"},

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
	// sitemap means the query names the sitemap field, so its answer
	// depends on the sitemap job.
	sitemap bool
	// text means the query has a text term, so its answer depends on the
	// search index jobs.
	text bool
}

// translateTrafficQuery parses query as an AIP-160 filter, checks it against
// the declared traffic fields, and translates it to SQL. Invalid queries
// return a *domain.QueryError.
//
// after, when set, promises that the caller reads only pairs with an id
// above it. Text terms then search only the traffic index rows that such
// pairs can have, so a common term does not gather every older match first.
func translateTrafficQuery(query string, after *uuid.UUID) (trafficQuery, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return trafficQuery{}, nil
	}
	t := &queryTranslator{
		raw:   query,
		lead:  len(query) - len(strings.TrimLeft(query, " \t\r\n\v\f")),
		after: after,
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

	t.topLevel = true
	where, err := t.condition(parsed.GetExpr())
	if err != nil {
		return trafficQuery{}, err
	}
	return trafficQuery{where: where, args: t.args, sitemap: t.sitemap, text: t.text}, nil
}

type queryTranslator struct {
	raw       string
	lead      int
	positions map[int64]int32
	args      []any
	depth     int
	sitemap   bool
	text      bool
	// topLevel is set while translating the root, or a condition the root
	// ANDs with others.
	topLevel bool
	// after is the id every pair the caller reads is above, if any.
	after *uuid.UUID
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
	offset := deepest.Position().Offset
	switch {
	case errors.Is(cause, io.EOF):
		message = "unexpected end of query"
	case cause != nil:
		message = cause.Error()
	case strings.HasPrefix(message, "unexpected "):
		// The library names the token type, such as WS, and can point at
		// the space before the token at fault. Name the token itself.
		message, offset = t.unexpectedToken(offset)
	case strings.HasPrefix(message, "expected "):
		message = "expected " + describeTokenType(strings.TrimPrefix(message, "expected "))
	}
	return &domain.QueryError{Message: message, Position: t.position(offset)}
}

// unexpectedToken describes the first token at or after offset in the trimmed
// query, skipping spaces, and returns its offset.
func (t *queryTranslator) unexpectedToken(offset int32) (string, int32) {
	trimmed := strings.TrimSpace(t.raw)
	var lexer filtering.Lexer
	lexer.Init(trimmed[offset:])
	next := func() (filtering.Token, bool) {
		for {
			token, err := lexer.Lex()
			if err != nil {
				return filtering.Token{}, false
			}
			if token.Type != filtering.TokenTypeWhitespace {
				return token, true
			}
		}
	}
	token, ok := next()
	if !ok {
		return "unexpected end of query", int32(len(trimmed))
	}
	offset += token.Position.Offset
	switch token.Type {
	case filtering.TokenTypeAnd, filtering.TokenTypeOr, filtering.TokenTypeNot:
		if _, more := next(); !more {
			return fmt.Sprintf("expected a condition after %s", token.Value), offset
		}
	case filtering.TokenTypeNumber, filtering.TokenTypeHexNumber:
		return fmt.Sprintf("%s is not a valid number", token.Value), offset
	}
	return fmt.Sprintf("unexpected %q", token.Value), offset
}

// describeTokenType names a token type the library expected in words, or
// returns what unchanged when it is not a token type.
func describeTokenType(what string) string {
	switch filtering.TokenType(what) {
	case filtering.TokenTypeWhitespace:
		return "a space"
	case filtering.TokenTypeText:
		return "a word"
	case filtering.TokenTypeString:
		return "a quoted string"
	case filtering.TokenTypeNumber, filtering.TokenTypeHexNumber:
		return "a number"
	}
	if strings.ContainsAny(what, " ") {
		return what
	}
	return fmt.Sprintf("%q", what)
}

// condition translates a boolean expression.
func (t *queryTranslator) condition(e *expr.Expr) (string, error) {
	t.depth++
	defer func() { t.depth-- }()
	if t.depth > maxQueryDepth {
		return "", t.errorAt(e, "query nests more than %d conditions; use fewer conditions or less nesting", maxQueryDepth)
	}
	// Only the root and the conditions it ANDs together are top level.
	topLevel := t.topLevel
	t.topLevel = false
	call := e.GetCallExpr()
	if call == nil {
		// Bare text searches every indexed text part.
		return t.textMatch(e, "")
	}
	args := call.GetArgs()
	switch call.GetFunction() {
	case filtering.FunctionAnd, filtering.FunctionFuzzyAnd:
		return t.junction("AND", args, topLevel)
	case filtering.FunctionOr:
		if branches := orBranches(e); topLevel && t.allIndexDriven(branches) {
			return t.union(branches)
		}
		return t.junction("OR", args, false)
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

// junction joins args with op. The args of a top-level AND are top level
// too.
func (t *queryTranslator) junction(op string, args []*expr.Expr, topLevel bool) (string, error) {
	parts := make([]string, len(args))
	for i, arg := range args {
		t.topLevel = topLevel
		part, err := t.condition(arg)
		if err != nil {
			return "", err
		}
		parts[i] = "(" + part + ")"
	}
	return strings.Join(parts, " "+op+" "), nil
}

// union translates a top-level OR whose branches can each be read through
// an index as a UNION of the branches, so each branch drives its own read
// instead of every pair being checked against the whole OR.
func (t *queryTranslator) union(branches []*expr.Expr) (string, error) {
	parts := make([]string, len(branches))
	for i, branch := range branches {
		part, err := t.condition(branch)
		if err != nil {
			return "", err
		}
		parts[i] = "SELECT request.id FROM request WHERE " + part
	}
	return "request.id IN (" + strings.Join(parts, " UNION ") + ")", nil
}

// orBranches returns the branches of an OR, with nested ORs flattened.
func orBranches(e *expr.Expr) []*expr.Expr {
	call := e.GetCallExpr()
	if call.GetFunction() != filtering.FunctionOr {
		return []*expr.Expr{e}
	}
	var branches []*expr.Expr
	for _, arg := range call.GetArgs() {
		branches = append(branches, orBranches(arg)...)
	}
	return branches
}

func (t *queryTranslator) allIndexDriven(branches []*expr.Expr) bool {
	for _, branch := range branches {
		if !t.indexDriven(branch) {
			return false
		}
	}
	return true
}

// indexDriven reports whether an index can find the pairs that match e
// without checking every pair: a text term, a link, note or highlight, a requested_at
// lower bound, an equality on an indexed field, or an AND with one of these.
// An invalid e reports false; translating it reports the error.
func (t *queryTranslator) indexDriven(e *expr.Expr) bool {
	call := e.GetCallExpr()
	if call == nil {
		return true // bare text
	}
	args := call.GetArgs()
	switch function := call.GetFunction(); function {
	case filtering.FunctionAnd, filtering.FunctionFuzzyAnd:
		return slices.ContainsFunc(args, t.indexDriven)
	case filtering.FunctionOr:
		return t.allIndexDriven(orBranches(e))
	case filtering.FunctionHas:
		name, _, err := t.fieldName(args[0])
		if err != nil {
			return false
		}
		kind := trafficFields[name].kind
		return kind == fieldIndexedText || kind == fieldMetadata || kind == fieldLink || kind == fieldHighlight
	default:
		op, ok := comparisonOperators[function]
		if !ok || len(args) != 2 {
			return false
		}
		name, _, err := t.fieldName(args[0])
		if err != nil {
			return false
		}
		field := trafficFields[name]
		switch {
		case field.kind == fieldLink, field.kind == fieldSitemap, field.kind == fieldHighlight:
			return op == "="
		case name == "requested_at":
			return op == "=" || op == ">" || op == ">="
		case !field.indexed:
			return false
		case field.kind == fieldExactText:
			value, ok := literalText(args[1])
			return ok && op == "=" && !strings.Contains(value, "*")
		default:
			return op != "!="
		}
	}
}

// has translates field:value, which searches one indexed text part.
func (t *queryTranslator) has(e, lhs, rhs *expr.Expr) (string, error) {
	name, field, path, err := t.field(lhs)
	if err != nil {
		return "", err
	}
	switch field.kind {
	case fieldIndexedText:
		if len(path) > 0 {
			return "", t.errorAt(lhs, "field %q has no member %q", name, path[0])
		}
		// An unquoted note:* means the pair has a note. Any other value,
		// including a quoted "*", stays a text search.
		if name == "note" && t.sourceToken(rhs) == "*" {
			return noteExists, nil
		}
		return t.textMatch(rhs, field.column)
	case fieldExactText:
		return "", t.errorAt(e, "operator : is not supported on %s; use = or !=, with * at the start or end", name)
	case fieldMetadata:
		if len(path) > 0 {
			// The parser reads metadata.<key>:* as the string "*".
			if rhs.GetConstExpr().GetStringValue() != "*" {
				return "", t.errorAt(e, "operator : on a metadata key takes only *, as in metadata.%s:*; use = or != to compare its value", strings.Join(path, "."))
			}
			jsonPath, err := t.metadataPath(lhs, path)
			if err != nil {
				return "", err
			}
			// json_type is NULL only when the key is absent; a JSON null
			// value has the type 'null'.
			t.bind(jsonPath)
			return "json_type(" + field.column + ", ?) IS NOT NULL", nil
		}
		// metadata:"text" searches the whole indexed metadata.
		return t.textMatch(rhs, field.column)
	case fieldLink:
		if len(path) > 0 {
			return "", t.errorAt(lhs, "field %q has no member %q", name, path[0])
		}
		// The parser reads launchpad:* as the string "*".
		if rhs.GetConstExpr().GetStringValue() != "*" {
			noun := field.links.noun
			return "", t.errorAt(e, "operator : on %s takes only *, as in %s:*; use %s = \"<%s id>\" for one %s", name, name, name, noun, noun)
		}
		return field.links.anyMember(), nil
	case fieldHighlight:
		if len(path) > 0 {
			return "", t.errorAt(lhs, "field %q has no member %q", name, path[0])
		}
		if rhs.GetConstExpr().GetStringValue() != "*" {
			return "", t.errorAt(e, "operator : on highlight takes only *, as in highlight:*; use highlight = 3 for one highlight")
		}
		return anyHighlight, nil
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
	phrase := `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	var search string
	switch column {
	case "":
		// One IN over the union, not an OR of two INs, so SQLite builds a
		// single match set and still drives the page from it.
		search = t.indexSearch("traffic_index", bodyColumns+" : "+phrase) +
			" UNION " + t.indexSearch("traffic_annotation_index", phrase)
	case "note", "metadata":
		search = t.indexSearch("traffic_annotation_index", column+" : "+phrase)
	default:
		search = t.indexSearch("traffic_index", column+" : "+phrase)
	}
	return "request.id IN (" + search + ")", nil
}

// bodyColumns is the column filter on every body index match. Body index rows
// written before the annotation index still hold note and metadata text, which
// must never match (ADR-0038).
const bodyColumns = "{request_head request_body response_head response_body}"

// indexSearch selects the ids of pairs whose row in the FTS5 table, the body
// index traffic_index or the annotation index traffic_annotation_index,
// matches the FTS5 expression match. Both are keyed on traffic_index_key.
func (t *queryTranslator) indexSearch(table, match string) string {
	t.text = true
	t.bind(match)
	search := "SELECT k.request_id FROM " + table + " JOIN traffic_index_key k ON k.id = " + table + ".rowid WHERE " + table + " MATCH ?"
	if t.after == nil {
		return search
	}
	// Every pair above after has an index key at or above the smallest key
	// of those pairs, so the search can start there. When none of them is
	// indexed yet, the bound is NULL and the term matches none of them,
	// as it would without the bound.
	t.bind(*t.after)
	return search + " AND " + table + ".rowid >= (SELECT min(id) FROM traffic_index_key WHERE request_id > ?)"
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
	name, field, path, err := t.field(lhs)
	if err != nil {
		return "", err
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
		return negate(op, t.matchExactText(field.column, value)), nil

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
		cond := t.compare(field.column, op, float64(value.UnixNano())/1e9)
		if name == "requested_at" && (op == ">" || op == ">=" || op == "=") {
			// The condition implies the id bound, so adding it changes no
			// result, but lets the id index drive the read.
			t.bind(idFloor(value.Add(-requestedAtIDSlack)))
			cond = "(" + cond + ") AND id >= ?"
		}
		return cond, nil

	case fieldLink:
		if op != "=" && op != "!=" {
			return "", t.errorAt(e, "operator %s is not supported on %s; use =, !=, or %s:*", op, name, name)
		}
		id, err := t.linkTargetID(rhs, name, field.links.noun)
		if err != nil {
			return "", err
		}
		t.bind(id)
		return negate(op, field.links.member()), nil

	case fieldSitemap:
		if op != "=" && op != "!=" {
			return "", t.errorAt(e, "operator %s is not supported on sitemap; use = or !=", op)
		}
		node, ok := rhs.GetConstExpr().GetConstantKind().(*expr.Constant_StringValue)
		if !ok {
			return "", t.errorAt(rhs, "sitemap needs a quoted node name, for example sitemap = \"https://api.example.com/users\"")
		}
		t.sitemap = true
		t.bind(node.StringValue, node.StringValue+"/", node.StringValue+"0")
		return negate(op, sitemapSubtree), nil

	case fieldHighlight:
		if op != "=" && op != "!=" {
			return "", t.errorAt(e, "operator %s is not supported on highlight; use =, !=, or highlight:*", op)
		}
		value, ok := rhs.GetConstExpr().GetConstantKind().(*expr.Constant_Int64Value)
		if !ok || value.Int64Value < domain.MinHighlight || value.Int64Value > domain.MaxHighlight {
			return "", t.errorAt(rhs, "highlight needs a whole number from %d to %d", domain.MinHighlight, domain.MaxHighlight)
		}
		t.bind(value.Int64Value)
		return negate(op, highlightIs), nil

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
		return negate(op, cond), nil
	}
	return "", t.errorAt(lhs, "unknown field %q", name)
}

// field returns the declared traffic field a field reference names, with its
// root name and any .member path.
func (t *queryTranslator) field(e *expr.Expr) (string, trafficField, []string, error) {
	name, path, err := t.fieldName(e)
	if err != nil {
		return "", trafficField{}, nil, err
	}
	field, ok := trafficFields[name]
	if !ok {
		return "", trafficField{}, nil, t.errorAt(e, "unknown field %q", strings.Join(append([]string{name}, path...), "."))
	}
	return name, field, path, nil
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

// negate wraps cond in NOT for !=. A NULL comparison counts as no match, so
// NOT of it is a match.
func negate(op, cond string) string {
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
		return negate(op, column+" = ?")
	}
	return column + " " + op + " ?"
}

// matchExactText matches column against value with exact case. A * at the
// start or end of value matches any run of characters there. It never uses
// LIKE, which ignores case.
func (t *queryTranslator) matchExactText(column, value string) string {
	core := value
	leadingWildcard := strings.HasPrefix(core, "*")
	if leadingWildcard {
		core = core[1:]
	}
	trailingWildcard := strings.HasSuffix(core, "*")
	if trailingWildcard {
		core = core[:len(core)-1]
	}
	switch {
	case (leadingWildcard || trailingWildcard) && core == "":
		return column + " IS NOT NULL"
	case leadingWildcard && trailingWildcard:
		t.bind(core)
		return "instr(" + column + ", ?) > 0"
	case trailingWildcard:
		t.bind(core, core)
		return "substr(" + column + ", 1, length(?)) = ?"
	case leadingWildcard:
		t.bind(core, core)
		return "substr(" + column + ", -length(?)) = ?"
	default:
		t.bind(core)
		return column + " = ?"
	}
}

// linkTargetID returns the id of the launchpad, finding or test case (noun)
// that a quoted string names for the field name.
func (t *queryTranslator) linkTargetID(e *expr.Expr, name, noun string) (uuid.UUID, error) {
	value, ok := e.GetConstExpr().GetConstantKind().(*expr.Constant_StringValue)
	if !ok {
		return uuid.Nil, t.errorAt(e, "%s needs a quoted %s id, for example %s = \"01937f5d-351a-7e68-936d-61a7a25661a3\"", name, noun, name)
	}
	id, err := uuid.Parse(value.StringValue)
	if err != nil {
		return uuid.Nil, t.errorAt(e, "%q is not a %s id; use a %s UUID", value.StringValue, noun, noun)
	}
	return id, nil
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
	column := trafficFields["metadata"].column
	var jsonTypes string
	var value any
	switch kind := rhs.GetExprKind().(type) {
	case *expr.Expr_ConstExpr:
		switch constant := kind.ConstExpr.GetConstantKind().(type) {
		case *expr.Constant_StringValue:
			jsonTypes, value = "('text')", constant.StringValue
		case *expr.Constant_Int64Value:
			jsonTypes, value = "('integer', 'real')", constant.Int64Value
		case *expr.Constant_DoubleValue:
			jsonTypes, value = "('integer', 'real')", constant.DoubleValue
		}
	case *expr.Expr_IdentExpr:
		switch name := kind.IdentExpr.GetName(); name {
		case "true", "false", "null":
			t.bind(path)
			return "json_type(" + column + ", ?) = '" + name + "'", nil
		default:
			// An unquoted word is a string.
			jsonTypes, value = "('text')", name
		}
	}
	if value == nil {
		return "", t.errorAt(rhs, "metadata keys compare with a string, number, true, false, or null")
	}
	t.bind(path, path, value)
	return "(json_type(" + column + ", ?) IN " + jsonTypes + " AND json_extract(" + column + ", ?) = ?)", nil
}

// requestedAtIDSlack is how much older than its requested_at a pair's id may
// be. A pair's UUIDv7 id is minted when the proxy receives the request,
// just after requested_at is read, except for a launch: its id is minted
// when the launch is registered, shortly before the request reaches the
// proxy. A lower bound on requested_at therefore implies a lower bound on id
// this much earlier.
//
// Only lower bounds are derived. An id can be much newer than requested_at,
// for example after the clock steps back (UUIDv7 ids stay monotonic) or for
// pairs written with an older requested_at, so an upper bound on
// requested_at implies no upper bound on id.
const requestedAtIDSlack = time.Hour

// idFloor returns the smallest pair id, in the id column's text form, that a
// UUIDv7 minted at or after at can have.
func idFloor(at time.Time) string {
	var id uuid.UUID
	ms := max(at.UnixMilli(), 0)
	for i := range 6 {
		id[i] = byte(ms >> (8 * (5 - i)))
	}
	return id.String()
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
