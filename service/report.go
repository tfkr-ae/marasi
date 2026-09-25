package service

import (
	"net/http"
	"net/url"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/report"
)

func addReportRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
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
}
