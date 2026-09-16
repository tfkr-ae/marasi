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

type testCaseMutation struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Category    *string   `json:"category"`
	Tags        *[]string `json:"tags"`
	Note        *string   `json:"note"`
}

type testCaseResponse struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Tags        []string  `json:"tags"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
}

type testCaseSummary struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Category  string    `json:"category"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

type testCaseDetail struct {
	testCaseResponse
	Items     []trafficSummary   `json:"items"`
	Artifacts []artifactResponse `json:"artifacts"`
}

type testCaseTrafficResponse struct {
	TestCaseID uuid.UUID `json:"test_case_id"`
	ID         uuid.UUID `json:"id"`
}

func addTestCaseRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	addTestCaseChecklistRoute(mux, proxy)

	mux.HandleFunc("GET /test-case", func(w http.ResponseWriter, r *http.Request) {
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		testCases, err := repo.ListTestCases()
		if err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		items := make([]testCaseSummary, 0, len(testCases))
		for _, testCase := range testCases {
			tags := testCase.Tags
			if tags == nil {
				tags = []string{}
			}
			items = append(items, testCaseSummary{ID: testCase.ID, Title: testCase.Title, Category: testCase.Category, Tags: tags, CreatedAt: testCase.CreatedAt})
		}
		writeJSON(w, r, http.StatusOK, struct {
			Items []testCaseSummary `json:"items"`
		}{Items: items})
	})

	mux.HandleFunc("POST /test-case", func(w http.ResponseWriter, r *http.Request) {
		mutation, err := decodeTestCaseMutation(r)
		if err != nil || mutation.Title == nil || *mutation.Title == "" {
			writeTestCaseError(w, r, http.StatusBadRequest, "invalid_test_case_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		testCase := &domain.TestCase{ID: id, Title: *mutation.Title, Tags: []string{}, Requests: []uuid.UUID{}, Artifacts: []*domain.ArtifactMetadata{}}
		applyTestCaseMutation(testCase, mutation)
		if err := repo.SaveTestCase(testCase); err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		testCase, err = repo.GetTestCase(id)
		if err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		response := testCaseResponseFromDomain(testCase)
		events.publish("test-case.created", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("GET /test-case/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeTestCaseError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		testCase, err := repo.GetTestCase(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			} else {
				writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			}
			return
		}
		members, err := repo.GetTestCaseRequests(id)
		if err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		items := make([]trafficSummary, 0, len(members))
		for _, member := range members {
			items = append(items, trafficSummaryFromDomain(member))
		}
		writeJSON(w, r, http.StatusOK, testCaseDetail{
			testCaseResponse: testCaseResponseFromDomain(testCase),
			Items:            items,
			Artifacts:        artifactResponsesForTestCase(testCase),
		})
	})

	mux.HandleFunc("POST /test-case/{id}/traffic", func(w http.ResponseWriter, r *http.Request) {
		testCaseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeTestCaseError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		requestID, err := decodeTrafficLink(r)
		if err != nil {
			writeTestCaseError(w, r, http.StatusBadRequest, "invalid_test_case_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetTestCase(testCaseID); err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		trafficRepo, err := proxy.GetTrafficRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := trafficRepo.GetRequestResponseRow(requestID); err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if err := repo.LinkRequestToTestCase(testCaseID, requestID); err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		response := testCaseTrafficResponse{TestCaseID: testCaseID, ID: requestID}
		events.publish("test-case.linked", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /test-case/{id}/traffic/{request_id}", func(w http.ResponseWriter, r *http.Request) {
		testCaseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeTestCaseError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		requestID, err := uuid.Parse(r.PathValue("request_id"))
		if err != nil {
			writeTestCaseError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetTestCase(testCaseID); err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		if err := repo.UnlinkRequestFromTestCase(testCaseID, requestID); err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		response := testCaseTrafficResponse{TestCaseID: testCaseID, ID: requestID}
		events.publish("test-case.unlinked", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("POST /test-case/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeTestCaseError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		mutation, err := decodeTestCaseMutation(r)
		if err != nil || mutation.Title != nil && *mutation.Title == "" {
			writeTestCaseError(w, r, http.StatusBadRequest, "invalid_test_case_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		testCase, err := repo.GetTestCase(id)
		if err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		applyTestCaseMutation(testCase, mutation)
		if err := repo.SaveTestCase(testCase); err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		testCase, err = repo.GetTestCase(id)
		if err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		response := testCaseResponseFromDomain(testCase)
		events.publish("test-case.updated", response)
		writeJSON(w, r, http.StatusOK, response)
	})

	mux.HandleFunc("DELETE /test-case/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeTestCaseError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeTestCaseError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetTestCase(id); err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		if err := repo.DeleteTestCase(id); err != nil {
			writeTestCaseRepositoryError(w, r, err)
			return
		}
		response := struct {
			ID uuid.UUID `json:"id"`
		}{ID: id}
		events.publish("test-case.deleted", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}

func decodeTestCaseMutation(r *http.Request) (testCaseMutation, error) {
	if r.Body == nil {
		return testCaseMutation{}, errors.New("invalid test case request")
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return testCaseMutation{}, err
	}
	if fields == nil {
		return testCaseMutation{}, errors.New("invalid test case request")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return testCaseMutation{}, errors.New("invalid test case request")
	}
	var mutation testCaseMutation
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return testCaseMutation{}, errors.New("invalid test case request")
		}
		switch name {
		case "title":
			if err := json.Unmarshal(raw, &mutation.Title); err != nil {
				return testCaseMutation{}, err
			}
		case "description":
			if err := json.Unmarshal(raw, &mutation.Description); err != nil {
				return testCaseMutation{}, err
			}
		case "category":
			if err := json.Unmarshal(raw, &mutation.Category); err != nil {
				return testCaseMutation{}, err
			}
		case "tags":
			if err := json.Unmarshal(raw, &mutation.Tags); err != nil {
				return testCaseMutation{}, err
			}
		case "note":
			if err := json.Unmarshal(raw, &mutation.Note); err != nil {
				return testCaseMutation{}, err
			}
		default:
			return testCaseMutation{}, errors.New("invalid test case request")
		}
	}
	return mutation, nil
}

func decodeTrafficLink(r *http.Request) (uuid.UUID, error) {
	if r.Body == nil {
		return uuid.Nil, errors.New("invalid traffic link")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		ID string `json:"id"`
	}
	if err := decoder.Decode(&body); err != nil {
		return uuid.Nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return uuid.Nil, errors.New("invalid traffic link")
	}
	return uuid.Parse(body.ID)
}

func applyTestCaseMutation(testCase *domain.TestCase, mutation testCaseMutation) {
	if mutation.Title != nil {
		testCase.Title = *mutation.Title
	}
	if mutation.Description != nil {
		testCase.Description = *mutation.Description
	}
	if mutation.Category != nil {
		testCase.Category = *mutation.Category
	}
	if mutation.Tags != nil {
		testCase.Tags = *mutation.Tags
	}
	if mutation.Note != nil {
		testCase.Note = *mutation.Note
	}
}

func testCaseResponseFromDomain(testCase *domain.TestCase) testCaseResponse {
	tags := testCase.Tags
	if tags == nil {
		tags = []string{}
	}
	return testCaseResponse{
		ID:          testCase.ID,
		Title:       testCase.Title,
		Description: testCase.Description,
		Category:    testCase.Category,
		Tags:        tags,
		Note:        testCase.Note,
		CreatedAt:   testCase.CreatedAt,
	}
}

func writeTestCaseError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeTestCaseRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrReportingAlreadyLinked) {
		writeTestCaseError(w, r, http.StatusConflict, "already_linked")
		return
	}
	if errors.Is(err, domain.ErrReportingNotLinked) {
		writeTestCaseError(w, r, http.StatusNotFound, "not_found")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeTestCaseError(w, r, http.StatusNotFound, "not_found")
		return
	}
	writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
}
