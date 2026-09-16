package service

import (
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

const maxArtifactSize = 20 * 1024 * 1024

type artifactResponse struct {
	ID         uuid.UUID  `json:"id"`
	Filename   string     `json:"filename"`
	MimeType   string     `json:"mime_type"`
	Size       int64      `json:"size"`
	TestCaseID *uuid.UUID `json:"test_case_id"`
	FindingID  *uuid.UUID `json:"finding_id"`
	CreatedAt  time.Time  `json:"created_at"`
}

func addArtifactRoutes(mux *http.ServeMux, proxy *marasi.Proxy, events *eventBroadcaster) {
	mux.HandleFunc("POST /test-case/{id}/artifact", artifactUploadHandler(proxy, events, "test-case"))
	mux.HandleFunc("POST /finding/{id}/artifact", artifactUploadHandler(proxy, events, "finding"))

	mux.HandleFunc("GET /artifact/{id}", func(w http.ResponseWriter, r *http.Request) {
		artifact, ok := getArtifact(w, r, proxy)
		if !ok {
			return
		}
		writeJSON(w, r, http.StatusOK, artifactResponseFromDomain(artifact))
	})

	mux.HandleFunc("GET /artifact/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		artifact, ok := getArtifact(w, r, proxy)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", artifact.MimeType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Filename}))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(artifact.Data)
	})

	mux.HandleFunc("DELETE /artifact/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseArtifactID(w, r)
		if !ok {
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeArtifactError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if _, err := repo.GetArtifact(id); err != nil {
			writeArtifactRepositoryError(w, r, err)
			return
		}
		if err := repo.DeleteArtifact(id); err != nil {
			writeArtifactRepositoryError(w, r, err)
			return
		}
		response := struct {
			ID uuid.UUID `json:"id"`
		}{ID: id}
		events.publish("artifact.deleted", response)
		writeJSON(w, r, http.StatusOK, response)
	})
}

func artifactUploadHandler(proxy *marasi.Proxy, events *eventBroadcaster, parent string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parentID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeArtifactError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		filename := r.URL.Query().Get("filename")
		if filename == "" {
			writeArtifactError(w, r, http.StatusBadRequest, "bad_request")
			return
		}
		repo, err := proxy.GetReportingRepo()
		if err != nil {
			writeArtifactError(w, r, http.StatusNotFound, "not_found")
			return
		}
		if parent == "test-case" {
			_, err = repo.GetTestCase(parentID)
		} else {
			_, err = repo.GetFinding(parentID)
		}
		if err != nil {
			writeArtifactRepositoryError(w, r, err)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxArtifactSize+1))
		if err != nil {
			writeArtifactError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		if len(data) > maxArtifactSize {
			writeArtifactError(w, r, http.StatusBadRequest, "too_large")
			return
		}
		id, err := uuid.NewV7()
		if err != nil {
			writeArtifactError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		mimeType := r.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		artifact := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{ID: id, Filename: filename, MimeType: mimeType, Size: int64(len(data))},
			Data:             data,
		}
		if parent == "test-case" {
			artifact.TestCaseID = &parentID
		} else {
			artifact.FindingID = &parentID
		}
		if err := repo.SaveArtifact(artifact); err != nil {
			writeArtifactRepositoryError(w, r, err)
			return
		}
		artifact, err = repo.GetArtifact(id)
		if err != nil {
			writeArtifactRepositoryError(w, r, err)
			return
		}
		response := artifactResponseFromDomain(artifact)
		events.publish("artifact.created", response)
		writeJSON(w, r, http.StatusOK, response)
	}
}

func artifactResponseFromDomain(artifact *domain.Artifact) artifactResponse {
	return artifactResponse{
		ID: artifact.ID, Filename: artifact.Filename, MimeType: artifact.MimeType, Size: artifact.Size,
		TestCaseID: artifact.TestCaseID, FindingID: artifact.FindingID, CreatedAt: artifact.CreatedAt,
	}
}

func artifactResponsesForTestCase(testCase *domain.TestCase) []artifactResponse {
	items := make([]artifactResponse, 0, len(testCase.Artifacts))
	for _, artifact := range testCase.Artifacts {
		items = append(items, artifactResponse{
			ID: artifact.ID, Filename: artifact.Filename, MimeType: artifact.MimeType, Size: artifact.Size,
			TestCaseID: &testCase.ID, CreatedAt: artifact.CreatedAt,
		})
	}
	return items
}

func artifactResponsesForFinding(finding *domain.Finding) []artifactResponse {
	items := make([]artifactResponse, 0, len(finding.Artifacts))
	for _, artifact := range finding.Artifacts {
		items = append(items, artifactResponse{
			ID: artifact.ID, Filename: artifact.Filename, MimeType: artifact.MimeType, Size: artifact.Size,
			FindingID: &finding.ID, CreatedAt: artifact.CreatedAt,
		})
	}
	return items
}

func getArtifact(w http.ResponseWriter, r *http.Request, proxy *marasi.Proxy) (*domain.Artifact, bool) {
	id, ok := parseArtifactID(w, r)
	if !ok {
		return nil, false
	}
	repo, err := proxy.GetReportingRepo()
	if err != nil {
		writeArtifactError(w, r, http.StatusNotFound, "not_found")
		return nil, false
	}
	artifact, err := repo.GetArtifact(id)
	if err != nil {
		writeArtifactRepositoryError(w, r, err)
		return nil, false
	}
	return artifact, true
}

func parseArtifactID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeArtifactError(w, r, http.StatusBadRequest, "bad_request")
		return uuid.Nil, false
	}
	return id, true
}

func writeArtifactRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeArtifactError(w, r, http.StatusNotFound, "not_found")
		return
	}
	writeArtifactError(w, r, http.StatusInternalServerError, "internal_server_error")
}

func writeArtifactError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
