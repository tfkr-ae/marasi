package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/report"
)

func addReportRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /report/template", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 0 {
			writeJSON(w, r, http.StatusBadRequest, struct {
				Error string `json:"error"`
			}{Error: "invalid_report_template_request"})
			return
		}
		if proxy == nil {
			writeJSON(w, r, http.StatusNotFound, struct {
				Error string `json:"error"`
			}{Error: "not_found"})
			return
		}
		generator, ok := proxy.ReportGenerator.(interface {
			ListTemplateDetails() ([]report.TemplateInfo, error)
		})
		if !ok {
			writeJSON(w, r, http.StatusNotFound, struct {
				Error string `json:"error"`
			}{Error: "not_found"})
			return
		}
		items, err := generator.ListTemplateDetails()
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, struct {
				Error string `json:"error"`
			}{Error: "internal_server_error"})
			return
		}
		writeJSON(w, r, http.StatusOK, struct {
			Items []report.TemplateInfo `json:"items"`
		}{Items: items})
	})

	mux.HandleFunc("POST /report/template", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		var request struct {
			Path string `json:"path"`
		}
		if err != nil || len(query) != 0 {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			return
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Path == "" {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			return
		}
		if proxy == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		generator, ok := proxy.ReportGenerator.(interface {
			AddTemplate(string) error
			ListTemplateDetails() ([]report.TemplateInfo, error)
		})
		if !ok {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if err := generator.AddTemplate(request.Path); err != nil {
			switch {
			case errors.Is(err, report.ErrTemplateAlreadyExists):
				writeJSON(w, r, http.StatusConflict, map[string]string{"error": "report_template_already_exists"})
			case errors.Is(err, report.ErrInvalidTemplateSource):
				writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			case errors.Is(err, os.ErrNotExist):
				writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			default:
				writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			}
			return
		}
		items, err := generator.ListTemplateDetails()
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		response := struct {
			Items []report.TemplateInfo `json:"items"`
		}{Items: items}
		events.publish("report.template.added", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}
