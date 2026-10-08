package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi"
)

func TestTestCaseChecklistControlAPI(t *testing.T) {
	t.Run("returns the configured checklist without ids", func(t *testing.T) {
		configDir := t.TempDir()
		contents := `title: Team checklist
description: Current scope
version: "2"
test_cases:
  - title: Check authorization
    description: Try another user's id
    category: Access control
`
		if err := os.WriteFile(filepath.Join(configDir, "test_cases.yml"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		server := newTestServer(&marasi.Proxy{ConfigDir: configDir}, func() {})
		response := httptest.NewRecorder()

		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case/checklist", nil))

		want := `{"title":"Team checklist","description":"Current scope","version":"2","items":[{"title":"Check authorization","description":"Try another user's id","category":"Access control"}]}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want || strings.Contains(response.Body.String(), `"id"`) {
			t.Fatalf("wanted checklist %s without ids, got %d %s", want, response.Code, response.Body.String())
		}
	})

	t.Run("keeps empty items stable", func(t *testing.T) {
		configDir := t.TempDir()
		contents := "title: Empty\ndescription: None\nversion: \"1\"\ntest_cases: []\n"
		if err := os.WriteFile(filepath.Join(configDir, "test_cases.yml"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		server := newTestServer(&marasi.Proxy{ConfigDir: configDir}, func() {})
		response := httptest.NewRecorder()

		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case/checklist", nil))

		want := "{\"title\":\"Empty\",\"description\":\"None\",\"version\":\"1\",\"items\":[]}\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("wanted stable empty checklist %s, got %d %s", want, response.Code, response.Body.String())
		}
	})

	t.Run("writes and serves the bundled default when missing", func(t *testing.T) {
		configDir := t.TempDir()
		path := filepath.Join(configDir, "test_cases.yml")
		server := newTestServer(&marasi.Proxy{ConfigDir: configDir}, func() {})
		response := httptest.NewRecorder()

		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case/checklist", nil))

		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("wanted default checklist written: %v", err)
		}
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"title":"OWASP Web Security Testing Guide (WSTG)"`) || !strings.Contains(response.Body.String(), `"category":"Information Gathering"`) {
			t.Fatalf("wanted bundled default response, got %d %s", response.Code, response.Body.String())
		}
		if string(written) != string(defaultTestCasesYAML) {
			t.Fatal("written checklist did not match the bundled default")
		}
	})
}
