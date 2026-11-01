package service

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

var errInvalidMetadataRequest = errors.New("invalid metadata request")

// trafficPage is the GET /traffic response body. Items are the page in
// reverse display order, so oldest first under the default requested_at
// desc sort. NextCursor continues along the display order (toward older
// pairs by default) and PrevCursor back against it; each is null at its end. AsOf is the newest pair id the
// page could include; clients pass it back on every later page of the same
// results.
type trafficPage struct {
	Items      []trafficSummary `json:"items"`
	NextCursor *string          `json:"next_cursor"`
	PrevCursor *string          `json:"prev_cursor"`
	AsOf       *uuid.UUID       `json:"as_of"`
	Index      trafficIndex     `json:"index"`
	// Sitemap is set when the query has a sitemap condition.
	Sitemap *jobCompleteness `json:"sitemap,omitempty"`
}

// trafficCount is the GET /traffic/count response body. Count is exact up to
// 100,000; above it, Count is 100,000 and Capped is true.
type trafficCount struct {
	Count  int          `json:"count"`
	Capped bool         `json:"capped"`
	Index  trafficIndex `json:"index"`
	// Sitemap is set when the query has a sitemap condition.
	Sitemap *jobCompleteness `json:"sitemap,omitempty"`
}

// trafficFieldCounts is the GET /traffic/count/by-field response body.
// Items hold every value of the field, uncapped, most pairs first; a pair
// with no value counts under "". AsOf is the newest pair id the counts could
// include, null when there is no pair.
type trafficFieldCounts struct {
	Items []trafficFieldCount `json:"items"`
	AsOf  *uuid.UUID          `json:"as_of"`
	Index trafficIndex        `json:"index"`
	// Sitemap is set when the query has a sitemap condition.
	Sitemap *jobCompleteness `json:"sitemap,omitempty"`
}

// trafficFieldCount is one value of the counted field and its pairs.
type trafficFieldCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// trafficList is the body of a traffic list paged by UUID, such as an
// Armory run's traffic.
type trafficList struct {
	Items      []trafficSummary `json:"items"`
	NextCursor *uuid.UUID       `json:"next_cursor"` // oldest item id when an older page exists
	Index      *trafficIndex    `json:"index,omitempty"`
}

// trafficIndex reports the state of the traffic index (docs/adr/2026-10-09-traffic-index-builds-in-the-background.md).
type trafficIndex struct {
	// Complete is false while the background build is still indexing pairs.
	// Text conditions in a query can miss pairs until it is true.
	Complete bool `json:"complete"`
}

// searchComplete reports whether every search index job in complete had
// finished, which is when text conditions can miss no pair. Completeness
// that names no search index job is incomplete.
func searchComplete(complete map[domain.JobName]bool) bool {
	_, found := complete[domain.JobTrafficIndex]
	for _, name := range domain.SearchIndexJobs {
		if done, ok := complete[name]; ok && !done {
			return false
		}
	}
	return found
}

// sitemapCompleteness returns the sitemap job's completeness when complete
// names it, which a traffic read does when its query has a sitemap
// condition, and nil otherwise.
func sitemapCompleteness(complete map[domain.JobName]bool) *jobCompleteness {
	done, ok := complete[domain.JobSitemap]
	if !ok {
		return nil
	}
	return &jobCompleteness{Complete: done}
}

// jobCompleteness reports whether a background job a read depends on had
// finished. While the sitemap job runs, pairs it has not reached are missing
// from the sitemap's counts and from sitemap query conditions.
type jobCompleteness struct {
	Complete bool `json:"complete"`
}

// sitemapPage is the body of GET /traffic/sitemap.
type sitemapPage struct {
	Items      []domain.SitemapChild `json:"items"`
	NextCursor *string               `json:"next_cursor"`
	Sitemap    jobCompleteness       `json:"sitemap"`
	// Index is set in query mode when the query has a text term.
	Index *trafficIndex `json:"index,omitempty"`
}

// sitemapLevelsRequest is the body of POST /traffic/sitemap/levels.
type sitemapLevelsRequest struct {
	Nodes     []string            `json:"nodes"`
	Scope     domain.SitemapScope `json:"scope"`
	Exclusion []string            `json:"exclusion"`
	Selection *struct {
		Query     string     `json:"query"`
		Exclusion []string   `json:"exclusion"`
		AsOf      *uuid.UUID `json:"as_of"`
	} `json:"selection"`
	Cursors map[string]string `json:"cursors"`
	Limit   *int              `json:"limit"`
}

// sitemapLevelsPage is the response of POST /traffic/sitemap/levels.
type sitemapLevelsPage struct {
	Levels  []sitemapLevel  `json:"levels"`
	Sitemap jobCompleteness `json:"sitemap"`
	// Index is set when the selection has a text term.
	Index *trafficIndex `json:"index,omitempty"`
}

// sitemapLevel is one node's page in a sitemapLevelsPage.
type sitemapLevel struct {
	Node       string                     `json:"node"`
	Items      []domain.SitemapLevelChild `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
	Missing    bool                       `json:"missing"`
}

// decodeSitemapLevelsQuery reads a POST /traffic/sitemap/levels body. It
// reports false for malformed JSON, an unknown field, a selection without
// as_of, or a limit outside 1 to domain.MaxSitemapPage. The limit defaults
// to 100, as GET /traffic/sitemap's does.
func decodeSitemapLevelsQuery(r *http.Request) (domain.SitemapLevelsQuery, bool) {
	var body sitemapLevelsRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return domain.SitemapLevelsQuery{}, false
	}
	q := domain.SitemapLevelsQuery{Nodes: body.Nodes, Scope: body.Scope, Exclusion: body.Exclusion, Cursors: body.Cursors, Limit: 100}
	if body.Limit != nil {
		if *body.Limit < 1 || *body.Limit > domain.MaxSitemapPage {
			return domain.SitemapLevelsQuery{}, false
		}
		q.Limit = *body.Limit
	}
	if body.Selection != nil {
		if body.Selection.AsOf == nil {
			return domain.SitemapLevelsQuery{}, false
		}
		q.Selection = &domain.SelectionQuery{Text: body.Selection.Query, Exclusion: body.Selection.Exclusion, AsOf: *body.Selection.AsOf}
	}
	return q, true
}

// contentTypeCounts is the body of GET /traffic/content-types.
type contentTypeCounts struct {
	Items   []contentTypeCount `json:"items"`
	Sitemap jobCompleteness    `json:"sitemap"`
}

// contentTypeCount is the number of pairs with one response content type;
// "" when the pair has none.
type contentTypeCount struct {
	ContentType string `json:"content_type"`
	Count       int    `json:"count"`
}

// trafficSummary is one request/response pair in a traffic list page.
type trafficSummary struct {
	ID          uuid.UUID      `json:"id"`           // request UUID
	Number      int64          `json:"number"`       // pair number in capture order (docs/adr/2026-10-16-pairs-are-numbered-in-capture-order.md)
	Scheme      string         `json:"scheme"`       // http or https
	Method      string         `json:"method"`       // HTTP method
	Host        string         `json:"host"`         // request host
	Path        string         `json:"path"`         // path including query
	Status      string         `json:"status"`       // HTTP status text
	StatusCode  int            `json:"status_code"`  // HTTP status code
	ContentType string         `json:"content_type"` // response content type
	Length      string         `json:"length"`       // content length
	Metadata    map[string]any `json:"metadata"`     // request metadata without prettified bodies
	RequestedAt time.Time      `json:"requested_at"` // when the request was made
	RespondedAt *time.Time     `json:"responded_at"` // when the response arrived; nil if none
	Highlight   *int           `json:"highlight"`    // highlight from 1 to 8; nil if none
	// UpstreamProxy is the route that carried the pair, as in
	// trafficDetail.
	UpstreamProxy *trafficRoute `json:"upstream_proxy"`
}

// trafficDetail is the GET /traffic/{id} response body.
type trafficDetail struct {
	ID        uuid.UUID       `json:"id"`        // request UUID
	Number    int64           `json:"number"`    // pair number in capture order (docs/adr/2026-10-16-pairs-are-numbered-in-capture-order.md)
	Note      string          `json:"note"`      // user note, empty if none
	Metadata  map[string]any  `json:"metadata"`  // combined metadata without prettified bodies
	Highlight *int            `json:"highlight"` // highlight from 1 to 8; nil if none
	Request   trafficRequest  `json:"request"`   // captured request
	Response  trafficResponse `json:"response"`  // captured response; StatusCode is -1 if none yet
	// Prettified holds the prettified copies the proxy stored at capture
	// (docs/adr/2026-10-14-prettified-bodies-leave-metadata.md). They are never part of metadata.
	Prettified trafficPrettified `json:"prettified"`
	// UpstreamProxy is the route that carried the pair, written with its
	// response. It is null before the response or for a pair captured
	// before route snapshots existed.
	UpstreamProxy *trafficRoute `json:"upstream_proxy"`
}

// trafficRoute is a pair's route snapshot: the upstream proxy that carried
// it, or tried to, by name and address without credentials. Direct is an
// empty name and address. It is read-only and never part of metadata.
type trafficRoute struct {
	Name    string `json:"name"`    // upstream proxy name, "" for direct
	Address string `json:"address"` // scheme://host:port, "" for direct
}

// trafficRouteFromDomain returns route's JSON shape, or nil when the pair
// has no snapshot.
func trafficRouteFromDomain(route *domain.RouteSnapshot) *trafficRoute {
	if route == nil {
		return nil
	}
	return &trafficRoute{Name: route.Name, Address: route.Address}
}

// trafficPrettified is the prettified copies of a traffic detail. A copy is
// null when the proxy could not prettify that body.
type trafficPrettified struct {
	Request  *string `json:"request"`  // prettified request, or null
	Response *string `json:"response"` // prettified response, or null
}

// trafficRequest is the request half of a traffic detail.
type trafficRequest struct {
	Scheme      string    `json:"scheme"`       // http or https
	Method      string    `json:"method"`       // HTTP method
	Host        string    `json:"host"`         // request host
	Path        string    `json:"path"`         // path including query
	Raw         []byte    `json:"raw"`          // complete raw HTTP request
	RequestedAt time.Time `json:"requested_at"` // when the request was made
}

// trafficResponse is the response half of a traffic detail.
type trafficResponse struct {
	Status      string     `json:"status"`       // HTTP status text
	StatusCode  int        `json:"status_code"`  // HTTP status code; -1 if none yet
	ContentType string     `json:"content_type"` // response content type
	Length      string     `json:"length"`       // content length
	Raw         []byte     `json:"raw"`          // complete raw HTTP response
	RespondedAt *time.Time `json:"responded_at"` // when the response arrived; nil if none
}

// addTrafficRoutes registers traffic and metadata control routes.
func addTrafficRoutes(mux routeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /traffic", func(w http.ResponseWriter, r *http.Request) {
		if message, removed := removedTrafficListParameter(r.URL.Query()); removed {
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: message})
			return
		}
		query, ok := parseTrafficQuery(r)
		if !ok {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		page, err := repo.ListTrafficContext(r.Context(), query)
		var queryErr *domain.QueryError
		switch {
		case r.Context().Err() != nil:
			return // the client went away; nobody reads the answer
		case errors.As(err, &queryErr):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: queryErr.Message, Position: queryErr.Position})
			return
		case errors.Is(err, domain.ErrInvalidCursor):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_cursor", Message: err.Error()})
			return
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		slices.Reverse(page.Items)
		body := trafficPage{
			Items:      trafficListFromSummaries(page.Items, nil).Items,
			NextCursor: page.NextCursor,
			PrevCursor: page.PrevCursor,
			AsOf:       page.AsOf,
			Index:      trafficIndex{Complete: searchComplete(page.Complete)},
			Sitemap:    sitemapCompleteness(page.Complete),
		}
		writeJSON(w, r, http.StatusOK, body)
	})
	mux.HandleFunc("GET /traffic/sitemap", func(w http.ResponseWriter, r *http.Request) {
		sitemapQuery, ok := parseSitemapQuery(r)
		if !ok {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		page, err := repo.ListSitemapChildren(r.Context(), sitemapQuery)
		var queryErr *domain.QueryError
		switch {
		case r.Context().Err() != nil:
			return // the client went away; nobody reads the answer
		case errors.Is(err, domain.ErrSitemapNodeNotFound):
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		case errors.As(err, &queryErr):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: queryErr.Message, Position: queryErr.Position})
			return
		case errors.Is(err, domain.ErrInvalidCursor):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_cursor", Message: err.Error()})
			return
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		body := sitemapPage{
			Items:      page.Children,
			NextCursor: page.NextCursor,
			Sitemap:    jobCompleteness{Complete: page.Complete[domain.JobSitemap]},
		}
		if _, ok := page.Complete[domain.JobTrafficIndex]; ok {
			body.Index = &trafficIndex{Complete: searchComplete(page.Complete)}
		}
		writeJSON(w, r, http.StatusOK, body)
	})
	mux.HandleFunc("POST /traffic/sitemap/levels", func(w http.ResponseWriter, r *http.Request) {
		levelsQuery, ok := decodeSitemapLevelsQuery(r)
		if !ok {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		levels, err := repo.ListSitemapLevels(r.Context(), levelsQuery)
		var queryErr *domain.QueryError
		switch {
		case r.Context().Err() != nil:
			return // the client went away; nobody reads the answer
		case errors.Is(err, domain.ErrInvalidSitemapLevels):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "bad_request", Message: err.Error()})
			return
		case errors.As(err, &queryErr):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: queryErr.Message, Position: queryErr.Position})
			return
		case errors.Is(err, domain.ErrInvalidCursor):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_cursor", Message: err.Error()})
			return
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		body := sitemapLevelsPage{
			Levels:  make([]sitemapLevel, len(levels.Levels)),
			Sitemap: jobCompleteness{Complete: levels.Complete[domain.JobSitemap]},
		}
		for i, level := range levels.Levels {
			body.Levels[i] = sitemapLevel{Node: level.Node, Items: level.Children, NextCursor: level.NextCursor, Missing: level.Missing}
		}
		if _, ok := levels.Complete[domain.JobTrafficIndex]; ok {
			body.Index = &trafficIndex{Complete: searchComplete(levels.Complete)}
		}
		writeJSON(w, r, http.StatusOK, body)
	})
	mux.HandleFunc("GET /traffic/content-types", func(w http.ResponseWriter, r *http.Request) {
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		counts, err := repo.CountContentTypesContext(r.Context())
		if r.Context().Err() != nil {
			return // the client went away; nobody reads the answer
		}
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		body := contentTypeCounts{Items: make([]contentTypeCount, len(counts.Counts)), Sitemap: jobCompleteness{Complete: counts.Complete[domain.JobSitemap]}}
		for i, count := range counts.Counts {
			body.Items[i] = contentTypeCount{ContentType: count.ContentType, Count: count.Count}
		}
		writeJSON(w, r, http.StatusOK, body)
	})
	mux.HandleFunc("GET /traffic/count", func(w http.ResponseWriter, r *http.Request) {
		query, ok := parseTrafficCountQuery(r)
		if !ok {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		count, err := repo.CountTrafficContext(r.Context(), query)
		var queryErr *domain.QueryError
		switch {
		case r.Context().Err() != nil:
			return // the client went away; nobody reads the answer
		case errors.As(err, &queryErr):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: queryErr.Message, Position: queryErr.Position})
			return
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		writeJSON(w, r, http.StatusOK, trafficCount{
			Count:   count.Count,
			Capped:  count.Capped,
			Index:   trafficIndex{Complete: searchComplete(count.Complete)},
			Sitemap: sitemapCompleteness(count.Complete),
		})
	})
	// GET /traffic/count/by-field reads as_of in the same transaction as the
	// counts when the request omits it, so a client can pass the bound back
	// and count the same pairs again (docs/adr/2026-10-14-query-selections-stop-at-as-of.md).
	mux.HandleFunc("GET /traffic/count/by-field", func(w http.ResponseWriter, r *http.Request) {
		query, ok := parseTrafficFieldCountQuery(r)
		if !ok {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		counts, err := repo.CountTrafficByFieldContext(r.Context(), query)
		var queryErr *domain.QueryError
		switch {
		case r.Context().Err() != nil:
			return // the client went away; nobody reads the answer
		case errors.Is(err, domain.ErrInvalidCountField):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_count_field", Message: err.Error()})
			return
		case errors.As(err, &queryErr):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: queryErr.Message, Position: queryErr.Position})
			return
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		body := trafficFieldCounts{
			Items:   make([]trafficFieldCount, len(counts.Counts)),
			AsOf:    counts.AsOf,
			Index:   trafficIndex{Complete: searchComplete(counts.Complete)},
			Sitemap: sitemapCompleteness(counts.Complete),
		}
		for i, count := range counts.Counts {
			body.Items[i] = trafficFieldCount{Value: count.Value, Count: count.Count}
		}
		writeJSON(w, r, http.StatusOK, body)
	})
	// GET /traffic/query checks q and exclude without running them, so it
	// needs no open project. It returns 204, or the invalid_query body
	// GET /traffic would return.
	mux.HandleFunc("GET /traffic/query", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var queryErr *domain.QueryError
		switch err := db.ValidateTrafficQuery(query.Get("q"), query["exclude"], query.Get("order_by")); {
		case errors.As(err, &queryErr):
			writeJSON(w, r, http.StatusBadRequest, invalidQuery{Error: "invalid_query", Message: queryErr.Message, Position: queryErr.Position})
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("GET /traffic/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		row, err := repo.GetRequestResponseRow(id)
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, r, http.StatusOK, trafficDetailFromRow(row))
	})
	mux.HandleFunc("GET /traffic/{id}/metadata", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		metadata, err := repo.GetMetadata(id)
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, r, http.StatusOK, metadataWithoutPrettified(metadata))
	})
	mux.HandleFunc("PUT /traffic/{id}/metadata", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		body, err := decodeMetadataBody(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_metadata_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if _, err := repo.GetMetadata(id); err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		_, noteErr := repo.GetNote(id)
		stored := overlayMetadata(body, noteErr == nil)
		if err := repo.UpdateMetadata(stored, id); err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		events.publish("metadata.updated", struct {
			ID       uuid.UUID      `json:"id"`
			Metadata map[string]any `json:"metadata"`
		}{ID: id, Metadata: stored})
		writeJSON(w, r, http.StatusOK, stored)
	})
}

// decodeMetadataBody reads one JSON object and rejects extra values.
func decodeMetadataBody(r *http.Request) (map[string]any, error) {
	if r.Body == nil {
		return nil, errInvalidMetadataRequest
	}
	decoder := json.NewDecoder(r.Body)
	var body map[string]any
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, errInvalidMetadataRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errInvalidMetadataRequest
	}
	return body, nil
}

// invalidQuery is the 400 body for an invalid traffic query. Position is the
// 1-based character offset of the problem in q, omitted when the problem is
// not in q.
type invalidQuery struct {
	Error    string `json:"error"`
	Message  string `json:"message"`
	Position int    `json:"position,omitempty"`
}

// removedTrafficListParameter reports the first removed list parameter in
// query, with a message naming the query clause that replaces it. Callers
// that still send one get an error instead of unfiltered traffic.
func removedTrafficListParameter(query url.Values) (string, bool) {
	for _, name := range []string{"host", "method", "path", "status_code"} {
		if !query.Has(name) {
			continue
		}
		value := query.Get(name)
		var clause string
		switch name {
		case "host":
			clause = "host = " + quoteQueryString(cmp.Or(value, "example.com"))
		case "method":
			clause = "method = " + quoteQueryString(cmp.Or(value, "GET"))
		case "path":
			// The removed parameter matched a path prefix.
			clause = "path = " + quoteQueryString(cmp.Or(value, "/")+"*")
		case "status_code":
			if _, err := strconv.Atoi(value); err != nil {
				value = "200"
			}
			clause = "status_code = " + value
		}
		return fmt.Sprintf("the %s parameter was removed; use q=%s", name, clause), true
	}
	return "", false
}

// quoteQueryString quotes value as an AIP-160 string literal. Only quotes,
// backslashes, and control characters are escaped, with escapes AIP-160
// defines; other characters, including non-ASCII, are written as they are.
func quoteQueryString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range strings.ToValidUTF8(value, "\uFFFD") {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// parseTrafficQuery reads a GET /traffic request's q, exclude, limit,
// cursor, direction and as_of parameters. The cursor and exclusion are
// passed through for the repository to check.
func parseTrafficQuery(r *http.Request) (domain.TrafficQuery, bool) {
	query := r.URL.Query()
	q := domain.TrafficQuery{
		Text:      query.Get("q"),
		Exclusion: query["exclude"],
		OrderBy:   query.Get("order_by"),
		Cursor:    query.Get("cursor"),
		Limit:     200,
	}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			return domain.TrafficQuery{}, false
		}
		q.Limit = parsed
	}
	switch direction := query["direction"]; {
	case len(direction) == 0 || (len(direction) == 1 && direction[0] == "forward"):
	case len(direction) == 1 && direction[0] == "backward":
		q.Direction = domain.Backward
	default:
		return domain.TrafficQuery{}, false
	}
	if raw := query.Get("as_of"); raw != "" {
		asOf, err := uuid.Parse(raw)
		if err != nil {
			return domain.TrafficQuery{}, false
		}
		q.AsOf = &asOf
	}
	return q, true
}

// parseSitemapQuery reads a GET /traffic/sitemap request's node, q, exclude,
// as_of, cursor and limit parameters. A q parameter, even an empty one,
// selects query mode, which needs as_of; as_of without q, a malformed id or
// an out-of-range limit reports false.
func parseSitemapQuery(r *http.Request) (domain.SitemapQuery, bool) {
	query := r.URL.Query()
	q := domain.SitemapQuery{Node: query.Get("node"), Cursor: query.Get("cursor"), Limit: 100}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > domain.MaxSitemapPage {
			return domain.SitemapQuery{}, false
		}
		q.Limit = parsed
	}
	if !query.Has("q") {
		q.Exclusion = query["exclude"]
		return q, !query.Has("as_of")
	}
	asOf, err := uuid.Parse(query.Get("as_of"))
	if err != nil {
		return domain.SitemapQuery{}, false
	}
	q.Query = &domain.SelectionQuery{Text: query.Get("q"), Exclusion: query["exclude"], AsOf: asOf}
	return q, true
}

// parseTrafficCountQuery reads q, exclude, as_of and after from a
// GET /traffic/count request. It reports false for a malformed id.
func parseTrafficCountQuery(r *http.Request) (domain.TrafficCountQuery, bool) {
	query := r.URL.Query()
	q := domain.TrafficCountQuery{Text: query.Get("q"), Exclusion: query["exclude"]}
	for name, bound := range map[string]**uuid.UUID{"as_of": &q.AsOf, "after": &q.After} {
		raw := query.Get(name)
		if raw == "" {
			continue
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return domain.TrafficCountQuery{}, false
		}
		*bound = &id
	}
	return q, true
}

// parseTrafficFieldCountQuery reads field, q, exclude and as_of from a
// GET /traffic/count/by-field request. It reports false for a malformed
// as_of; the repository checks the field.
func parseTrafficFieldCountQuery(r *http.Request) (domain.TrafficFieldCountQuery, bool) {
	query := r.URL.Query()
	q := domain.TrafficFieldCountQuery{Text: query.Get("q"), Exclusion: query["exclude"], Field: query.Get("field")}
	if raw := query.Get("as_of"); raw != "" {
		asOf, err := uuid.Parse(raw)
		if err != nil {
			return domain.TrafficFieldCountQuery{}, false
		}
		q.AsOf = &asOf
	}
	return q, true
}

// trafficListFromSummaries maps repository summaries to a list response.
func trafficListFromSummaries(items []*domain.RequestResponseSummary, nextCursor *uuid.UUID) trafficList {
	summaries := make([]trafficSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, trafficSummaryFromDomain(item))
	}
	return trafficList{Items: summaries, NextCursor: nextCursor}
}

// trafficSummaryFromDomain maps a repository summary to a list item.
func trafficSummaryFromDomain(item *domain.RequestResponseSummary) trafficSummary {
	summary := trafficSummary{
		ID:            item.ID,
		Number:        item.Number,
		Scheme:        item.Scheme,
		Method:        item.Method,
		Host:          item.Host,
		Path:          item.Path,
		Status:        item.Status,
		StatusCode:    item.StatusCode,
		ContentType:   item.ContentType,
		Length:        item.Length,
		Metadata:      metadataWithoutPrettified(item.Metadata),
		RequestedAt:   item.RequestedAt,
		RespondedAt:   respondedAt(item.RespondedAt),
		Highlight:     item.Highlight,
		UpstreamProxy: trafficRouteFromDomain(item.Route),
	}
	return summary
}

// respondedAt returns the response time, or nil when the pair has no response.
func respondedAt(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

// trafficDetailFromRow maps a repository row to a detail response.
func trafficDetailFromRow(row *domain.RequestResponseRow) trafficDetail {
	metadata := metadataWithoutPrettified(row.Metadata)
	return trafficDetail{
		ID:        row.Request.ID,
		Number:    row.Request.Number,
		Note:      row.Note,
		Metadata:  metadata,
		Highlight: row.Highlight,
		Request: trafficRequest{
			Scheme:      row.Request.Scheme,
			Method:      row.Request.Method,
			Host:        row.Request.Host,
			Path:        row.Request.Path,
			Raw:         row.Request.Raw,
			RequestedAt: row.Request.RequestedAt,
		},
		Response: trafficResponse{
			Status:      row.Response.Status,
			StatusCode:  row.Response.StatusCode,
			ContentType: row.Response.ContentType,
			Length:      row.Response.Length,
			Raw:         row.Response.Raw,
			RespondedAt: respondedAt(row.Response.RespondedAt),
		},
		Prettified: trafficPrettified{
			Request:  prettifiedCopy(row.Prettified.Request),
			Response: prettifiedCopy(row.Prettified.Response),
		},
		UpstreamProxy: trafficRouteFromDomain(row.Route),
	}
}

// prettifiedCopy returns copy, or nil when the pair has no such copy.
func prettifiedCopy(copy string) *string {
	if copy == "" {
		return nil
	}
	return &copy
}

// overlayMetadata replaces client-owned keys and sets has_note from the notes
// table (docs/adr/2026-10-06-metadata-replace-with-overlay.md). Prettified copies are not metadata (docs/adr/2026-10-14-prettified-bodies-leave-metadata.md), so keys of
// that name from the client are dropped.
func overlayMetadata(client map[string]any, hasNote bool) map[string]any {
	stored := make(map[string]any, len(client)+1)
	for key, value := range client {
		if key == "prettified-request" || key == "prettified-response" || key == "has_note" {
			continue
		}
		stored[key] = value
	}
	if hasNote {
		stored["has_note"] = true
	}
	return stored
}

// metadataWithoutPrettified copies metadata without prettified-request and prettified-response.
func metadataWithoutPrettified(metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if key == "prettified-request" || key == "prettified-response" {
			continue
		}
		out[key] = value
	}
	return out
}
