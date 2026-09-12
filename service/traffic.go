package service

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

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
