package service

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// trafficList is the GET /traffic response body.
type trafficList struct {
	Items      []trafficSummary `json:"items"`
	NextCursor *uuid.UUID       `json:"next_cursor"` // oldest item id when an older page exists
}

// trafficSummary is one request/response pair in a traffic list page.
type trafficSummary struct {
	ID          uuid.UUID      `json:"id"`           // request UUID
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
}

// trafficDetail is the GET /traffic/{id} response body.
type trafficDetail struct {
	ID       uuid.UUID       `json:"id"`       // request UUID
	Note     string          `json:"note"`     // user note, empty if none
	Metadata map[string]any  `json:"metadata"` // combined metadata without prettified bodies
	Request  trafficRequest  `json:"request"`  // captured request
	Response trafficResponse `json:"response"` // captured response; StatusCode is -1 if none yet
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
	Status      string    `json:"status"`       // HTTP status text
	StatusCode  int       `json:"status_code"`  // HTTP status code; -1 if none yet
	ContentType string    `json:"content_type"` // response content type
	Length      string    `json:"length"`       // content length
	Raw         []byte    `json:"raw"`          // complete raw HTTP response
	RespondedAt time.Time `json:"responded_at"` // when the response arrived
}

// addTrafficRoutes registers GET /traffic and GET /traffic/{id}.
func addTrafficRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /traffic", func(w http.ResponseWriter, r *http.Request) {
		limit, cursor, filter, ok := parseTrafficListQuery(r)
		if !ok {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		repo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		items, nextCursor, err := repo.ListTraffic(cursor, limit, filter)
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		slices.Reverse(items)
		writeJSON(w, r, http.StatusOK, trafficListFromSummaries(items, nextCursor))
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
}

// parseTrafficListQuery reads limit, cursor, and filter query parameters.
func parseTrafficListQuery(r *http.Request) (limit int, cursor *uuid.UUID, filter domain.TrafficListFilter, ok bool) {
	query := r.URL.Query()
	limit = 200
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			return 0, nil, filter, false
		}
		limit = parsed
	}
	if raw := query.Get("cursor"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return 0, nil, filter, false
		}
		cursor = &parsed
	}
	filter.Host = query.Get("host")
	filter.Method = query.Get("method")
	filter.PathPrefix = query.Get("path")
	if raw := query.Get("status_code"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return 0, nil, filter, false
		}
		filter.StatusCode = &parsed
	}
	return limit, cursor, filter, true
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
		ID:          item.ID,
		Scheme:      item.Scheme,
		Method:      item.Method,
		Host:        item.Host,
		Path:        item.Path,
		Status:      item.Status,
		StatusCode:  item.StatusCode,
		ContentType: item.ContentType,
		Length:      item.Length,
		Metadata:    metadataWithoutPrettified(item.Metadata),
		RequestedAt: item.RequestedAt,
	}
	if !item.RespondedAt.IsZero() {
		respondedAt := item.RespondedAt
		summary.RespondedAt = &respondedAt
	}
	return summary
}

// trafficDetailFromRow maps a repository row to a detail response.
func trafficDetailFromRow(row *domain.RequestResponseRow) trafficDetail {
	metadata := metadataWithoutPrettified(row.Metadata)
	return trafficDetail{
		ID:       row.Request.ID,
		Note:     row.Note,
		Metadata: metadata,
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
			RespondedAt: row.Response.RespondedAt,
		},
	}
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
