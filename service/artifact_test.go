package service

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

type stubArtifactRepository struct {
	domain.ReportingRepository
	testCases map[uuid.UUID]*domain.TestCase
	findings  map[uuid.UUID]*domain.Finding
	artifacts map[uuid.UUID]*domain.Artifact
}

func TestArtifactList(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/artifacts.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	server := newTestServer(&marasi.Proxy{ReportingRepo: db.NewProxyRepo(conn)}, func() {})
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/artifact", ""), http.StatusOK, "{\"items\":[]}\n")
	uploads := make([]artifactResponse, 0, 2)
	for _, parent := range []string{"test-case", "finding"} {
		created := requestControlAPI(server, http.MethodPost, "/"+parent, `{"title":"Evidence"}`)
		var resource struct {
			ID string `json:"id"`
		}
		if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &resource) != nil || resource.ID == "" {
			t.Fatalf("creating parent: %s", created.Body.String())
		}
		uploaded := requestControlAPI(server, http.MethodPost, "/"+parent+"/"+resource.ID+"/artifact?filename="+parent+".txt", "proof")
		var artifact artifactResponse
		if uploaded.Code != http.StatusOK || json.Unmarshal(uploaded.Body.Bytes(), &artifact) != nil {
			t.Fatalf("uploading artifact: %s", uploaded.Body.String())
		}
		uploads = append(uploads, artifact)
	}
	listed := requestControlAPI(server, http.MethodGet, "/artifact", "")
	want, err := json.Marshal(struct {
		Items []artifactResponse `json:"items"`
	}{Items: []artifactResponse{uploads[1], uploads[0]}})
	if err != nil {
		t.Fatal(err)
	}
	assertControlAPIResponse(t, listed, http.StatusOK, string(want)+"\n")
	for _, artifact := range uploads {
		requestControlAPI(server, http.MethodDelete, "/artifact/"+artifact.ID.String(), "")
	}
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/artifact", ""), http.StatusOK, "{\"items\":[]}\n")
	assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodGet, "/artifact", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
}

func (repo *stubArtifactRepository) GetTestCase(id uuid.UUID) (*domain.TestCase, error) {
	testCase, ok := repo.testCases[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return testCase, nil
}

func (repo *stubArtifactRepository) GetTestCaseRequests(uuid.UUID) ([]*domain.RequestResponseSummary, error) {
	return []*domain.RequestResponseSummary{}, nil
}

func (repo *stubArtifactRepository) GetFinding(id uuid.UUID) (*domain.Finding, error) {
	finding, ok := repo.findings[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return finding, nil
}

func (repo *stubArtifactRepository) GetFindingRequests(uuid.UUID) ([]*domain.RequestResponseSummary, error) {
	return []*domain.RequestResponseSummary{}, nil
}

func (repo *stubArtifactRepository) SaveArtifact(artifact *domain.Artifact) error {
	if repo.artifacts == nil {
		repo.artifacts = make(map[uuid.UUID]*domain.Artifact)
	}
	copy := *artifact
	metadata := *artifact.ArtifactMetadata
	metadata.CreatedAt = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	copy.ArtifactMetadata = &metadata
	copy.Data = append([]byte(nil), artifact.Data...)
	repo.artifacts[artifact.ID] = &copy
	if artifact.TestCaseID != nil {
		repo.testCases[*artifact.TestCaseID].Artifacts = append(repo.testCases[*artifact.TestCaseID].Artifacts, &metadata)
	} else {
		repo.findings[*artifact.FindingID].Artifacts = append(repo.findings[*artifact.FindingID].Artifacts, &metadata)
	}
	return nil
}

func (repo *stubArtifactRepository) GetArtifact(id uuid.UUID) (*domain.Artifact, error) {
	artifact, ok := repo.artifacts[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return artifact, nil
}

func (repo *stubArtifactRepository) DeleteArtifact(id uuid.UUID) error {
	if _, ok := repo.artifacts[id]; !ok {
		return sql.ErrNoRows
	}
	delete(repo.artifacts, id)
	return nil
}

func TestArtifactControlAPI(t *testing.T) {
	testCaseID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
	findingID := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	repo := &stubArtifactRepository{
		testCases: map[uuid.UUID]*domain.TestCase{testCaseID: {ID: testCaseID, Title: "Access control", Tags: []string{}, Artifacts: []*domain.ArtifactMetadata{}}},
		findings:  map[uuid.UUID]*domain.Finding{findingID: {ID: findingID, Title: "Finding", Artifacts: []*domain.ArtifactMetadata{}}},
		artifacts: map[uuid.UUID]*domain.Artifact{},
	}
	server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)

	t.Run("uploads raw bytes to a test case and emits metadata", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/test-case/"+testCaseID.String()+"/artifact?filename=proof.png", strings.NewReader("PNG bytes"))
		request.Header.Set("Content-Type", "image/png")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("wanted 200, got %d: %s", response.Code, response.Body.String())
		}
		var metadata artifactResponse
		if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.ID.Version() != 7 || metadata.Filename != "proof.png" || metadata.MimeType != "image/png" || metadata.Size != 9 || metadata.TestCaseID == nil || *metadata.TestCaseID != testCaseID || metadata.FindingID != nil {
			t.Fatalf("unexpected metadata: %+v", metadata)
		}
		stored := repo.artifacts[metadata.ID]
		if string(stored.Data) != "PNG bytes" {
			t.Fatalf("wanted raw bytes, got %q", stored.Data)
		}
		event := <-subscriber.events
		if event.name != "artifact.created" || string(event.data) != strings.TrimSpace(response.Body.String()) {
			t.Fatalf("unexpected event %q %s", event.name, event.data)
		}
	})

	t.Run("gets metadata and content, then deletes", func(t *testing.T) {
		var id uuid.UUID
		for artifactID := range repo.artifacts {
			id = artifactID
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/artifact/"+id.String(), nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "PNG bytes") {
			t.Fatalf("unexpected metadata response %d %q", response.Code, response.Body.String())
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/artifact/"+id.String()+"/content", nil))
		if response.Code != http.StatusOK || response.Body.String() != "PNG bytes" || response.Header().Get("Content-Type") != "image/png" || !strings.Contains(response.Header().Get("Content-Disposition"), "proof.png") {
			t.Fatalf("unexpected content response: status %d headers %v body %q", response.Code, response.Header(), response.Body.String())
		}

		response = httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/artifact/"+id.String(), nil))
		if response.Code != http.StatusOK || response.Body.String() != `{"id":"`+id.String()+`"}`+"\n" {
			t.Fatalf("unexpected delete response %d %q", response.Code, response.Body.String())
		}
		event := <-subscriber.events
		if event.name != "artifact.deleted" || string(event.data) != `{"id":"`+id.String()+`"}` {
			t.Fatalf("unexpected event %q %s", event.name, event.data)
		}
	})

	t.Run("uses the default mime for a finding upload", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/finding/"+findingID.String()+"/artifact?filename=notes.bin", strings.NewReader("notes"))
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mime_type":"application/octet-stream"`) || !strings.Contains(response.Body.String(), `"finding_id":"`+findingID.String()+`"`) || !strings.Contains(response.Body.String(), `"test_case_id":null`) {
			t.Fatalf("unexpected finding upload response %d %q", response.Code, response.Body.String())
		}
		<-subscriber.events
	})

	t.Run("lists parent artifact metadata without bytes", func(t *testing.T) {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/finding/"+findingID.String(), nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"artifacts":[{"id":`) || strings.Contains(response.Body.String(), `"data"`) {
			t.Fatalf("unexpected parent response %d %q", response.Code, response.Body.String())
		}
	})
}

func TestArtifactControlAPIErrors(t *testing.T) {
	testCaseID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
	repo := &stubArtifactRepository{testCases: map[uuid.UUID]*domain.TestCase{testCaseID: {ID: testCaseID}}, findings: map[uuid.UUID]*domain.Finding{}, artifacts: map[uuid.UUID]*domain.Artifact{}}
	server := newTestServer(&marasi.Proxy{ReportingRepo: repo}, func() {})

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   []byte
		status int
		code   string
	}{
		{name: "missing filename", method: http.MethodPost, path: "/test-case/" + testCaseID.String() + "/artifact", status: http.StatusBadRequest, code: "bad_request"},
		{name: "missing parent", method: http.MethodPost, path: "/finding/0193802f-f0e7-73d9-a764-06d21e367809/artifact?filename=x", status: http.StatusNotFound, code: "not_found"},
		{name: "too large", method: http.MethodPost, path: "/test-case/" + testCaseID.String() + "/artifact?filename=x", body: bytes.Repeat([]byte{'x'}, maxArtifactSize+1), status: http.StatusBadRequest, code: "too_large"},
		{name: "malformed id", method: http.MethodGet, path: "/artifact/nope", status: http.StatusBadRequest, code: "bad_request"},
		{name: "missing artifact", method: http.MethodGet, path: "/artifact/0193802f-f0e7-73d9-a764-06d21e367809", status: http.StatusNotFound, code: "not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body)))
			if response.Code != test.status || response.Body.String() != `{"error":"`+test.code+`"}`+"\n" {
				t.Fatalf("wanted %d %s, got %d %q", test.status, test.code, response.Code, response.Body.String())
			}
		})
	}
}
