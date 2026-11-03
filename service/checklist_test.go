package service

import (
	"errors"
	"io/fs"
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

	t.Run("should serve the bundled default without writing it when missing", func(t *testing.T) {
		configDir := t.TempDir()
		server := newTestServer(&marasi.Proxy{ConfigDir: configDir}, func() {})
		response := httptest.NewRecorder()

		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-case/checklist", nil))

		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"title":"OWASP Web Security Testing Guide (WSTG)"`) || !strings.Contains(response.Body.String(), `"category":"Information Gathering"`) {
			t.Fatalf("\nwanted:\n200 with the bundled default\ngot:\n%d %s", response.Code, response.Body.String())
		}
		if _, err := os.Stat(filepath.Join(configDir, "test_cases.yml")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", fs.ErrNotExist, err)
		}
	})
}

func TestWriteDefaultTestCaseChecklist(t *testing.T) {
	t.Run("should write the bundled default when no checklist exists", func(t *testing.T) {
		configDir := t.TempDir()

		if err := WriteDefaultTestCaseChecklist(configDir); err != nil {
			t.Fatal(err)
		}

		written, err := os.ReadFile(filepath.Join(configDir, "test_cases.yml"))
		if err != nil || string(written) != string(defaultTestCasesYAML) {
			t.Fatalf("\nwanted:\nthe bundled default\ngot:\n%q %v", written, err)
		}
	})

	t.Run("should keep a checklist the user wrote", func(t *testing.T) {
		configDir := t.TempDir()
		path := filepath.Join(configDir, "test_cases.yml")
		want := "title: Mine\ntest_cases: []\n"
		if err := os.WriteFile(path, []byte(want), 0600); err != nil {
			t.Fatal(err)
		}

		if err := WriteDefaultTestCaseChecklist(configDir); err != nil {
			t.Fatal(err)
		}

		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q %v", want, got, err)
		}
	})
}
