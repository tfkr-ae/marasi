package service

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type trafficList struct {
	Items      []trafficSummary `json:"items"`
	NextCursor *uuid.UUID       `json:"next_cursor"`
}

type trafficSummary struct {
	ID          uuid.UUID      `json:"id"`
	Scheme      string         `json:"scheme"`
	Method      string         `json:"method"`
	Host        string         `json:"host"`
	Path        string         `json:"path"`
	Status      string         `json:"status"`
	StatusCode  int            `json:"status_code"`
	ContentType string         `json:"content_type"`
	Length      string         `json:"length"`
	Metadata    map[string]any `json:"metadata"`
	RequestedAt time.Time      `json:"requested_at"`
	RespondedAt *time.Time     `json:"responded_at"`
}

type trafficDetail struct {
	ID       uuid.UUID       `json:"id"`
	Note     string          `json:"note"`
	Metadata map[string]any  `json:"metadata"`
	Request  trafficRequest  `json:"request"`
	Response trafficResponse `json:"response"`
}

type trafficRequest struct {
	Scheme      string    `json:"scheme"`
	Method      string    `json:"method"`
	Host        string    `json:"host"`
	Path        string    `json:"path"`
	Raw         []byte    `json:"raw"`
	RequestedAt time.Time `json:"requested_at"`
}

type trafficResponse struct {
	Status      string    `json:"status"`
	StatusCode  int       `json:"status_code"`
	ContentType string    `json:"content_type"`
	Length      string    `json:"length"`
	Raw         []byte    `json:"raw"`
	RespondedAt time.Time `json:"responded_at"`
}

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

func trafficListFromSummaries(items []*domain.RequestResponseSummary, nextCursor *uuid.UUID) trafficList {
	summaries := make([]trafficSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, trafficSummaryFromDomain(item))
	}
	return trafficList{Items: summaries, NextCursor: nextCursor}
}

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
