package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
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

	mux.HandleFunc("DELETE /report/template/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 0 || !report.ValidTemplateName(name) {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			return
		}
		if proxy == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		generator, ok := proxy.ReportGenerator.(interface {
			RemoveTemplate(string) error
			ListTemplateDetails() ([]report.TemplateInfo, error)
		})
		if !ok {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if err := generator.RemoveTemplate(name); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			} else {
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
		events.publish("report.template.removed", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /report/template/restore", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 0 {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_report_template_request"})
			return
		}
		if proxy == nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		generator, ok := proxy.ReportGenerator.(interface {
			ListTemplateDetails() ([]report.TemplateInfo, error)
		})
		if !ok {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if err := report.RestoreDefaultTemplate(proxy.ReportGenerator); err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
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
		events.publish("report.template.restored", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /report", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 0 {
			writeReportError(w, r, http.StatusBadRequest, "invalid_report_request")
			return
		}
		var request reportExportRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeReportError(w, r, http.StatusBadRequest, "invalid_report_request")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeReportError(w, r, http.StatusBadRequest, "invalid_report_request")
			return
		}
		export, err := request.parsed()
		if err != nil {
			writeReportError(w, r, http.StatusBadRequest, "invalid_report_request")
			return
		}
		if proxy == nil || proxy.ReportGenerator == nil {
			writeReportError(w, r, http.StatusNotFound, "not_found")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeReportError(w, r, http.StatusNotFound, "not_found")
			return
		}
		raw, err := proxy.ReportGenerator.LoadTemplate(export.template)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeReportError(w, r, http.StatusNotFound, "not_found")
			} else {
				writeReportError(w, r, http.StatusInternalServerError, "internal_server_error")
			}
			return
		}
		findings, err := repo.ListFindings()
		if err != nil {
			writeReportError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		testCases := []*domain.TestCase{}
		if export.includeTestCases {
			testCases, err = repo.ListTestCases()
			if err != nil {
				writeReportError(w, r, http.StatusInternalServerError, "internal_server_error")
				return
			}
		}
		rendered, err := proxy.ReportGenerator.Execute(raw, &domain.ReportPayload{
			Metadata: domain.ReportMetadata{
				Title:            export.title,
				Client:           export.client,
				Type:             export.assessmentType,
				IsDraft:          export.isDraft,
				Scope:            export.scope,
				Assessor:         export.assessor,
				Start:            export.start,
				End:              export.end,
				CreatedAt:        time.Now().UTC(),
				TruncateLength:   export.truncateLength,
				CustomProperties: map[string]any{},
			},
			TestCases: testCases,
			Findings:  findings,
		})
		if err != nil {
			writeJSON(w, r, http.StatusBadRequest, struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}{Error: "invalid_report_request", Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rendered)
	})
}

type reportExportRequest struct {
	Template         string `json:"template"`
	Title            string `json:"title"`
	Client           string `json:"client"`
	Type             string `json:"type"`
	Scope            string `json:"scope"`
	Assessor         string `json:"assessor"`
	Start            string `json:"start"`
	End              string `json:"end"`
	IsDraft          *bool  `json:"is_draft"`
	TruncateLength   *int   `json:"truncate_length"`
	IncludeTestCases *bool  `json:"include_test_cases"`
}

type parsedReportExport struct {
	template         string
	title            string
	client           string
	assessmentType   string
	scope            string
	assessor         string
	start            time.Time
	end              time.Time
	isDraft          bool
	truncateLength   int
	includeTestCases bool
}

func (request reportExportRequest) parsed() (parsedReportExport, error) {
	if !report.ValidTemplateName(request.Template) {
		return parsedReportExport{}, report.ErrInvalidTemplateName
	}
	start, err := reportDate(request.Start)
	if err != nil {
		return parsedReportExport{}, err
	}
	end, err := reportDate(request.End)
	if err != nil {
		return parsedReportExport{}, err
	}
	truncateLength := 0
	if request.TruncateLength != nil {
		if *request.TruncateLength < 0 {
			return parsedReportExport{}, errors.New("negative truncate length")
		}
		truncateLength = *request.TruncateLength
	}
	isDraft := true
	if request.IsDraft != nil {
		isDraft = *request.IsDraft
	}
	includeTestCases := true
	if request.IncludeTestCases != nil {
		includeTestCases = *request.IncludeTestCases
	}
	return parsedReportExport{
		template:         request.Template,
		title:            request.Title,
		client:           request.Client,
		assessmentType:   request.Type,
		scope:            request.Scope,
		assessor:         request.Assessor,
		start:            start,
		end:              end,
		isDraft:          isDraft,
		truncateLength:   truncateLength,
		includeTestCases: includeTestCases,
	}, nil
}

func reportDate(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC), nil
}

func writeReportError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, map[string]string{"error": code})
}

func invalidReportTemplatePath(path string) bool {
	const prefix = "/report/template/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return !report.ValidTemplateName(strings.TrimPrefix(path, prefix))
}
