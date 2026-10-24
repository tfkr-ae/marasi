package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// trafficSummaryColumns are the columns a traffic list reads into a
// dbRequestResponseSummary, from trafficSummarySource.
const trafficSummaryColumns = `id, scheme, method, host, path, requested_at,
	status, status_code, content_type, length, responded_at,
	upstream_proxy_name, upstream_proxy_address,
	metadata,
	pair_highlight.highlight, pair_number.number`

// trafficSummarySource is the FROM clause trafficSummaryColumns read.
const trafficSummarySource = ` FROM request` + highlightJoin + numberJoin

// trafficCursorVersion is the version of the cursor token format.
const trafficCursorVersion = 1

// trafficCursor is the decoded form of an opaque list cursor (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md). It
// names the sort it was made under, the row's sort key and id, and the phase
// of the walk: 0 while walking pairs with a sort value, 1 once the walk has
// reached the pairs with none. Key is null for requested_at, whose key is id,
// and in phase 1. A text key is its bytes, so any stored value survives JSON.
type trafficCursor struct {
	Version int             `json:"v"`
	Sort    string          `json:"s"`
	Key     json.RawMessage `json:"k,omitempty"`
	ID      uuid.UUID       `json:"id"`
	Phase   int             `json:"p"`
	// Responses is the highest pair_response_order seq when the walk's
	// first page was read. Pairs whose response was written later are
	// walked with the pairs that have no value. Nil in a cursor of a sort
	// without that phase, or made before ticket 28.
	Responses *int64 `json:"r,omitempty"`
}

// encode returns the cursor as an opaque token: unpadded URL-safe base64 of
// its JSON.
func (c trafficCursor) encode() string {
	raw, _ := json.Marshal(c) // a struct of plain fields cannot fail to marshal
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeTrafficCursor parses token, checks that it was made under sort, and
// returns it with its key as a value to bind. Any failure wraps
// domain.ErrInvalidCursor.
func decodeTrafficCursor(token string, sort trafficSort) (trafficCursor, any, error) {
	invalid := fmt.Errorf("%w: not a cursor token", domain.ErrInvalidCursor)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return trafficCursor{}, nil, invalid
	}
	var cursor trafficCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return trafficCursor{}, nil, invalid
	}
	switch {
	case cursor.Version != trafficCursorVersion:
		return trafficCursor{}, nil, fmt.Errorf("%w: unsupported cursor version %d", domain.ErrInvalidCursor, cursor.Version)
	case cursor.Sort != sort.name:
		return trafficCursor{}, nil, fmt.Errorf("%w: the cursor was made under order_by %q, not %q", domain.ErrInvalidCursor, cursor.Sort, sort.name)
	case cursor.ID == uuid.Nil || cursor.Phase < 0 || cursor.Phase > 1 || (cursor.Phase == 1 && !sort.phased()):
		return trafficCursor{}, nil, invalid
	case cursor.Responses != nil && *cursor.Responses < 0:
		return trafficCursor{}, nil, invalid
	}
	hasKey := len(cursor.Key) > 0 && string(cursor.Key) != "null"
	if wantKey := sort.key.expr != "" && cursor.Phase == 0; hasKey != wantKey {
		return trafficCursor{}, nil, invalid
	}
	if !hasKey {
		return cursor, nil, nil
	}
	if sort.key.integer {
		var key int64
		if err := json.Unmarshal(cursor.Key, &key); err != nil {
			return trafficCursor{}, nil, invalid
		}
		return cursor, key, nil
	}
	var key []byte
	if err := json.Unmarshal(cursor.Key, &key); err != nil {
		return trafficCursor{}, nil, invalid
	}
	// Bound as text: SQLite orders every BLOB after every TEXT.
	return cursor, string(key), nil
}

// cursorAt returns the token for a row under sort, in a walk that started
// at responses.
func cursorAt(sort trafficSort, row sortedRow, responses *int64) *string {
	cursor := trafficCursor{Version: trafficCursorVersion, Sort: sort.name, ID: row.ID, Phase: row.phase, Responses: responses}
	if sort.key.expr != "" && row.phase == 0 {
		var key any
		switch value := row.SortKey.(type) {
		case string:
			key = []byte(value)
		default:
			key = value
		}
		cursor.Key, _ = json.Marshal(key)
	}
	token := cursor.encode()
	return &token
}

// exclusionCondition returns a SQL condition that hides pairs whose content
// type is in exclusion, which must be normalised. Exact types are bound into
// NOT IN; major/* becomes the range [major/, major0), which an index on
// content_type can serve. Pairs with a NULL or empty content type are kept.
// It returns "" for an empty exclusion.
func exclusionCondition(exclusion []string) (string, []any) {
	if len(exclusion) == 0 {
		return "", nil
	}
	var exact []any
	var parts []string
	var args []any
	for _, value := range exclusion {
		if major, ok := strings.CutSuffix(value, "/*"); ok {
			parts = append(parts, `NOT (content_type >= ? AND content_type < ?)`)
			args = append(args, major+"/", major+"0")
			continue
		}
		exact = append(exact, value)
	}
	if len(exact) > 0 {
		parts = append([]string{`content_type NOT IN (?` + strings.Repeat(`, ?`, len(exact)-1) + `)`}, parts...)
		args = append(exact, args...)
	}
	return `content_type IS NULL OR content_type = '' OR (` + strings.Join(parts, ` AND `) + `)`, args
}

// pairExclusionCondition returns a condition on request that hides the
// pairs whose content type is in exclusion, which must be normalised, as
// exclusionCondition does. It looks each candidate's content type up by
// rowid, so it filters the pairs the rest of the WHERE clause selects
// rather than steering the plan: a read that the query's index covers
// stays on that index, where a plain condition on content_type sent SQLite
// through every row of request. It returns "" for an empty exclusion.
func pairExclusionCondition(exclusion []string) (string, []any) {
	if len(exclusion) == 0 {
		return "", nil
	}
	var exact []any
	var parts []string
	var args []any
	for _, value := range exclusion {
		if major, ok := strings.CutSuffix(value, "/*"); ok {
			parts = append(parts, `(excluded.content_type >= ? AND excluded.content_type < ?)`)
			args = append(args, major+"/", major+"0")
			continue
		}
		exact = append(exact, value)
	}
	if len(exact) > 0 {
		parts = append([]string{`excluded.content_type IN (?` + strings.Repeat(`, ?`, len(exact)-1) + `)`}, parts...)
		args = append(exact, args...)
	}
	return `NOT EXISTS (SELECT 1 FROM request excluded WHERE excluded.rowid = request.rowid AND (` + strings.Join(parts, ` OR `) + `))`, args
}

// trafficMatch is a translated traffic query and exclusion, ready to be
// combined with other conditions in a WHERE clause.
type trafficMatch struct {
	conditions []string
	args       []any
	// sitemap means the match depends on the sitemap job.
	sitemap bool
	// text means the match has a text term, so it depends on the search
	// index jobs.
	text bool
}

// jobs returns the background jobs whose progress changes which pairs m
// matches: the search index jobs when it has a text term, and the sitemap
// job when it names the sitemap field.
func (m trafficMatch) jobs() []domain.JobName {
	var jobs []domain.JobName
	if m.text {
		jobs = append(jobs, domain.SearchIndexJobs...)
	}
	if m.sitemap {
		jobs = append(jobs, domain.JobSitemap)
	}
	return jobs
}

// listCompleteness reports the completeness of the jobs a traffic list or
// count of the pairs m matches depends on. Lists and counts always report
// the search index jobs, which clients show as index.complete, even when m
// has no text term.
func listCompleteness(q sqlx.Queryer, m trafficMatch) (map[domain.JobName]bool, error) {
	return jobCompleteness(q, append(slices.Clone(domain.SearchIndexJobs), m.jobs()...)...)
}

// newTrafficMatch translates text and normalises exclusion. An invalid query
// or exclusion returns a *domain.QueryError.
func newTrafficMatch(text string, exclusion []string) (trafficMatch, error) {
	return newTrafficMatchAfter(text, exclusion, nil)
}

// newTrafficMatchAfter is newTrafficMatch for a read of only the pairs
// with an id above after, which it adds as a condition when set.
func newTrafficMatchAfter(text string, exclusion []string, after *uuid.UUID) (trafficMatch, error) {
	translated, err := translateTrafficQuery(text, after)
	if err != nil {
		return trafficMatch{}, err
	}
	normalized, err := domain.NormalizeExclusion(exclusion)
	if err != nil {
		return trafficMatch{}, err
	}
	match := trafficMatch{sitemap: translated.sitemap, text: translated.text}
	if translated.where != "" {
		match.add(translated.where, translated.args...)
	}
	if where, args := pairExclusionCondition(normalized); where != "" {
		match.add(where, args...)
	}
	if after != nil {
		match.add(`id > ?`, *after)
	}
	return match, nil
}

// ValidateTrafficQuery checks a traffic query's text, exclusion and
// order_by without running it. It never touches a database, and it reports
// the same *domain.QueryError that ListTraffic returns for the same text,
// exclusion and order_by.
func ValidateTrafficQuery(text string, exclusion []string, orderBy string) error {
	if _, err := parseTrafficOrderBy(orderBy); err != nil {
		return err
	}
	_, err := newTrafficMatch(text, exclusion)
	return err
}

// add appends a condition, parenthesised, with its arguments.
func (f *trafficMatch) add(condition string, args ...any) {
	f.conditions = append(f.conditions, `(`+condition+`)`)
	f.args = append(f.args, args...)
}

// where returns " WHERE ..." for the match's conditions, or "" if it has none.
func (f trafficMatch) where() string {
	if len(f.conditions) == 0 {
		return ""
	}
	return ` WHERE ` + strings.Join(f.conditions, ` AND `)
}

// sortedRow is a listed pair with its sort key and the phase of the walk it
// was read in.
type sortedRow struct {
	dbRequestResponseSummary
	SortKey any `db:"sort_key"`
	phase   int
}

// ListTraffic returns one page of the pairs that match q, in display order
// under q.OrderBy. The page is bounded by q.AsOf, or by the newest pair id at
// read time when it is nil. An invalid query, exclusion or sort returns a
// *domain.QueryError; a malformed cursor, or one made under another sort,
// wraps domain.ErrInvalidCursor.
func (repo *Repository) ListTraffic(q domain.TrafficQuery) (domain.TrafficPage, error) {
	return repo.ListTrafficContext(context.Background(), q)
}

// ListTrafficContext is ListTraffic for a read that stops when ctx ends.
func (repo *Repository) ListTrafficContext(ctx context.Context, q domain.TrafficQuery) (domain.TrafficPage, error) {
	if q.Limit < 1 {
		return domain.TrafficPage{}, fmt.Errorf("listing traffic: limit %d is not positive", q.Limit)
	}
	sort, err := parseTrafficOrderBy(q.OrderBy)
	if err != nil {
		return domain.TrafficPage{}, err
	}
	match, err := newTrafficMatch(q.Text, q.Exclusion)
	if err != nil {
		return domain.TrafficPage{}, err
	}
	if err := repo.indexReadyFor(match); err != nil {
		return domain.TrafficPage{}, err
	}
	var cursor *trafficCursor
	var cursorKey any
	if q.Cursor != "" {
		decoded, key, err := decodeTrafficCursor(q.Cursor, sort)
		if err != nil {
			return domain.TrafficPage{}, err
		}
		cursor, cursorKey = &decoded, key
	}
	backward := q.Direction == domain.Backward
	var responses *int64
	if cursor != nil {
		responses = cursor.Responses
	}

	var page domain.TrafficPage
	var rows []sortedRow
	err = repo.inTxContext(ctx, func(tx *sqlx.Tx) error {
		complete, err := listCompleteness(tx, match)
		if err != nil {
			return err
		}
		page.Complete = complete

		page.AsOf = q.AsOf
		if page.AsOf == nil {
			var newest uuid.UUID
			err := tx.GetContext(ctx, &newest, `SELECT id FROM request ORDER BY id DESC LIMIT 1`)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			page.AsOf = &newest
		}
		match.add(`id <= ?`, *page.AsOf)
		if cursor == nil && sort.phased() {
			var seq int64
			if err := tx.GetContext(ctx, &seq, `SELECT coalesce(max(seq), 0) FROM pair_response_order`); err != nil {
				return err
			}
			responses = &seq
		}
		for _, segment := range sortSegments(sort, backward, cursor, cursorKey, responses) {
			segmentMatch := trafficMatch{
				conditions: slices.Clone(match.conditions),
				args:       slices.Clone(match.args),
			}
			for _, condition := range segment.conditions {
				segmentMatch.add(condition.sql, condition.args...)
			}
			query := `SELECT ` + trafficSummaryColumns + `, ` + sort.column() + ` AS sort_key` + trafficSummarySource +
				segmentMatch.where() + ` ORDER BY ` + segment.order + ` LIMIT ?`
			var segmentRows []sortedRow
			if err := tx.SelectContext(ctx, &segmentRows, query, append(segmentMatch.args, q.Limit+1-len(rows))...); err != nil {
				return err
			}
			for i := range segmentRows {
				segmentRows[i].phase = segment.phase
			}
			rows = append(rows, segmentRows...)
			if len(rows) > q.Limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return domain.TrafficPage{}, fmt.Errorf("listing traffic: %w", err)
	}

	hasMore := len(rows) > q.Limit
	if hasMore {
		rows = rows[:q.Limit]
	}
	if backward {
		slices.Reverse(rows)
	}
	page.Items = make([]*domain.RequestResponseSummary, len(rows))
	for i := range rows {
		page.Items[i] = toDomainRequestResponseSummary(&rows[i].dbRequestResponseSummary)
	}

	// Cursors point at the edge rows. The side the read came from has rows
	// exactly when a cursor was given; the side it walked toward has rows
	// exactly when the read found more than a page.
	var first, last *string
	if len(rows) > 0 {
		first = cursorAt(sort, rows[0], responses)
		last = cursorAt(sort, rows[len(rows)-1], responses)
	} else if cursor != nil {
		first, last = &q.Cursor, &q.Cursor
	}
	switch {
	case backward:
		page.NextCursor = ifSet(cursor != nil, last)
		page.PrevCursor = ifSet(hasMore, first)
	default:
		page.NextCursor = ifSet(hasMore, last)
		page.PrevCursor = ifSet(cursor != nil, first)
	}
	return page, nil
}

// sqlCondition is one SQL condition with its arguments.
type sqlCondition struct {
	sql  string
	args []any
}

// sortSegment is one index range of a sorted walk: the pairs of one phase,
// past the cursor when it is in that phase, in walk order.
type sortSegment struct {
	phase      int
	conditions []sqlCondition
	order      string
}

// sortSegments returns the reads that walk sort from cursor, in walk order.
// Display order is the pairs with a value by (key, id) in the sort's
// direction, then the pairs with none by id in the same direction. A
// backward walk reads the inverse order. Keyset conditions use row values so
// SQLite walks the (key, id) index.
//
// When responses is set, a pair whose response was written after it counts
// as having no value, so a pair that gets its response during the walk stays
// in the part it was in when the walk started. A backward walk reads the
// pairs with no value first; without this, such a pair would be read again
// with the valued pairs (docs/adr/2026-10-14-traffic-lists-sort-by-any-ledger-column.md).
func sortSegments(sort trafficSort, backward bool, cursor *trafficCursor, cursorKey any, responses *int64) []sortSegment {
	walkDesc := sort.desc != backward
	op, dir := ">", "ASC"
	if walkDesc {
		op, dir = "<", "DESC"
	}
	valued := sortSegment{phase: 0, order: `id ` + dir}
	switch {
	case sort.key.unique:
		valued.order = sort.key.expr + ` ` + dir
	case sort.key.expr != "":
		valued.order = sort.key.expr + ` ` + dir + `, id ` + dir
	}
	if sort.key.hasValue != "" {
		valued.conditions = append(valued.conditions, sqlCondition{sql: sort.key.hasValue})
	}
	unvalued := sortSegment{phase: 1, order: `id ` + dir,
		conditions: []sqlCondition{{sql: sort.key.noValue}}}
	if responses != nil && sort.phased() {
		const later = `SELECT request_id FROM pair_response_order WHERE seq > ?`
		valued.conditions = append(valued.conditions, sqlCondition{`id NOT IN (` + later + `)`, []any{*responses}})
		unvalued.conditions[0] = sqlCondition{`(` + sort.key.noValue + ` OR id IN (` + later + `))`, []any{*responses}}
	}

	if cursor != nil {
		switch {
		case cursor.Phase == 1:
			unvalued.conditions = append(unvalued.conditions, sqlCondition{`id ` + op + ` ?`, []any{cursor.ID}})
		case sort.key.expr == "":
			valued.conditions = append(valued.conditions, sqlCondition{`id ` + op + ` ?`, []any{cursor.ID}})
		case sort.key.unique:
			valued.conditions = append(valued.conditions, sqlCondition{sort.key.expr + ` ` + op + ` ?`, []any{cursorKey}})
		case sort.key.expression:
			// SQLite cannot seek an expression index with a row value, so
			// the key bounds the range and the id breaks ties within it.
			valued.conditions = append(valued.conditions, sqlCondition{
				sort.key.expr + ` ` + op + `= ? AND (` + sort.key.expr + ` ` + op + ` ? OR id ` + op + ` ?)`,
				[]any{cursorKey, cursorKey, cursor.ID},
			})
		default:
			valued.conditions = append(valued.conditions,
				sqlCondition{`(` + sort.key.expr + `, id) ` + op + ` (?, ?)`, []any{cursorKey, cursor.ID}})
		}
	}

	var segments []sortSegment
	phase := -1
	if cursor != nil {
		phase = cursor.Phase
	}
	switch {
	case !sort.phased():
		segments = []sortSegment{valued}
	case backward && phase == 0:
		segments = []sortSegment{valued}
	case backward:
		segments = []sortSegment{unvalued, valued}
	case phase == 1:
		segments = []sortSegment{unvalued}
	default:
		segments = []sortSegment{valued, unvalued}
	}
	return segments
}

// ifSet returns token when ok, otherwise nil.
func ifSet(ok bool, token *string) *string {
	if ok {
		return token
	}
	return nil
}
