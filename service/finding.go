package service

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type findingMutation struct {
	Title         *string
	Severity      *string
	CVSSVector    *string
	CVSSScore     *float64
	WriteUp       *string
	TreatmentPlan *string
	TestCaseID    *uuid.UUID
	TestCaseIDSet bool
}

type findingResponse struct {
	ID            uuid.UUID  `json:"id"`
	TestCaseID    *uuid.UUID `json:"test_case_id"`
	Title         string     `json:"title"`
	Severity      string     `json:"severity"`
	CVSSVector    string     `json:"cvss_vector"`
	CVSSScore     float64    `json:"cvss_score"`
	WriteUp       string     `json:"writeup"`
	TreatmentPlan string     `json:"treatment_plan"`
	CreatedAt     time.Time  `json:"created_at"`
}

type findingSummary struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	Severity   string     `json:"severity"`
	TestCaseID *uuid.UUID `json:"test_case_id"`
	CVSSScore  float64    `json:"cvss_score"`
	CreatedAt  time.Time  `json:"created_at"`
}

type findingDetail struct {
	findingResponse
	Items     []trafficSummary `json:"items"`
	Artifacts []any            `json:"artifacts"`
}

type findingTrafficResponse struct {
	FindingID uuid.UUID `json:"finding_id"`
	ID        uuid.UUID `json:"id"`
}

func addFindingRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("GET /finding", func(w http.ResponseWriter, r *http.Request) {
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		findings, err := repo.ListFindings()
		if err != nil {
			writeFindingError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		items := make([]findingSummary, 0, len(findings))
		for _, finding := range findings {
			items = append(items, findingSummary{
				ID: finding.ID, Title: finding.Title, Severity: finding.Severity,
				TestCaseID: finding.TestCaseID, CVSSScore: finding.CVSSScore, CreatedAt: finding.CreatedAt,
			})
		}
		writeJSON(w, r, http.StatusOK, struct {
			Items []findingSummary `json:"items"`
		}{Items: items})
	})

	mux.HandleFunc("POST /finding", func(w http.ResponseWriter, r *http.Request) {
		mutation, err := decodeFindingMutation(r)
		if err != nil || mutation.Title == nil || *mutation.Title == "" {
			writeFindingError(w, r, http.StatusBadRequest, "invalid_finding_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if mutation.TestCaseIDSet && mutation.TestCaseID != nil && !findingTestCaseExists(w, r, repo, *mutation.TestCaseID) {
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			writeFindingError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		finding := &domain.Finding{ID: id, Title: *mutation.Title, Requests: []uuid.UUID{}, Artifacts: []*domain.ArtifactMetadata{}}
		applyFindingMutation(finding, mutation)
		if err := repo.SaveFinding(finding); err != nil {
			writeFindingError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		finding, err = repo.GetFinding(id)
		if err != nil {
			writeFindingError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response := findingResponseFromDomain(finding)
		events.publish("finding.created", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("GET /finding/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseFindingID(w, r)
		if !ok {
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		finding, err := repo.GetFinding(id)
		if err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		members, err := repo.GetFindingRequests(id)
		if err != nil {
			writeFindingError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		items := make([]trafficSummary, 0, len(members))
		for _, member := range members {
			items = append(items, trafficSummaryFromDomain(member))
		}
		writeJSON(w, r, http.StatusOK, findingDetail{
			findingResponse: findingResponseFromDomain(finding),
			Items:           items,
			Artifacts:       []any{},
		})
	})

	mux.HandleFunc("POST /finding/{id}/traffic", func(w http.ResponseWriter, r *http.Request) {
		findingID, ok := parseFindingID(w, r)
		if !ok {
			return
		}
		requestID, err := decodeTrafficLink(r)
		if err != nil {
			writeFindingError(w, r, http.StatusBadRequest, "invalid_finding_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetFinding(findingID); err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		trafficRepo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := trafficRepo.GetRequestResponseRow(requestID); err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := repo.LinkRequestToFinding(findingID, requestID); err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		response := findingTrafficResponse{FindingID: findingID, ID: requestID}
		events.publish("finding.linked", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /finding/{id}/traffic/{request_id}", func(w http.ResponseWriter, r *http.Request) {
		findingID, ok := parseFindingID(w, r)
		if !ok {
			return
		}
		requestID, err := uuid.Parse(r.PathValue("request_id"))
		if err != nil {
			writeFindingError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetFinding(findingID); err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		if err := repo.UnlinkRequestFromFinding(findingID, requestID); err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		response := findingTrafficResponse{FindingID: findingID, ID: requestID}
		events.publish("finding.unlinked", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /finding/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseFindingID(w, r)
		if !ok {
			return
		}
		mutation, err := decodeFindingMutation(r)
		if err != nil || mutation.Title != nil && *mutation.Title == "" {
			writeFindingError(w, r, http.StatusBadRequest, "invalid_finding_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		finding, err := repo.GetFinding(id)
		if err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		if mutation.TestCaseIDSet && mutation.TestCaseID != nil && !findingTestCaseExists(w, r, repo, *mutation.TestCaseID) {
			return
		}
		applyFindingMutation(finding, mutation)
		if err := repo.SaveFinding(finding); err != nil {
			writeFindingError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		finding, err = repo.GetFinding(id)
		if err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		response := findingResponseFromDomain(finding)
		events.publish("finding.updated", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /finding/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseFindingID(w, r)
		if !ok {
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeFindingError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetFinding(id); err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		if err := repo.DeleteFinding(id); err != nil {
			writeFindingRepositoryError(w, r, err)
			return
		}
		response := struct {
			ID uuid.UUID `json:"id"`
		}{ID: id}
		events.publish("finding.deleted", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}

func decodeFindingMutation(r *http.Request) (findingMutation, error) {
	if r.Body == nil {
		return findingMutation{}, errors.New("invalid finding request")
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return findingMutation{}, errors.New("invalid finding request")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return findingMutation{}, errors.New("invalid finding request")
	}
	var mutation findingMutation
	for name, raw := range fields {
		isNull := bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		if isNull && name != "test_case_id" {
			return findingMutation{}, errors.New("invalid finding request")
		}
		switch name {
		case "title":
			if err := json.Unmarshal(raw, &mutation.Title); err != nil {
				return findingMutation{}, err
			}
		case "severity":
			if err := json.Unmarshal(raw, &mutation.Severity); err != nil || mutation.Severity == nil || !validFindingSeverity(*mutation.Severity) {
				return findingMutation{}, errors.New("invalid finding severity")
			}
		case "cvss_vector":
			if err := json.Unmarshal(raw, &mutation.CVSSVector); err != nil {
				return findingMutation{}, err
			}
		case "cvss_score":
			if err := json.Unmarshal(raw, &mutation.CVSSScore); err != nil {
				return findingMutation{}, err
			}
		case "writeup":
			if err := json.Unmarshal(raw, &mutation.WriteUp); err != nil {
				return findingMutation{}, err
			}
		case "treatment_plan":
			if err := json.Unmarshal(raw, &mutation.TreatmentPlan); err != nil {
				return findingMutation{}, err
			}
		case "test_case_id":
			mutation.TestCaseIDSet = true
			if isNull {
				continue
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || value == "" {
				return findingMutation{}, errors.New("invalid test case id")
			}
			id, err := uuid.Parse(value)
			if err != nil {
				return findingMutation{}, errors.New("invalid test case id")
			}
			mutation.TestCaseID = &id
		default:
			return findingMutation{}, errors.New("invalid finding request")
		}
	}
	return mutation, nil
}

func validFindingSeverity(severity string) bool {
	switch severity {
	case "", "Critical", "High", "Medium", "Low", "Informational":
		return true
	default:
		return false
	}
}

func applyFindingMutation(finding *domain.Finding, mutation findingMutation) {
	if mutation.Title != nil {
		finding.Title = *mutation.Title
	}
	if mutation.Severity != nil {
		finding.Severity = *mutation.Severity
	}
	if mutation.CVSSVector != nil {
		finding.CVSSVector = *mutation.CVSSVector
	}
	if mutation.CVSSScore != nil {
		finding.CVSSScore = *mutation.CVSSScore
	}
	if mutation.WriteUp != nil {
		finding.WriteUp = *mutation.WriteUp
	}
	if mutation.TreatmentPlan != nil {
		finding.TreatmentPlan = *mutation.TreatmentPlan
	}
	if mutation.TestCaseIDSet {
		finding.TestCaseID = mutation.TestCaseID
	}
}

func findingResponseFromDomain(finding *domain.Finding) findingResponse {
	return findingResponse{
		ID: finding.ID, TestCaseID: finding.TestCaseID, Title: finding.Title, Severity: finding.Severity,
		CVSSVector: finding.CVSSVector, CVSSScore: finding.CVSSScore, WriteUp: finding.WriteUp,
		TreatmentPlan: finding.TreatmentPlan, CreatedAt: finding.CreatedAt,
	}
}

func findingTestCaseExists(w http.ResponseWriter, r *http.Request, repo domain.ReportingRepository, id uuid.UUID) bool {
	if _, err := repo.GetTestCase(id); err != nil {
		writeFindingRepositoryError(w, r, err)
		return false
	}
	return true
}

func parseFindingID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeFindingError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func writeFindingError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeFindingRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrReportingAlreadyLinked) {
		writeFindingError(w, r, http.StatusConflict, "already_linked")
		return
	}
	if errors.Is(err, domain.ErrReportingNotLinked) {
		writeFindingError(w, r, http.StatusNotFound, "not_found")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeFindingError(w, r, http.StatusNotFound, "not_found")
		return
	}
	writeFindingError(w, r, http.StatusInternalServerError, "internal_server_error")
}
