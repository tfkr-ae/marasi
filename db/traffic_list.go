package db

import (
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
// dbRequestResponseSummary.
const trafficSummaryColumns = `id, scheme, method, host, path, requested_at,
	status, status_code, content_type, length, responded_at,
	json_remove(metadata, '$.prettified-request', '$.prettified-response') AS metadata`

// defaultTrafficOrder is the canonical name of the default sort. It is the
// only sort until sortable ledger columns land (ADR-0041).
const defaultTrafficOrder = "requested_at desc"

// trafficCursorVersion is the version of the cursor token format.
const trafficCursorVersion = 1

// trafficCursor is the decoded form of an opaque list cursor (ADR-0041). It
// names the sort it was made under, the row's sort key and id, and the phase
// of the walk: 0 while walking pairs with a sort value, 1 once the walk has
// reached the pairs with none. Key is null for requested_at, whose key is id.
type trafficCursor struct {
	Version int             `json:"v"`
	Sort    string          `json:"s"`
	Key     json.RawMessage `json:"k,omitempty"`
	ID      uuid.UUID       `json:"id"`
	Phase   int             `json:"p"`
}

// encode returns the cursor as an opaque token: unpadded URL-safe base64 of
// its JSON.
func (c trafficCursor) encode() string {
	raw, _ := json.Marshal(c) // a struct of plain fields cannot fail to marshal
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeTrafficCursor parses token and checks that it was made under sort.
// Any failure wraps domain.ErrInvalidCursor.
func decodeTrafficCursor(token, sort string) (trafficCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return trafficCursor{}, fmt.Errorf("%w: not a cursor token", domain.ErrInvalidCursor)
	}
	var cursor trafficCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return trafficCursor{}, fmt.Errorf("%w: not a cursor token", domain.ErrInvalidCursor)
	}
	switch {
	case cursor.Version != trafficCursorVersion:
		return trafficCursor{}, fmt.Errorf("%w: unsupported cursor version %d", domain.ErrInvalidCursor, cursor.Version)
	case cursor.Sort != sort:
		return trafficCursor{}, fmt.Errorf("%w: the cursor was made under order_by %q, not %q", domain.ErrInvalidCursor, cursor.Sort, sort)
	case cursor.ID == uuid.Nil || cursor.Phase != 0:
		return trafficCursor{}, fmt.Errorf("%w: not a cursor token", domain.ErrInvalidCursor)
	}
	return cursor, nil
}

// parseTrafficOrderBy returns the canonical name of orderBy. Until sortable
// columns land, only the empty string and "requested_at desc" are accepted.
func parseTrafficOrderBy(orderBy string) (string, error) {
	canonical := strings.Join(strings.Fields(orderBy), " ")
	if canonical == "" || canonical == defaultTrafficOrder {
		return defaultTrafficOrder, nil
	}
	return "", &domain.QueryError{Message: fmt.Sprintf("unsupported order_by %q: only %q is supported", orderBy, defaultTrafficOrder), Position: 1}
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

// trafficFilter is a translated traffic query and exclusion, ready to be
// combined with other conditions in a WHERE clause.
type trafficFilter struct {
	conditions []string
	args       []any
	// sitemap means the filter depends on the sitemap job.
	sitemap bool
}

// newTrafficFilter translates text and normalises exclusion. An invalid query
// or exclusion returns a *domain.QueryError.
func newTrafficFilter(text string, exclusion []string) (trafficFilter, error) {
	translated, err := translateTrafficQuery(text)
	if err != nil {
		return trafficFilter{}, err
	}
	normalized, err := domain.NormalizeExclusion(exclusion)
	if err != nil {
		return trafficFilter{}, err
	}
	filter := trafficFilter{sitemap: translated.sitemap}
	if translated.where != "" {
		filter.add(translated.where, translated.args...)
	}
	if where, args := exclusionCondition(normalized); where != "" {
		filter.add(where, args...)
	}
	return filter, nil
}

// add appends a condition, parenthesised, with its arguments.
func (f *trafficFilter) add(condition string, args ...any) {
	f.conditions = append(f.conditions, `(`+condition+`)`)
	f.args = append(f.args, args...)
}

// where returns " WHERE ..." for the filter's conditions, or "" if it has none.
func (f trafficFilter) where() string {
	if len(f.conditions) == 0 {
		return ""
	}
	return ` WHERE ` + strings.Join(f.conditions, ` AND `)
}

// ListTraffic returns one page of the pairs that match q, in display order.
// The page is bounded by q.AsOf, or by the newest pair id at read time when
// it is nil. An invalid query, exclusion or sort returns a *domain.QueryError;
// a malformed cursor, or one made under another sort, wraps
// domain.ErrInvalidCursor.
func (repo *Repository) ListTraffic(q domain.TrafficQuery) (domain.TrafficPage, error) {
	if q.Limit < 1 {
		return domain.TrafficPage{}, fmt.Errorf("listing traffic: limit %d is not positive", q.Limit)
	}
	sort, err := parseTrafficOrderBy(q.OrderBy)
	if err != nil {
		return domain.TrafficPage{}, err
	}
	filter, err := newTrafficFilter(q.Text, q.Exclusion)
	if err != nil {
		return domain.TrafficPage{}, err
	}
	var cursor *trafficCursor
	if q.Cursor != "" {
		decoded, err := decodeTrafficCursor(q.Cursor, sort)
		if err != nil {
			return domain.TrafficPage{}, err
		}
		cursor = &decoded
	}
	backward := q.Direction == domain.Backward

	var page domain.TrafficPage
	var dbSummary []*dbRequestResponseSummary
	err = repo.inTx(func(tx *sqlx.Tx) error {
		indexComplete, err := repo.indexComplete(tx)
		if err != nil {
			return err
		}
		page.Complete = map[string]bool{domain.JobTrafficIndex: indexComplete}
		if filter.sitemap {
			job, err := loadJob(tx, SitemapJob)
			if err != nil {
				return fmt.Errorf("reading the sitemap job: %w", err)
			}
			page.Complete[SitemapJob] = job.Complete
		}

		page.AsOf = q.AsOf
		if page.AsOf == nil {
			var newest uuid.UUID
			err := tx.Get(&newest, `SELECT id FROM request ORDER BY id DESC LIMIT 1`)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			page.AsOf = &newest
		}

		filter.add(`id <= ?`, *page.AsOf)
		order := `DESC`
		if backward {
			order = `ASC`
		}
		if cursor != nil {
			if backward {
				filter.add(`id > ?`, cursor.ID)
			} else {
				filter.add(`id < ?`, cursor.ID)
			}
		}
		query := `SELECT ` + trafficSummaryColumns + ` FROM request` + filter.where() +
			` ORDER BY id ` + order + ` LIMIT ?`
		return tx.Select(&dbSummary, query, append(filter.args, q.Limit+1)...)
	})
	if err != nil {
		return domain.TrafficPage{}, fmt.Errorf("listing traffic: %w", err)
	}

	hasMore := len(dbSummary) > q.Limit
	if hasMore {
		dbSummary = dbSummary[:q.Limit]
	}
	if backward {
		slices.Reverse(dbSummary)
	}
	page.Items = make([]*domain.RequestResponseSummary, len(dbSummary))
	for i, row := range dbSummary {
		page.Items[i] = toDomainRequestResponseSummary(row)
	}

	// Cursors point at the edge rows. The side the read came from has rows
	// exactly when a cursor was given; the side it walked toward has rows
	// exactly when the read found more than a page.
	var first, last *string
	if len(page.Items) > 0 {
		first = cursorAt(sort, page.Items[0].ID)
		last = cursorAt(sort, page.Items[len(page.Items)-1].ID)
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

// cursorAt returns the token for the row with id under sort.
func cursorAt(sort string, id uuid.UUID) *string {
	token := trafficCursor{Version: trafficCursorVersion, Sort: sort, ID: id}.encode()
	return &token
}

// ifSet returns token when ok, otherwise nil.
func ifSet(ok bool, token *string) *string {
	if ok {
		return token
	}
	return nil
}
