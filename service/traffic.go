package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

var errInvalidMetadataRequest = errors.New("invalid metadata request")

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

// addTrafficRoutes registers traffic and metadata control routes.
func addTrafficRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
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
		previous, err := repo.GetMetadata(id)
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		_, noteErr := repo.GetNote(id)
		stored := overlayMetadata(body, previous, noteErr == nil)
		if err := repo.UpdateMetadata(stored, id); err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		stripped := metadataWithoutPrettified(stored)
		events.publish("metadata.updated", struct {
			ID       uuid.UUID      `json:"id"`
			Metadata map[string]any `json:"metadata"`
		}{ID: id, Metadata: stripped})
		writeJSON(w, r, http.StatusOK, stripped)
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

// overlayMetadata replaces client-owned keys, copies proxy-owned prettified
// bodies from the previous document, and sets has_note from the notes table.
func overlayMetadata(client, previous map[string]any, hasNote bool) map[string]any {
	stored := make(map[string]any, len(client)+3)
	for key, value := range client {
		if key == "prettified-request" || key == "prettified-response" || key == "has_note" {
			continue
		}
		stored[key] = value
	}
	if value, ok := previous["prettified-request"]; ok {
		stored["prettified-request"] = value
	}
	if value, ok := previous["prettified-response"]; ok {
		stored["prettified-response"] = value
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
