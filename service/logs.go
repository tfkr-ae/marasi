package service

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type proxyLogList struct {
	Items      []proxyLogResponse `json:"items"`
	NextCursor *uuid.UUID         `json:"next_cursor"`
}

type proxyLogResponse struct {
	ID          uuid.UUID      `json:"id"`
	Timestamp   string         `json:"timestamp"`
	Level       string         `json:"level"`
	Message     string         `json:"message"`
	Context     map[string]any `json:"context"`
	RequestID   *uuid.UUID     `json:"request_id"`
	ExtensionID *uuid.UUID     `json:"extension_id"`
}

func addLogRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		limit, cursor, err := parseNewestFirstPage(r)
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if proxy.LogRepo == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		logs, nextCursor, err := proxy.LogRepo.ListLogs(cursor, limit)
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		items := make([]proxyLogResponse, len(logs))
		for i, entry := range logs {
			items[i] = proxyLogFromDomain(entry)
		}
		writeJSON(w, r, http.StatusOK, proxyLogList{Items: items, NextCursor: nextCursor})
	})
}

func proxyLogFromDomain(entry *domain.Log) proxyLogResponse {
	context := entry.Context
	if context == nil {
		context = map[string]any{}
	}
	return proxyLogResponse{
		ID:          entry.ID,
		Timestamp:   entry.Timestamp.Format(time.RFC3339),
		Level:       entry.Level,
		Message:     entry.Message,
		Context:     context,
		RequestID:   entry.RequestID,
		ExtensionID: entry.ExtensionID,
	}
}
